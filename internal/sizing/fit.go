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
	Status Status   `json:"status"`
	Checks []string `json:"checks"`
	// Load is the demand over the usable capacity (ceilings applied) of the nodes left after the
	// planned loss: above 1 the configuration does not hold.
	Load map[Constraint]float64 `json:"load"`
	// Utilization is the share of the capacity (Hypotheses.Capacity) used at peak with every node up.
	Utilization map[Constraint]float64 `json:"utilization"`
}

// Evaluate grades nodes × node against the demand.
func Evaluate(d, node Amounts, nodes int, h Hypotheses, l Layout) Fit {
	f := Fit{Status: OK, Load: map[Constraint]float64{}, Utilization: utilization(d, h.Capacity(node), nodes)}
	flag := func(s Status, format string, args ...any) {
		if s.rank() > f.Status.rank() {
			f.Status = s
		}
		f.Checks = append(f.Checks, fmt.Sprintf(format, args...))
	}
	if nodes < l.MinNodes() {
		flag(Short, "Topology: %d node(s) < RF+1 (%d min).", nodes, l.MinNodes())
	}
	if l.AZs > 1 && nodes < l.AZs {
		flag(Tight, "Fewer nodes than configured AZs (%d): no real multi-AZ spread.", l.AZs)
	} else if l.Racks > 1 && nodes%l.Racks != 0 {
		flag(Tight, "%d nodes do not split evenly over %d racks.", nodes, l.Racks)
	}
	serving := h.Serving(nodes, l)
	down := ""
	if serving < nodes {
		down = ", " + h.lossText(l)
	}
	usable := h.Usable(node)
	for _, c := range Resources {
		demand := d.Of(c)
		if demand <= 0 {
			continue
		}
		capacity := usable.Of(c) * float64(serving)
		if capacity <= 0 {
			flag(Short, "No usable %s on this node shape.", c.label())
			continue
		}
		load := demand / capacity
		f.Load[c] = load
		switch {
		case exceeds(load, 1):
			flag(Short, "Not enough %s: %s needed vs %s usable (%s%s).", c.label(), c.amount(demand), c.amount(capacity), h.ceilingText(c), down)
		case exceeds(load, TightLoad):
			flag(Tight, "%s tight: %s of the usable capacity.", capitalize(c.label()), units.Pct(load))
		}
	}
	if len(f.Checks) == 0 {
		f.Checks = append(f.Checks, "Comfortable margin on storage, RAM, CPU and IOPS.")
	}
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

// Explain tells in a sentence or two what drives a sizing.
func (s Sizing) Explain(w Workload, d Detail, node Amounts, h Hypotheses, l Layout) string {
	usable := h.Usable(node)
	scope := fmt.Sprintf("RF%d, %s growth included", w.RF, units.Pct(h.Growth))
	if s.Nodes == 0 {
		if usable.Of(s.Binding) <= 0 {
			return fmt.Sprintf("No node count works: the generic node has no usable %s once the ceilings and the OS reserve are applied.", s.Binding.label())
		}
		return fmt.Sprintf("No sensible node count works: the generic node offers too little %s for this workload.", s.Binding.label())
	}
	var b strings.Builder
	switch s.Binding {
	case ByStorage:
		fmt.Fprintf(&b, "Disk storage drives the sizing: %s to place (%s; %s), %s usable per node (%s).", units.SI(d.Storage), d.storageParts(), scope, units.SI(usable.Storage), h.ceilingText(ByStorage))
	case ByRAM:
		fmt.Fprintf(&b, "Memory drives the sizing: %s (%s; %s), %s usable per node (%s).", units.IEC(d.RAM), d.ramParts(), scope, units.IEC(usable.RAM), h.ceilingText(ByRAM))
		if d.DataRAM > 0 {
			fmt.Fprintf(&b, " That ceiling weighs the RAM fill of the indexes with the %s storage fill of the records in memory.", units.Pct(h.StorageFill))
		}
	case ByCPU:
		fmt.Fprintf(&b, "CPU drives the sizing: %s transactions/s at the horizon (%s growth) need %s at %s per vCPU, %s usable per node (%s).", units.Count(d.TPS), units.Pct(h.Growth), ByCPU.amount(d.CPU), units.Count(h.OpsPerVCPU), ByCPU.amount(usable.CPU), h.ceilingText(ByCPU))
	case ByIOPS:
		fmt.Fprintf(&b, "Device throughput drives the sizing: %s IOPS of 4 KiB (%s; %s), %s usable per node (%s).", units.Count(d.IOPS), d.iopsParts(w.FoundShare()), scope, units.Count(usable.IOPS), h.ceilingText(ByIOPS))
	default:
		fmt.Fprintf(&b, "Storage, RAM, CPU and IOPS fit on fewer nodes: the topology floor (RF+1 = %d) sets the minimum for fault tolerance.", l.MinNodes())
	}
	if h.Loss != NoLoss && s.Binding != ByTopology && s.Binding != ByRacks {
		fmt.Fprintf(&b, " The ceilings still hold with %s.", h.lossText(l))
	}
	if s.Rounded {
		fmt.Fprintf(&b, " Rounded up to %d nodes so that each of the %d racks holds the same number.", s.Nodes, l.Racks)
	}
	return b.String()
}

// storageParts lists what the storage demand is made of.
func (d Detail) storageParts() string {
	var parts []string
	if d.Data > 0 {
		parts = append(parts, "records "+units.SI(d.Data))
	}
	if d.FlashIndex > 0 {
		parts = append(parts, fmt.Sprintf("primary index on flash %s with %d sprigs per partition%s", units.SI(d.FlashIndex), d.SprigsPerPartition, d.sprigsNote()))
	}
	if d.FlashSindex > 0 {
		parts = append(parts, "secondary indexes on flash "+units.SI(d.FlashSindex))
	}
	return strings.Join(parts, ", ")
}

// sprigsNote warns when partition-tree-sprigs goes beyond what the reference lets one set alone.
func (d Detail) sprigsNote() string {
	if d.SprigsPerPartition > SupportSprigsPerPart {
		return fmt.Sprintf(" (above %s: ask Aerospike Support)", units.Count(SupportSprigsPerPart))
	}
	return ""
}

// ramParts lists what the memory demand is made of.
func (d Detail) ramParts() string {
	var parts []string
	add := func(label string, v float64) {
		if v > 0 {
			parts = append(parts, label+" "+units.IEC(v))
		}
	}
	add("primary index", d.Index)
	add("sprigs of the flash index", d.Sprigs)
	add("secondary indexes", d.Sindex)
	add("set indexes", d.SetIndex)
	add("records in memory", d.DataRAM)
	return strings.Join(parts, ", ")
}

// iopsParts lists what the device load is made of; found is the share of the reads that find a
// record, the only ones that read record blocks.
func (d Detail) iopsParts(found float64) string {
	var parts []string
	add := func(label string, v float64) {
		if v > 0 {
			parts = append(parts, label+" "+units.Count(v))
		}
	}
	reads := "record reads"
	if d.BlocksPerRead > 1 {
		reads = fmt.Sprintf("record reads of %.0f blocks", d.BlocksPerRead)
	}
	if found < 1 {
		reads += fmt.Sprintf(" for the %s of the reads that find a record", units.PctTenth(found))
	}
	add(reads, d.ReadIOPS)
	add("record writes in 4 KiB blocks with defrag", d.WriteIOPS)
	add("flash index blocks", d.IndexIOPS)
	return strings.Join(parts, ", ")
}

func (h Hypotheses) lossText(l Layout) string {
	if h.Loss == RackLoss && l.Racks > 1 {
		return "one rack down"
	}
	return "one node down"
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

func (h Hypotheses) ceilingText(c Constraint) string {
	switch c {
	case ByStorage:
		return units.Pct(h.StorageFill) + " fill"
	case ByRAM:
		return fmt.Sprintf("%s of RAM after the OS reserve of %.0f GiB or %s", units.Pct(h.RAMFill), h.OSReserveGiB, units.Pct(h.OSReservePct))
	case ByCPU:
		return units.Pct(h.CPUTarget) + " CPU"
	}
	return units.Pct(h.IOTarget) + " of the sustained IOPS"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
