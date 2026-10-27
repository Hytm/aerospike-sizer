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
  var pricesAsOf = "";
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
  function amount(key, v) {
    return key === "storage" ? si(v) : key === "ram" ? iec(v) : key === "cpu" ? v.toFixed(1) + " vCPU" : count(v);
  }

  // ---- Form ↔ request ----

  function fields() { return document.querySelectorAll("[data-path]"); }

  function fillForm() {
    fields().forEach(function (el) {
      var v = getPath(req, el.getAttribute("data-path"));
      if (el.classList.contains("seg")) {
        markSeg(el, String(v));
      } else if (el.type === "checkbox") {
        el.checked = !!v;
      } else {
        el.value = +(v * parseFloat(el.getAttribute("data-scale") || "1")).toFixed(6);
      }
    });
    refreshDependents();
  }

  function markSeg(seg, v) {
    seg.querySelectorAll("button").forEach(function (b) { b.classList.toggle("on", b.getAttribute("data-v") === v); });
  }

  // netFlags tells which network options apply: an option shows unchecked and disabled while its
  // prerequisite is off, and the choice comes back with it.
  function netFlags() {
    var n = req.network, rack = n.azs > 1 && n.rack_aware, active = rack && n.active_rack;
    return {
      az: n.mode === "az", region: n.mode === "region", rack: rack, active: active, local: rack && n.local_read,
      activeRack: active, coLocated: active && n.client_placement === "single", compression: n.xdr_compression
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
    $("rateUnit").textContent = f.az ? "$/GB cross-AZ" : "$/GB cross-region";
    $("whatif").hidden = sameHypotheses();
  }

  function sameHypotheses() {
    var a = req.hypotheses, b = defaults.hypotheses;
    return Object.keys(b).every(function (k) {
      return typeof b[k] === "number" ? Math.abs(a[k] - b[k]) < 1e-9 : a[k] === b[k];
    });
  }

  document.addEventListener("input", function (e) {
    var el = e.target, path = el.getAttribute && el.getAttribute("data-path");
    if (!path || el.type === "checkbox") return;
    var v = parseFloat(el.value);
    if (isNaN(v)) return; // keep the last valid value while the field is being edited
    setPath(req, path, v / parseFloat(el.getAttribute("data-scale") || "1"));
    changed();
  });

  document.addEventListener("change", function (e) {
    var el = e.target, path = el.getAttribute && el.getAttribute("data-path");
    if (!path || el.type !== "checkbox") return;
    setPath(req, path, el.checked);
    changed();
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
      $("failure").textContent = "The sizer service did not answer: " + err.message;
      $("failure").hidden = false;
    });
  }

  // ---- Rendering ----

  function render(r) {
    renderSizing(r);
    renderPlacement(r);
    renderNetwork(r);
    renderRows(r);
    $("notes").innerHTML = r.notes.map(function (n) { return '<div class="notice warn small">' + esc(n) + "</div>"; }).join("");
    if (!addNodesTouched && r.generic.nodes) $("addNodes").value = r.generic.nodes;
  }

  function gauge(util, ceiling, load) {
    var cls = load > 1 ? "crit" : load > 0.85 ? "warn" : "";
    return '<div class="gauge ' + cls + '"><div style="width:' + Math.min(100, util * 100).toFixed(1) + '%"></div>' +
      '<span style="left:' + (ceiling * 100).toFixed(1) + '%" title="Ceiling ' + pct(ceiling) + '"></span></div>';
  }

  function renderSizing(r) {
    var g = r.generic, h = r.hypotheses, memory = req.workload.engine === "memory";
    $("nodes").textContent = g.nodes || "—";
    var b = $("binding");
    b.className = "badge " + (g.nodes ? "sev-info" : "sev-critical");
    b.textContent = g.nodes ? BINDINGS[g.binding] : "No fit";
    $("explain").textContent = g.explain;
    var titles = {
      storage: "Storage to place", ram: memory ? "Memory: data and index" : "Memory: primary index",
      cpu: "CPU: " + count(req.workload.reads_per_sec + req.workload.writes_per_sec) + " tx/s", iops: "Device IOPS"
    };
    var serving = g.nodes - (h.node_loss && g.nodes > 1 ? 1 : 0);
    $("kpis").innerHTML = RESOURCES.map(function (res) {
      var demand = g.demand[res.key], ceiling = h[res.ceiling];
      if (!demand) {
        return '<div class="kpi"><div class="l">' + titles[res.key] + '</div><div class="v">—</div><div class="n">' +
          (res.key === "storage" && memory ? "Not used in memory mode" : "No demand") + "</div></div>";
      }
      var util = g.utilization[res.key] || 0, load = g.nodes ? util * g.nodes / serving / ceiling : 0;
      return '<div class="kpi"><div class="l">' + titles[res.key] + '</div><div class="v">' + amount(res.key, demand) + "</div>" +
        '<div class="n">' + g.need[res.key] + " nodes needed · " + amount(res.key, g.usable[res.key]) + " usable per node</div>" +
        (g.nodes ? gauge(util, ceiling, load) + '<div class="n">' + pct(util) + " used at peak · ceiling " + pct(ceiling) + "</div>" : "") +
        "</div>";
    }).join("");
    $("ceilingsHead").textContent = "ceilings: " + RESOURCES.map(function (res) { return res.label + " " + pct(h[res.ceiling]); }).join(" · ");
  }

  function renderPlacement(r) {
    var n = r.generic.nodes, s = r.settings, groups = [], letters = "abcdef";
    if (!n) {
      $("racks").innerHTML = '<div class="muted small">No node count works with this generic node.</div>';
      return;
    }
    if (s.mode === "region") {
      for (var k = 0; k < s.regions; k++) groups.push({ title: "Region " + (k + 1) + " · full copy", nodes: n, first: 1 });
    } else {
      var first = 1;
      for (var i = 0; i < s.azs; i++) {
        var size = Math.floor(n / s.azs) + (i < n % s.azs ? 1 : 0);
        var title = s.rack_aware ? "Rack " + (i + 1) + " · AZ " + letters[i] : s.azs > 1 ? "AZ " + letters[i] + " · no rack ID" : "AZ a";
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
      return '<tr data-provider="' + p.key + '"><td><span class="dot" style="background:' + p.color + '"></span> ' + esc(p.label) + "</td>" +
        '<td class="num" data-cell="gb"></td>' +
        '<td class="num"><input type="number" data-path="network.rates.' + p.key + '" min="0" step="0.001"></td>' +
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
      html += '<tr class="group"><td colspan="7"><span class="dot" style="background:' + p.color + '"></span><strong>' + esc(p.label) + "</strong> " +
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
    if (p.live_prices) badge += ' <button class="btn icon" type="button" data-refresh="' + esc(row.id) + '" title="Look the price up again">↻</button>';
    return badge;
  }

  function rowHTML(row, p, alt, best) {
    var i = row.instance, h = last.hypotheses, isBest = best && best.id === row.id;
    var tags = (row.auto ? ' <span class="badge sev-info" title="Cheapest configuration of this provider for the workload; follows the form">recommended</span>' : "") +
      (alt ? ' <span class="badge sev-info" title="Smallest instance type that fits comfortably with as many nodes">comfortable alternative</span>' : "") +
      (isBest ? ' <span class="badge sev-ok" title="Lowest monthly cost among the configurations that fit comfortably">lowest cost</span>' : "");
    var usage = RESOURCES.map(function (res) {
      var u = row.fit.utilization[res.key];
      if (u === undefined) return '<span>' + res.label + '</span><span></span><span class="pct">—</span>';
      return "<span>" + res.label + "</span>" + gauge(u, h[res.ceiling], row.fit.load[res.key]) + '<span class="pct">' + pct(u) + "</span>";
    }).join("");
    var c = row.cost;
    return '<tr class="' + (alt ? "alt" : "") + (isBest ? " choice" : "") + '">' +
      "<td><strong>" + esc(i.name) + "</strong>" + tags +
      '<div class="small muted">' + i.vcpu + " vCPU · " + i.ram_gib + " GiB · " + i.drives + "×" + i.drive_gb + " GB NVMe · " + count(i.iops) + " IOPS</div>" +
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
      .then(function () { polls = 0; schedule(0); });
  }

  $("rows").addEventListener("change", function (e) {
    if (e.target.classList.contains("qty")) setNodes(e.target.getAttribute("data-row"), e.target.value);
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

  function fillAddForm() {
    var p = providers.find(function (x) { return x.key === $("addProvider").value; }) || providers[0];
    $("addRegion").innerHTML = options(p.regions, function (r) { return r.id; }, function (r) { return r.label; });
    $("addRegion").value = p.default_region;
    $("addInstance").innerHTML = options(p.instances, function (i) { return i.name; }, function (i) {
      return i.name + " — " + i.vcpu + " vCPU, " + i.ram_gib + " GiB, " + (i.drives * i.drive_gb / 1000).toFixed(1) + " TB";
    });
  }

  $("addProvider").addEventListener("change", fillAddForm);
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

  // ---- Start ----

  fetch("/api/defaults").then(function (r) { return r.json(); }).then(function (d) {
    defaults = d.request;
    delete defaults.rows;
    providers = d.providers;
    pricesAsOf = d.prices_as_of;
    req = clone(defaults);
    delete req.auto;
    auto = defaults.auto.slice();
    $("pricesNote").textContent = "Prices: AWS and Azure are read live for the chosen region (ec2.shop, Azure Retail Prices API); " +
      "otherwise, and always for Google Cloud, the reference list price in US East read on " + pricesAsOf + " applies.";
    $("addProvider").innerHTML = options(providers, function (p) { return p.key; }, function (p) { return p.label; });
    fillAddForm();
    buildNetTable();
    fillForm();
    send();
  }).catch(function (err) {
    $("failure").textContent = "The sizer service did not answer: " + err.message;
    $("failure").hidden = false;
  });
})();
