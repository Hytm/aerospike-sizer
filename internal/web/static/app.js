// Aerospike Sizer — the form drives /api/plan: every figure and explanation comes from the server.
(function () {
  "use strict";

  var RESOURCES = [
    { key: "storage", label: "Storage", ceiling: "storage_fill" },
    { key: "ram", label: "RAM", ceiling: "ram_fill" },
    { key: "cpu", label: "CPU", ceiling: "cpu_target" },
    { key: "iops", label: "IOPS", ceiling: "io_target" }
  ];
  var BINDINGS = {
    storage: "Storage-bound", ram: "RAM-bound", cpu: "CPU-bound", iops: "IOPS-bound",
    topology: "Topology floor (RF+1)", racks: "Rack balance"
  };
  var FIT = { ok: ["sev-ok", "OK"], tight: ["sev-warning", "Tight"], insufficient: ["sev-critical", "Insufficient"] };

  var defaults = null;   // the form as the interface opens
  var providers = [];    // catalog
  var pricesAsOf = "";   // date of the reference prices
  var ratesAsOf = "";    // date of the default network rates
  var livePrices = {};   // provider key → this server looks its prices up live
  var tight = 0.85;      // load above which a resource is tight (sizing.TightLoad)
  var tolerance = 1e-12; // relative error of the arithmetic a load may carry (sizing.RatioTolerance)
  var req = null;        // the form: workload, node, hypotheses, network
  var rows = [];         // configurations added by the user
  var auto = [];         // providers whose recommended configuration is listed
  var hiddenAlts = {};   // row id → its alternative was dismissed
  var last = null;       // last answer
  var seq = 0, timer = null, poll = null, polls = 0, nextId = 1, addNodesTouched = false;

  function $(id) { return document.getElementById(id); }
  function clone(o) { return JSON.parse(JSON.stringify(o)); }
  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function getPath(o, path) { return path.split(".").reduce(function (v, k) { return v == null ? v : v[k]; }, o); }
  function setPath(o, path, value) {
    var keys = path.split("."), leaf = keys.pop();
    keys.reduce(function (v, k) { return v[k]; }, o)[leaf] = value;
  }

  function scaled(b, base, names) {
    var i = 0;
    while (Math.abs(b) >= base && i < names.length - 1) { b /= base; i++; }
    return i === 0 ? Math.round(b) + " " + names[0] : b.toFixed(2) + " " + names[i];
  }
  function si(b) { return scaled(b, 1000, ["B", "KB", "MB", "GB", "TB", "PB"]); }
  function iec(b) { return scaled(b, 1024, ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]); }
  function count(n) { return Math.round(n).toLocaleString("en-US"); }
  function usd(n) { return "$" + Math.round(n).toLocaleString("en-US"); }
  function pct(f) { return Math.round(f * 100) + "%"; }
  function pctTenth(f) { return (f * 100).toFixed(1).replace(/\.0$/, "") + "%"; } // a measured share: 99.6%, not 100%
  function amount(key, v) {
    return key === "storage" ? si(v) : key === "ram" ? iec(v) : key === "cpu" ? v.toFixed(1) + " vCPU" : count(v);
  }

  function showFailure(err) {
    $("failure").textContent = "The sizer service did not answer: " + err.message;
    $("failure").hidden = false;
  }

  // ---- Form ↔ request ----

  function fields() { return document.querySelectorAll("[data-path]"); }
  function scaleOf(el) { return parseFloat(el.getAttribute("data-scale") || "1"); }
  function shown(el, v) { return +(v * scaleOf(el)).toFixed(6); }

  function fillForm() {
    fields().forEach(function (el) {
      var v = getPath(req, el.getAttribute("data-path"));
      if (el.classList.contains("seg")) {
        markSeg(el, String(v));
      } else if (el.type === "checkbox") {
        el.checked = !!v;
      } else {
        el.value = shown(el, v);
      }
    });
    refreshDependents();
  }

  // markSeg lights the button of the value. A value the buttons do not offer (an RF or an AZ count
  // prefilled from the URL) gets a button of its own, so that it stays visible and clickable.
  function markSeg(seg, v) {
    var found = false;
    seg.querySelectorAll("button").forEach(function (b) {
      var on = b.getAttribute("data-v") === v;
      if (b.classList.contains("extra") && !on) { b.remove(); return; }
      b.classList.toggle("on", on);
      b.setAttribute("aria-pressed", on ? "true" : "false");
      found = found || on;
    });
    if (found) return;
    var b = document.createElement("button");
    b.type = "button";
    b.className = "extra on";
    b.setAttribute("data-v", v);
    b.setAttribute("aria-pressed", "true");
    b.textContent = (seg.getAttribute("data-label") || "{v}").replace("{v}", v);
    seg.appendChild(b);
  }

  // netFlags tells which options apply: a network option shows unchecked and disabled while its
  // prerequisite is off, and the choice comes back with it; Active Rack's own inputs show only
  // when it engages (a full copy per rack: AZs ≤ RF); workload fields follow the engine and the
  // indexes.
  function netFlags() {
    var n = req.network, w = req.workload, rack = n.azs > 1 && n.rack_aware, active = rack && n.active_rack;
    var engaged = active && n.azs <= w.rf;
    return {
      az: n.mode === "az", region: n.mode === "region", rack: rack, active: active, local: rack && n.local_read,
      activeRack: engaged, coLocated: engaged && n.client_placement === "single", compression: n.xdr_compression,
      memory: w.engine === "memory", device: w.engine === "device", sindex: w.sindex_entries > 0
    };
  }

  function setCheck(path, checked, disabled) {
    var el = document.querySelector('input[data-path="' + path + '"]');
    el.checked = checked;
    el.disabled = disabled;
  }

  function refreshDependents() {
    var f = netFlags();
    setCheck("network.rack_aware", f.rack, req.network.azs <= 1);
    setCheck("network.active_rack", f.active, !f.rack);
    setCheck("network.local_read", f.local, !f.rack);
    document.querySelectorAll("[data-when]").forEach(function (el) { el.hidden = !f[el.getAttribute("data-when")]; });
    bindRates(f.az ? "rates" : "region_rates");
    $("whatif").hidden = sameHypotheses();
  }

  // bindRates points the rate inputs at the rates of the current mode: between AZs, or between
  // regions for XDR.
  function bindRates(map) {
    $("rateUnit").textContent = map === "rates" ? "$/GB cross-AZ" : "$/GB cross-region";
    document.querySelectorAll("#netTable input").forEach(function (el) {
      var path = "network." + map + "." + el.getAttribute("data-provider");
      if (el.getAttribute("data-path") === path) return;
      el.setAttribute("data-path", path);
      el.value = shown(el, getPath(req, path));
    });
  }

  function sameHypotheses() {
    var a = req.hypotheses, b = defaults.hypotheses;
    return Object.keys(b).every(function (k) {
      return typeof b[k] === "number" ? Math.abs(a[k] - b[k]) < 1e-9 : a[k] === b[k];
    });
  }

  // The form's bounds apply as the server's would: an out-of-range value is clamped when stored.
  function clampTo(el, v) {
    if (el.min !== "" && v < +el.min) v = +el.min;
    if (el.max !== "" && v > +el.max) v = +el.max;
    return v;
  }

  document.addEventListener("input", function (e) {
    var el = e.target, path = el.getAttribute && el.getAttribute("data-path");
    if (!path || el.type === "checkbox") return;
    var v = parseFloat(el.value);
    if (isNaN(v)) return; // keep the last valid value while the field is being edited
    setPath(req, path, clampTo(el, v) / scaleOf(el));
    changed();
  });

  // Once a field is left, it shows the value in use: the clamped one, or the kept one when the
  // field was emptied.
  document.addEventListener("change", function (e) {
    var el = e.target, path = el.getAttribute && el.getAttribute("data-path");
    if (!path) return;
    if (el.type === "checkbox") {
      setPath(req, path, el.checked);
      changed();
      return;
    }
    el.value = shown(el, getPath(req, path));
  });

  document.addEventListener("click", function (e) {
    var b = e.target.closest(".seg button");
    if (!b) return;
    var seg = b.parentNode, v = b.getAttribute("data-v");
    setPath(req, seg.getAttribute("data-path"), seg.getAttribute("data-type") === "int" ? parseInt(v, 10) : v);
    markSeg(seg, v);
    changed();
  });

  function changed() {
    refreshDependents();
    polls = 0;
    schedule(150);
  }

  // ---- Server ----

  function schedule(delay) {
    clearTimeout(timer);
    clearTimeout(poll);
    timer = setTimeout(send, delay);
  }

  function post(url, body) {
    return fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
      .then(function (r) {
        if (!r.ok) return r.text().then(function (t) { throw new Error(t || "HTTP " + r.status); });
        return r.status === 204 ? null : r.json();
      });
  }

  function send() {
    var id = ++seq, body = clone(req);
    body.rows = rows;
    body.auto = auto;
    post("/api/plan", body).then(function (resp) {
      if (id !== seq) return; // a newer request is on its way
      $("failure").hidden = true;
      last = resp;
      render(resp);
      if (resp.pending && polls < 20) {
        polls++;
        poll = setTimeout(send, 1200); // live prices are being looked up
      }
    }).catch(function (err) {
      if (id !== seq) return;
      showFailure(err);
    });
  }

  // ---- Rendering ----

  function render(r) {
    renderSizing(r);
    renderPlacement(r);
    renderNetwork(r);
    renderRows(r);
    // The inputs the server moved into their bounds, with what it used instead.
    $("clamps").innerHTML = (r.clamps || []).map(function (c) { return '<div class="notice warn small">' + esc(c.note) + "</div>"; }).join("");
    $("notes").innerHTML = r.notes.map(function (n) { return '<div class="notice warn small">' + esc(n) + "</div>"; }).join("");
    if (!addNodesTouched && r.generic.nodes) $("addNodes").value = r.generic.nodes;
  }

  // over tells whether a load passes a limit by more than the error of the arithmetic, as the fit
  // grades it (sizing.exceeds).
  function over(load, limit) { return load * (1 - tolerance) > limit; }

  function gauge(util, ceiling, load) {
    var cls = over(load, 1) ? "crit" : over(load, tight) ? "warn" : "";
    return '<div class="gauge ' + cls + '"><div style="width:' + Math.min(100, util * 100).toFixed(1) + '%"></div>' +
      '<span style="left:' + (ceiling * 100).toFixed(1) + '%" title="Ceiling ' + pct(ceiling) + '"></span></div>';
  }

  var PARTS = {
    storage: [["data", "records"], ["flash_index", "primary index on flash"], ["flash_sindex", "secondary indexes on flash"]],
    ram: [["index", "primary index"], ["sprigs", "sprigs of the flash index"], ["sindex", "secondary indexes"], ["set_index", "set indexes"], ["data_ram", "records"]],
    iops: [["read_iops", "record reads"], ["write_iops", "record writes with defrag"], ["index_iops", "flash index blocks"]]
  };

  // partsText lists what a demand is made of, when it has more than one part.
  function partsText(key, d) {
    var parts = (PARTS[key] || []).filter(function (p) { return d[p[0]] > 0; });
    if (parts.length < 2) return "";
    return parts.map(function (p) { return p[1] + " " + amount(key, d[p[0]]); }).join(" · ");
  }

  function renderSizing(r) {
    var g = r.generic, h = r.hypotheses, w = r.workload, d = g.demand;
    $("nodes").textContent = g.nodes || "—";
    var b = $("binding");
    b.className = "badge " + (g.nodes ? "sev-info" : "sev-critical");
    b.textContent = g.nodes ? BINDINGS[g.binding] : "No fit";
    $("explain").textContent = g.explain;
    $("checks").innerHTML = g.checks.map(function (c) { return '<div class="notice warn small">' + esc(c) + "</div>"; }).join("");
    var titles = {
      storage: "Storage to place", ram: w.index_on === "flash" ? "Memory (index on flash)" : "Memory",
      cpu: "CPU: " + count(d.tps) + " tx/s at the horizon", iops: "Device IOPS"
    };
    $("kpis").innerHTML = RESOURCES.map(function (res) {
      var demand = d[res.key], ceiling = h[res.ceiling];
      if (!demand) {
        var why = res.key === "storage" && w.engine === "memory" ? "Nothing on device in memory mode" : res.key === "iops" ? "No device operation" : "No demand";
        return '<div class="kpi"><div class="l">' + titles[res.key] + '</div><div class="v">—</div><div class="n">' + why + "</div></div>";
      }
      var util = g.utilization[res.key] || 0, load = g.load[res.key] || 0, need = g.need[res.key];
      var parts = [partsText(res.key, d)];
      if (res.key === "storage" && d.flash_index > 0) parts.push("partition-tree-sprigs " + d.sprigs_per_partition);
      if (res.key === "iops" && d.blocks_per_read > 1) parts.push(d.blocks_per_read + " blocks of 4 KiB per read");
      if (res.key === "iops" && w.engine === "device" && w.reads_per_sec > 0 && w.reads_found < 1) parts.push(pctTenth(w.reads_found) + " of the reads find a record (the others read no record block)");
      parts = parts.filter(Boolean).join(" · ");
      var needed = need === undefined ? "no node count works" : need + " node" + (need === 1 ? "" : "s") + " needed";
      return '<div class="kpi"><div class="l">' + titles[res.key] + '</div><div class="v">' + amount(res.key, demand) + "</div>" +
        (parts ? '<div class="n">' + parts + "</div>" : "") +
        '<div class="n">' + needed + " · " + amount(res.key, g.usable[res.key]) + " usable per node</div>" +
        (g.nodes ? gauge(util, ceiling, load) + '<div class="n">' + pct(util) + " used at peak · ceiling " + pct(ceiling) + (res.key === "ram" && d.data_ram > 0 ? " (records at the storage fill)" : "") + "</div>" : "") +
        "</div>";
    }).join("");
    $("ceilingsHead").textContent = "ceilings: " + RESOURCES.map(function (res) { return res.label + " " + pct(h[res.ceiling]); }).join(" · ");
  }

  function renderPlacement(r) {
    var n = r.generic.nodes, s = r.settings, groups = [];
    if (!n) {
      $("racks").innerHTML = '<div class="muted small">No node count works with this generic node.</div>';
      return;
    }
    if (s.mode === "region") {
      for (var k = 0; k < s.regions; k++) groups.push({ title: "Region " + (k + 1) + " · full copy", nodes: n, first: 1 });
    } else {
      var first = 1;
      for (var i = 0; i < s.azs; i++) {
        var size = Math.floor(n / s.azs) + (i < n % s.azs ? 1 : 0), az = String.fromCharCode(97 + i); // a…l for the 12 AZs the server accepts
        var title = s.rack_aware ? "Rack " + (i + 1) + " · AZ " + az : s.azs > 1 ? "AZ " + az + " · no rack ID" : "AZ a";
        groups.push({ title: title, nodes: size, first: first });
        first += size;
      }
    }
    $("racks").innerHTML = groups.map(function (gr) {
      var chips = "";
      if (gr.nodes > 48) {
        chips = '<span class="node">' + gr.nodes + " nodes</span>";
      } else {
        for (var j = 0; j < gr.nodes; j++) chips += '<span class="node">N' + String(gr.first + j).padStart(2, "0") + "</span>";
      }
      return '<div class="rack"><h4>' + esc(gr.title) + '</h4><div class="nodes">' + chips + "</div></div>";
    }).join("");
  }

  function buildNetTable() {
    $("netTable").querySelector("tbody").innerHTML = providers.map(function (p) {
      return '<tr data-provider="' + esc(p.key) + '"><td><span class="dot" style="background:' + esc(p.color) + '"></span> ' + esc(p.label) + "</td>" +
        '<td class="num" data-cell="gb"></td>' +
        '<td class="num"><input type="number" data-provider="' + esc(p.key) + '" data-path="network.rates.' + esc(p.key) + '" min="0" step="0.001" aria-label="' + esc(p.label) + ' rate, $ per GB"></td>' +
        '<td class="num" data-cell="cost"></td></tr>';
    }).join("");
  }

  function renderNetwork(r) {
    var t = r.network;
    $("netNote").textContent = t.note;
    $("netWarnings").innerHTML = t.warnings ? t.warnings.map(function (w) { return '<div class="notice warn">' + esc(w) + "</div>"; }).join("") : "";
    providers.forEach(function (p) {
      var tr = $("netTable").querySelector('tr[data-provider="' + p.key + '"]');
      tr.querySelector('[data-cell="gb"]').textContent = t.monthly_gb.toLocaleString("en-US", { maximumFractionDigits: 1 });
      tr.querySelector('[data-cell="cost"]').textContent = usd(t.costs[p.key]);
    });
  }

  // cheapest returns the configuration with the lowest monthly cost among those that fit comfortably.
  function cheapest(list) {
    var best = null;
    list.forEach(function (row) {
      [row, row.alternative && !hiddenAlts[row.id] ? row.alternative : null].forEach(function (c) {
        if (c && c.fit.status === "ok" && (!best || c.cost.total < best.cost.total)) best = c;
      });
    });
    return best;
  }

  function renderRows(r) {
    var active = document.activeElement;
    if (active && active.tagName === "INPUT" && $("rows").contains(active)) return; // after the edit
    var best = cheapest(r.rows), html = "";
    providers.forEach(function (p) {
      var list = r.rows.filter(function (row) { return row.provider === p.key; });
      if (!list.length) return;
      var low = cheapest(list);
      html += '<tr class="group"><td colspan="7"><span class="dot" style="background:' + esc(p.color) + '"></span><strong>' + esc(p.label) + "</strong> " +
        '<span class="muted small">' + list.length + " configuration" + (list.length > 1 ? "s" : "") +
        (low ? " · from " + usd(low.cost.total) + "/month" : "") + "</span></td></tr>";
      list.forEach(function (row) {
        html += rowHTML(row, p, false, best);
        if (row.alternative && !hiddenAlts[row.id]) html += rowHTML(row.alternative, p, true, best);
      });
    });
    $("rows").innerHTML = html || '<tr><td colspan="7" class="muted">No configuration: add one below.</td></tr>';
  }

  function priceSource(row, p) {
    if (row.price.source === "pending") return '<span class="nature"><span class="spinner"></span> looking up</span>';
    var badge = row.price.source === "live"
      ? '<span class="nature live" title="Public on-demand price in ' + esc(row.region.id) + '">live</span>'
      : '<span class="nature" title="Reference on-demand list price in US East, read on ' + esc(pricesAsOf) + '">reference</span>';
    if (livePrices[p.key]) badge += ' <button class="btn icon" type="button" data-refresh="' + esc(row.id) + '" title="Look the price up again">↻</button>';
    return badge;
  }

  function rowHTML(row, p, alt, best) {
    var i = row.instance, h = last.hypotheses, isBest = best && best.id === row.id;
    var tags = (row.auto ? ' <span class="badge sev-info" title="Cheapest configuration of this provider for the workload; follows the form">recommended</span>' : "") +
      (alt ? ' <span class="badge sev-info" title="Cheapest instance type that fits comfortably with as many nodes">comfortable alternative</span>' : "") +
      (isBest ? ' <span class="badge sev-ok" title="Lowest monthly cost among the configurations that fit comfortably">lowest cost</span>' : "");
    var usage = RESOURCES.map(function (res) {
      var u = row.fit.utilization[res.key];
      if (u === undefined) return '<span>' + res.label + '</span><span></span><span class="pct">—</span>';
      return "<span>" + res.label + "</span>" + gauge(u, h[res.ceiling], row.fit.load[res.key]) + '<span class="pct">' + pct(u) + "</span>";
    }).join("");
    var c = row.cost;
    return '<tr class="' + (alt ? "alt" : "") + (isBest ? " choice" : "") + '">' +
      "<td><strong>" + esc(i.name) + "</strong>" + tags +
      '<div class="small muted">' + i.vcpu + " vCPU · " + i.ram_gib + " GiB · " + i.drives + "×" + i.drive_gb + " GB NVMe · " + count(i.iops) + " IOPS" + (i.arch === "arm64" ? " · arm64" : "") + "</div>" +
      '<div class="small muted">' + esc(row.region.label) + "</div></td>" +
      '<td class="num"><input type="number" class="qty" min="1" step="1" value="' + row.nodes + '" data-row="' + esc(row.id) + '" aria-label="Nodes"></td>' +
      '<td><span class="badge ' + FIT[row.fit.status][0] + '">' + FIT[row.fit.status][1] + "</span>" +
      '<div class="checks">' + row.fit.checks.map(esc).join(" ") + "</div></td>" +
      '<td><div class="usage">' + usage + "</div></td>" +
      '<td class="num">$' + row.price.hourly.toFixed(3) + '<div class="small">' + priceSource(row, p) + "</div></td>" +
      '<td class="num"><div class="cost">' + usd(c.total) + "</div>" +
      '<div class="small muted">compute ' + usd(c.compute) + (c.clusters > 1 ? " (" + c.clusters + " clusters)" : "") + "</div>" +
      '<div class="small muted">network ' + usd(c.network) + "</div>" +
      '<div class="small muted">' + usd(c.total * 12) + "/year</div></td>" +
      '<td><button class="btn icon danger" type="button" data-remove="' + esc(row.id) + '" title="Remove">✕</button></td></tr>';
  }

  // ---- Configurations ----

  function findRow(id) {
    var found = null;
    (last ? last.rows : []).forEach(function (row) {
      if (row.id === id) found = row;
      if (row.alternative && row.alternative.id === id) found = row.alternative;
    });
    return found;
  }

  function parentOf(id) { return id.replace(/\/alt$/, ""); }

  function manualRow(row, nodes) {
    return { id: "r" + nextId++, provider: row.provider, region: row.region.id, instance: row.instance.name, nodes: nodes };
  }

  // An edited recommendation or alternative becomes a configuration of its own, kept as entered.
  function setNodes(id, value) {
    var row = findRow(id), nodes = Math.max(1, parseInt(value, 10) || 1);
    if (!row) return;
    if (row.auto) {
      auto = auto.filter(function (k) { return k !== row.provider; });
      rows.unshift(manualRow(row, nodes));
    } else if (id !== parentOf(id)) {
      hiddenAlts[parentOf(id)] = true;
      var at = rows.findIndex(function (r) { return r.id === parentOf(id); });
      rows.splice(at + 1, 0, manualRow(row, nodes));
    } else {
      rows.forEach(function (r) { if (r.id === id) r.nodes = nodes; });
    }
    schedule(0);
  }

  function removeRow(id) {
    var row = findRow(id);
    if (!row) return;
    if (row.auto) auto = auto.filter(function (k) { return k !== row.provider; });
    else if (id !== parentOf(id)) hiddenAlts[parentOf(id)] = true;
    else rows = rows.filter(function (r) { return r.id !== id; });
    schedule(0);
  }

  function refreshPrice(id) {
    var row = findRow(id);
    if (!row) return;
    post("/api/refresh", { provider: row.provider, region: row.region.id, instance: row.instance.name })
      .then(function () { polls = 0; schedule(0); })
      .catch(showFailure);
  }

  // A Nodes field leaves focus when its value is taken, so that the table shows the new answer.
  $("rows").addEventListener("change", function (e) {
    if (!e.target.classList.contains("qty")) return;
    e.target.blur();
    setNodes(e.target.getAttribute("data-row"), e.target.value);
  });
  $("rows").addEventListener("click", function (e) {
    var b = e.target.closest("button");
    if (!b) return;
    if (b.hasAttribute("data-remove")) removeRow(b.getAttribute("data-remove"));
    if (b.hasAttribute("data-refresh")) refreshPrice(b.getAttribute("data-refresh"));
  });
  // A qty edit skips rendering while the field has focus: catch up when it leaves.
  $("rows").addEventListener("focusout", function () { setTimeout(function () { if (last) renderRows(last); }, 0); });

  function options(list, value, label) {
    return list.map(function (x) { return '<option value="' + esc(value(x)) + '">' + esc(label(x)) + "</option>"; }).join("");
  }

  function addProvider() {
    return providers.find(function (x) { return x.key === $("addProvider").value; }) || providers[0];
  }

  function fillAddForm() {
    var p = addProvider();
    $("addRegion").innerHTML = options(p.regions, function (r) { return r.id; }, function (r) { return r.label; });
    $("addRegion").value = p.default_region;
    fillInstances();
  }

  // fillInstances lists the types the region offers, one group per family, families and sizes from
  // the cheapest; the selected type stays when the new region offers it too.
  function fillInstances() {
    var region = $("addRegion").value, kept = $("addInstance").value, families = [], byFamily = {};
    addProvider().instances.forEach(function (i) {
      if (i.regions.indexOf(region) < 0) return;
      if (!byFamily[i.family]) { byFamily[i.family] = []; families.push(i.family); }
      byFamily[i.family].push(i);
    });
    $("addInstance").innerHTML = families.map(function (f) {
      return '<optgroup label="' + esc(f) + '">' + options(byFamily[f], function (i) { return i.name; }, function (i) {
        return i.name + " — " + i.vcpu + " vCPU, " + i.ram_gib + " GiB, " + (i.drives * i.drive_gb / 1000).toFixed(1) + " TB" + (i.arch === "arm64" ? ", arm64" : "");
      }) + "</optgroup>";
    }).join("");
    $("addInstance").value = kept;
    if ($("addInstance").selectedIndex < 0) $("addInstance").selectedIndex = 0;
  }

  $("addProvider").addEventListener("change", fillAddForm);
  $("addRegion").addEventListener("change", fillInstances);
  $("addNodes").addEventListener("input", function () { addNodesTouched = true; });
  $("addRow").addEventListener("click", function () {
    rows.push({
      id: "r" + nextId++, provider: $("addProvider").value, region: $("addRegion").value,
      instance: $("addInstance").value, nodes: Math.max(1, parseInt($("addNodes").value, 10) || 1)
    });
    schedule(0);
  });

  $("reset").addEventListener("click", function () {
    req = clone(defaults);
    rows = [];
    auto = defaults.auto.slice();
    hiddenAlts = {};
    addNodesTouched = false;
    fillForm();
    changed();
  });
  $("resetHyp").addEventListener("click", function () {
    req.hypotheses = clone(defaults.hypotheses);
    fillForm();
    changed();
  });

  // applyQuery prefills the form from the URL: one parameter per field, named by its path
  // (workload.objects=5e8, hypotheses.growth=0.2, network.azs=3), as Wellness links here; "source"
  // names where the values come from and "back" links back to it when the server finds it local
  // (localBack, passed as the page's data-back).
  function applyQuery() {
    var params = new URLSearchParams(window.location.search), applied = 0;
    params.forEach(function (value, key) {
      var cur = getPath(req, key);
      if (cur === undefined || cur === null || typeof cur === "object") return;
      if (typeof cur === "number") {
        var v = parseFloat(value);
        if (isNaN(v)) return;
        var seg = document.querySelector('.seg[data-path="' + key + '"]');
        if (seg && seg.getAttribute("data-type") === "int") v = Math.round(v); // the API wants whole counts
        setPath(req, key, v);
      } else if (typeof cur === "boolean") {
        setPath(req, key, value === "1" || value === "true");
      } else {
        setPath(req, key, value);
      }
      applied++;
    });
    if (!applied) return;
    var source = params.get("source") || "the link", back = document.body.getAttribute("data-back"), note = $("prefilled");
    note.textContent = "Prefilled from " + source + ": " + applied + " value" + (applied > 1 ? "s" : "") + " of the form come from it. ";
    if (back) {
      var a = document.createElement("a");
      a.href = back;
      a.textContent = "Back to " + source;
      note.appendChild(a);
    } else if (params.get("back")) {
      note.appendChild(document.createTextNode("The page it links back to is not local: no link back."));
    }
    note.hidden = false;
  }

  // pricesNote says which prices this server reads live, and what applies otherwise.
  function pricesNote() {
    var live = providers.filter(function (p) { return livePrices[p.key]; }).map(function (p) { return p.label; });
    var reference = "the reference on-demand list price in US East read on " + pricesAsOf;
    if (!live.length) return "Prices: " + reference + " applies to every configuration (live lookups are off on this server).";
    return "Prices: " + live.join(" and ") + " are read live for the chosen region (AWS price list, Azure Retail Prices API); otherwise, and for the other providers, " + reference + " applies.";
  }

  // ---- Start ----

  fetch("/api/defaults").then(function (r) { return r.json(); }).then(function (d) {
    defaults = d.request;
    delete defaults.rows;
    providers = d.providers;
    pricesAsOf = d.prices_as_of;
    ratesAsOf = d.rates_as_of || "";
    livePrices = d.live_prices || {};
    tight = (d.thresholds && d.thresholds.tight) || tight;
    tolerance = (d.thresholds && d.thresholds.tolerance) || tolerance;
    req = clone(defaults);
    delete req.auto;
    auto = defaults.auto.slice();
    $("pricesNote").textContent = pricesNote();
    document.querySelectorAll(".ratesAsOf").forEach(function (el) { el.textContent = ratesAsOf; });
    $("addProvider").innerHTML = options(providers, function (p) { return p.key; }, function (p) { return p.label; });
    fillAddForm();
    buildNetTable();
    applyQuery();
    fillForm();
    send();
  }).catch(showFailure);
})();
