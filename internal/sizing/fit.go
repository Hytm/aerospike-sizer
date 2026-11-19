package sizing

import (
	"fmt"
	"strings"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/units"
)

// Status grades how a configuration fits the demand.
type Status string

// Statuses, from the best.
const (
	OK    Status = "ok"
	Tight Status = "tight"
	Short Status = "insufficient"
)

func (s Status) rank() int { return map[Status]int{OK: 0, Tight: 1, Short: 2}[s] }

// TightLoad is the load, as a share of the usable capacity, above which a configuration is tight;
// a comfortable one keeps every load under it.
const TightLoad = 0.85

// Fit tells how a number of nodes of one shape holds the demand.
type Fit struct {
	Status Status `json:"status"`
	// Checks are the remarks in English; Messages are the same remarks as codes and values, one
	// for one (T22).
	Checks   []string  `json:"checks"`
	Messages []Message `json:"messages"`
	// Load is the demand over the usable capacity (ceilings applied) of the nodes left after the
	// planned loss: above 1 the configuration does not hold.
	Load map[Constraint]float64 `json:"load"`
	// Utilization is the share of the capacity (Hypotheses.Capacity) used at peak with every node up.
	Utilization map[Constraint]float64 `json:"utilization"`
}

// Evaluate grades nodes × node against the demand.
func Evaluate(d, node Amounts, nodes int, h Hypotheses, l Layout) Fit {
	f := Fit{Status: OK, Load: map[Constraint]float64{}, Utilization: utilization(d, h.Capacity(node), nodes)}
	flag := func(s Status, m Message) {
		if s.rank() > f.Status.rank() {
			f.Status = s
		}
		f.Messages = append(f.Messages, m)
	}
	if nodes < l.MinNodes() {
		flag(Short, message("check.topology", Args{"nodes": nodes, "min_nodes": l.MinNodes()}))
	}
	if l.AZs > 1 && nodes < l.AZs {
		flag(Tight, message("check.fewer_azs", Args{"azs": l.AZs}))
	} else if l.Racks > 1 && nodes%l.Racks != 0 {
		flag(Tight, message("check.uneven_racks", Args{"nodes": nodes, "racks": l.Racks}))
	}
	serving := h.Serving(nodes, l)
	usable := h.Usable(node)
	for _, c := range Resources {
		demand := d.Of(c)
		if demand <= 0 {
			continue
		}
		capacity := usable.Of(c) * float64(serving)
		if capacity <= 0 {
			flag(Short, message("check.no_usable", Args{"resource": c}))
			continue
		}
		load := demand / capacity
		f.Load[c] = load
		switch {
		case exceeds(load, 1):
			limits := []Message{h.ceiling(c)}
			if serving < nodes {
				limits = append(limits, h.loss(l))
			}
			flag(Short, message("check.short", Args{"resource": c, "needed": demand, "usable": capacity}, limits...))
		case exceeds(load, TightLoad):
			flag(Tight, message("check.tight", Args{"resource": c, "load": load}))
		}
	}
	if len(f.Messages) == 0 {
		f.Messages = append(f.Messages, message("check.comfortable", nil))
	}
	f.Checks = Texts(f.Messages)
	return f
}

// MaxLoad returns the highest load of the fit.
func (f Fit) MaxLoad() float64 {
	m := 0.0
	for _, v := range f.Load {
		m = max(m, v)
	}
	return m
}

// Option is a configuration of one instance type.
type Option struct {
	Instance catalog.Instance `json:"instance"`
	Nodes    int              `json:"nodes"`
}

// nodePenaltyPer is the node count that doubles the price of a configuration in the
// recommendation score (+2 % per node): dozens of small nodes must not win over a sensible
// cluster (the aerospike-wellness penalty).
const nodePenaltyPer = 50

// Recommend returns the recommended configuration among the instance types given, a provider's in
// a region (decision T21, the rule aerospike-wellness applies too): the comfortable count of each
// type (every load under TightLoad, the topology rules met), or, when no type is comfortable, the
// count that holds; then the lowest score, the reference hourly rate of the basis × nodes × (1 +
// nodes ÷ 50), so that dozens of small nodes do not win over a sensible cluster; on a tie, fewer
// nodes, then the name, whatever the order of the types. ok is false when no instance type suits the
// demand.
func Recommend(insts []catalog.Instance, d Amounts, h Hypotheses, l Layout, b catalog.Basis) (Option, bool) {
	var comfortable, holding candidate
	for _, inst := range insts {
		s := Size(d, OfInstance(inst), h, l)
		if s.Nodes == 0 {
			continue
		}
		holding.consider(inst, s.Nodes, b)
		if n, ok := comfortableNodes(d, OfInstance(inst), h, l); ok {
			comfortable.consider(inst, n, b)
		}
	}
	if comfortable.found {
		return comfortable.Option, true
	}
	return holding.Option, holding.found
}

type candidate struct {
	Option
	score float64
	found bool
}

// score is what the recommendation minimizes: the hourly rate of a configuration with a penalty on
// large node counts (+2 % per node). It is computed from the hourly rate, as aerospike-wellness
// does, so that both tools break the same float ties.
func score(rate float64, nodes int) float64 {
	return rate * float64(nodes) * (1 + float64(nodes)/nodePenaltyPer)
}

func (c *candidate) consider(inst catalog.Instance, nodes int, b catalog.Basis) {
	s := score(inst.Rate(b), nodes)
	if !c.found || s < c.score ||
		s == c.score && (nodes < c.Nodes || nodes == c.Nodes && inst.Name < c.Instance.Name) {
		*c = candidate{Option: Option{Instance: inst, Nodes: nodes}, score: s, found: true}
	}
}

// comfortableNodes returns the fewest nodes of a shape that fit comfortably: the count the demand
// divided by the tight threshold needs, one per AZ at least, confirmed by Evaluate.
func comfortableNodes(d, node Amounts, h Hypotheses, l Layout) (int, bool) {
	s := Size(d.Scaled(TightLoad), node, h, l)
	if s.Nodes == 0 {
		return 0, false
	}
	n := max(s.Nodes, l.AZs)
	// The rounding of the loads may leave the count one rack short.
	for range 2 {
		if n > MaxNodes {
			return 0, false
		}
		if Evaluate(d, node, n, h, l).Status == OK {
			return n, true
		}
		n += max(l.Racks, 1)
	}
	return 0, false
}

// Comfortable returns the cheapest of the instance types given, a provider's in a region, that fits
// comfortably with the given node count (every load under TightLoad, the topology rules met): the
// lowest reference rate of the basis, on a tie the name, whatever the order of the types.
func Comfortable(insts []catalog.Instance, d Amounts, h Hypotheses, l Layout, nodes int, b catalog.Basis) (catalog.Instance, bool) {
	var best catalog.Instance
	found := false
	for _, inst := range insts {
		if Evaluate(d, OfInstance(inst), nodes, h, l).Status != OK {
			continue
		}
		if r := inst.Rate(b); !found || r < best.Rate(b) || r == best.Rate(b) && inst.Name < best.Name {
			best, found = inst, true
		}
	}
	return best, found
}

// Explain tells in a sentence or two what drives a sizing: the English of Explained.
func (s Sizing) Explain(w Workload, d Detail, node Amounts, h Hypotheses, l Layout) string {
	return strings.Join(Texts(s.Explained(w, d, node, h, l)), " ")
}

// Explained returns the sentences that tell what drives a sizing, as messages (T22).
func (s Sizing) Explained(w Workload, d Detail, node Amounts, h Hypotheses, l Layout) []Message {
	usable := h.Usable(node)
	if s.Nodes == 0 {
		if usable.Of(s.Binding) <= 0 {
			return []Message{message("explain.unusable", Args{"resource": s.Binding})}
		}
		return []Message{message("explain.too_little", Args{"resource": s.Binding})}
	}
	// sized quotes a resource that drives the sizing: the demand with its scope, the usable amount
	// per node and its ceiling.
	sized := func(demand, perNode, fill float64) Args {
		return Args{"resource": s.Binding, "demand": demand, "usable": perNode, "fill": fill, "rf": w.RF, "growth": h.Growth}
	}
	var out []Message
	switch s.Binding {
	case ByStorage:
		out = append(out, message("explain.storage", sized(d.Storage, usable.Storage, h.StorageFill), d.storageParts()...))
	case ByRAM:
		a := sized(d.RAM, usable.RAM, h.RAMFill)
		a["os_reserve_gib"], a["os_reserve_pct"] = h.OSReserveGiB, h.OSReservePct
		out = append(out, message("explain.ram", a, d.ramParts()...))
		if d.DataRAM > 0 {
			out = append(out, message("explain.records_in_memory", Args{"fill": h.StorageFill}))
		}
	case ByCPU:
		out = append(out, message("explain.cpu", Args{"resource": ByCPU, "tps": d.TPS, "growth": h.Growth, "demand": d.CPU, "ops_per_vcpu": h.OpsPerVCPU, "usable": usable.CPU, "fill": h.CPUTarget}))
	case ByIOPS:
		out = append(out, message("explain.iops", sized(d.IOPS, usable.IOPS, h.IOTarget), d.iopsParts(w.FoundShare())...))
	default:
		out = append(out, message("explain.floor", Args{"min_nodes": l.MinNodes()}))
	}
	if h.Loss != NoLoss && s.Binding != ByTopology && s.Binding != ByRacks {
		out = append(out, message("explain.loss", nil, h.loss(l)))
	}
	if s.Rounded {
		out = append(out, message("explain.rounded", Args{"nodes": s.Nodes, "racks": l.Racks}))
	}
	return out
}

// storageParts lists what the storage demand is made of.
func (d Detail) storageParts() []Message {
	var parts []Message
	if d.Data > 0 {
		parts = append(parts, message("part.records", Args{"bytes": d.Data}))
	}
	if d.FlashIndex > 0 {
		a := Args{"bytes": d.FlashIndex, "sprigs": d.SprigsPerPartition}
		code := "part.flash_index"
		if d.SprigsPerPartition > SupportSprigsPerPart {
			// Beyond what the reference lets one set alone.
			code, a["support_sprigs"] = "part.flash_index_support", SupportSprigsPerPart
		}
		parts = append(parts, message(code, a))
	}
	if d.FlashSindex > 0 {
		parts = append(parts, message("part.flash_sindex", Args{"bytes": d.FlashSindex}))
	}
	return parts
}

// ramParts lists what the memory demand is made of.
func (d Detail) ramParts() []Message {
	var parts []Message
	add := func(code string, v float64) {
		if v > 0 {
			parts = append(parts, message(code, Args{"bytes": v}))
		}
	}
	add("part.index", d.Index)
	add("part.sprigs", d.Sprigs)
	add("part.sindex", d.Sindex)
	add("part.set_index", d.SetIndex)
	add("part.records_in_memory", d.DataRAM)
	return parts
}

// iopsParts lists what the device load is made of; found is the share of the reads that find a
// record, the only ones that read record blocks.
func (d Detail) iopsParts(found float64) []Message {
	var parts []Message
	if d.ReadIOPS > 0 {
		a := Args{"count": d.ReadIOPS}
		code := "part.reads"
		if d.BlocksPerRead > 1 {
			code, a["blocks"] = "part.reads_blocks", d.BlocksPerRead
		}
		if found < 1 {
			code, a["found"] = code+"_found", found
		}
		parts = append(parts, message(code, a))
	}
	if d.WriteIOPS > 0 {
		parts = append(parts, message("part.writes", Args{"count": d.WriteIOPS}))
	}
	if d.IndexIOPS > 0 {
		parts = append(parts, message("part.flash_index_io", Args{"count": d.IndexIOPS}))
	}
	return parts
}

// loss names the loss the ceilings survive.
func (h Hypotheses) loss(l Layout) Message {
	if h.Loss == RackLoss && l.Racks > 1 {
		return message("loss.rack", nil)
	}
	return message("loss.node", nil)
}

func (c Constraint) label() string {
	if c == ByStorage {
		return string(c)
	}
	return strings.ToUpper(string(c))
}

func (c Constraint) amount(v float64) string {
	switch c {
	case ByStorage:
		return units.SI(v)
	case ByRAM:
		return units.IEC(v)
	case ByCPU:
		return fmt.Sprintf("%.1f vCPU", v)
	}
	return units.Count(v) + " IOPS"
}

// ceiling names the ceiling of a resource.
func (h Hypotheses) ceiling(c Constraint) Message {
	switch c {
	case ByStorage:
		return message("ceiling.storage", Args{"fill": h.StorageFill})
	case ByRAM:
		return message("ceiling.ram", Args{"fill": h.RAMFill, "os_reserve_gib": h.OSReserveGiB, "os_reserve_pct": h.OSReservePct})
	case ByCPU:
		return message("ceiling.cpu", Args{"fill": h.CPUTarget})
	}
	return message("ceiling.iops", Args{"fill": h.IOTarget})
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
