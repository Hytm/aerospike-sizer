// Package plan answers the interface: from one request holding the whole form, the generic sizing,
// the network traffic and the configurations compared with their fit and cost.
package plan

import (
	"fmt"
	"slices"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/network"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

// Node is the generic node profile of the first estimate, independent of any provider.
type Node struct {
	VCPU      float64 `json:"vcpu"`
	RAMGiB    float64 `json:"ram_gib"`
	StorageTB float64 `json:"storage_tb"` // raw NVMe, decimal TB
	IOPS      float64 `json:"iops"`       // sustained planning IOPS, 4 KiB random reads
}

// Bounds of the generic node: the largest values an estimate accepts, so that every figure stays
// finite whatever the request says.
const (
	MaxNodeVCPU      = 1e5
	MaxNodeRAMGiB    = 1e6
	MaxNodeStorageTB = 1e5
	MaxNodeIOPS      = 1e9
)

// Amounts returns the raw resources of the node, within its bounds.
func (n Node) Amounts() sizing.Amounts {
	n, _ = n.Bounded()
	return sizing.Amounts{Storage: n.StorageTB * 1e12, RAM: n.RAMGiB * catalog.GiB, CPU: n.VCPU, IOPS: n.IOPS}
}

// Bounded returns the node within its bounds, with the values it moved (sizing.Clamp).
func (n Node) Bounded() (Node, []sizing.Clamp) {
	var b sizing.Bounds
	b.Float("node.vcpu", &n.VCPU, 0, MaxNodeVCPU)
	b.Float("node.ram_gib", &n.RAMGiB, 0, MaxNodeRAMGiB)
	b.Float("node.storage_tb", &n.StorageTB, 0, MaxNodeStorageTB)
	b.Float("node.iops", &n.IOPS, 0, MaxNodeIOPS)
	return n, b.Clamps
}

// RowRequest is a configuration the user added to the comparison.
type RowRequest struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Region   string `json:"region"`
	Instance string `json:"instance"`
	Nodes    int    `json:"nodes"`
}

// Request is the whole form.
type Request struct {
	Workload   sizing.Workload   `json:"workload"`
	Node       Node              `json:"node"`
	Hypotheses sizing.Hypotheses `json:"hypotheses"`
	Network    network.Settings  `json:"network"`
	Rows       []RowRequest      `json:"rows"`
	Auto       []string          `json:"auto"` // providers whose recommended configuration is listed
}

// DefaultRequest is the form as the interface opens. Every read finds its record: the share is a
// number rather than absent so that the form shows it and the URL can prefill it; a request that
// leaves it out keeps it.
func DefaultRequest() Request {
	auto := make([]string, 0, len(catalog.Providers))
	for _, p := range catalog.Providers {
		auto = append(auto, p.Key)
	}
	found := 1.0
	return Request{
		Workload:   sizing.Workload{ObjectSize: 1024, Objects: 500e6, ReadsPerSec: 200000, ReadsFound: &found, WritesPerSec: 40000, RF: 2, Engine: sizing.Device, IndexOn: sizing.InRAM, SindexOn: sizing.InRAM},
		Node:       Node{VCPU: 16, RAMGiB: 128, StorageTB: 3.75, IOPS: 200000},
		Hypotheses: sizing.DefaultHypotheses(),
		Network:    network.DefaultSettings(),
		Auto:       auto,
	}
}

// Generic is the estimate on the generic node.
type Generic struct {
	Demand sizing.Detail  `json:"demand"` // whole cluster, with its parts
	Node   sizing.Amounts `json:"node"`   // raw, per node
	Usable sizing.Amounts `json:"usable"` // per node, at the ceilings
	sizing.Sizing
	// Load is the demand over the usable capacity of the nodes left after the planned loss.
	Load map[sizing.Constraint]float64 `json:"load"`
	// Checks are the fit's remarks on the node count when it is not comfortable: a topology rule
	// the count misses, a tight resource.
	Checks  []string `json:"checks"`
	Explain string   `json:"explain"`
}

// Cost is the monthly cost of a configuration, list prices.
type Cost struct {
	Compute  float64 `json:"compute"` // every cluster
	Network  float64 `json:"network"`
	Total    float64 `json:"total"`
	Clusters int     `json:"clusters"` // one per region with XDR
}

// Row is a configuration of the comparison.
type Row struct {
	ID       string           `json:"id"`
	Auto     bool             `json:"auto"` // the provider's recommended configuration
	Provider string           `json:"provider"`
	Region   catalog.Region   `json:"region"`
	Instance catalog.Instance `json:"instance"`
	Nodes    int              `json:"nodes"`
	Fit      sizing.Fit       `json:"fit"`
	Price    pricing.Quote    `json:"price"`
	Cost     Cost             `json:"cost"`
	// Alternative is the cheapest instance type that fits comfortably with as many nodes, offered
	// when this configuration is tight or insufficient and another shape solves it.
	Alternative *Row `json:"alternative,omitempty"`
}

// Response is what the interface shows.
type Response struct {
	Generic    Generic           `json:"generic"`
	Layout     sizing.Layout     `json:"layout"`
	Workload   sizing.Workload   `json:"workload"`   // as normalized
	Hypotheses sizing.Hypotheses `json:"hypotheses"` // as normalized; ram_fill is the ceiling the demand calls for (Hypotheses.ForDemand)
	Settings   network.Settings  `json:"settings"`   // as normalized: options their prerequisites rule out are off
	Network    network.Traffic   `json:"network"`
	Rows       []Row             `json:"rows"`
	Pending    bool              `json:"pending"` // live prices are being looked up: ask again shortly
	Notes      []string          `json:"notes"`
	// Clamps are the inputs moved into their bounds, each with a note that says what was used and,
	// for the bounds of the model, what that changes: the workload, the generic node, the
	// hypotheses and the network settings, in that order; empty when nothing was bounded.
	Clamps []ClampNote `json:"clamps"`
}

// ClampNote is an input the estimate moved into its bounds, with what the interface says of it.
type ClampNote struct {
	sizing.Clamp
	Note string `json:"note"`
}

// Quoter prices one node; *pricing.Book implements it.
type Quoter interface {
	Quote(provider, region string, inst catalog.Instance) pricing.Quote
}

// computation carries what every row is evaluated against.
type computation struct {
	demand  sizing.Amounts
	hyp     sizing.Hypotheses
	layout  sizing.Layout
	traffic network.Traffic
	quoter  Quoter
}

// Compute answers a request.
func Compute(req Request, q Quoter) Response {
	w, wc := req.Workload.Bounded()
	n, nc := req.Node.Bounded()
	h, hc := req.Hypotheses.Bounded()
	s, sc := req.Network.Bounded()
	node := n.Amounts()
	clamps := clampNotes(slices.Concat(wc, nc, hc, sc), h)
	detail := sizing.Detailed(w, h)
	h = h.ForDemand(detail) // the RAM ceiling of a memory namespace follows its records
	c := computation{demand: detail.Amounts, hyp: h, layout: s.Layout(w.RF), traffic: network.Estimate(s, w.Grown(h)), quoter: q}

	g := sizing.Size(c.demand, node, h, c.layout)
	generic := Generic{Demand: detail, Node: node, Usable: h.Usable(node), Sizing: g, Load: map[sizing.Constraint]float64{}, Checks: []string{}, Explain: g.Explain(w, detail, node, h, c.layout)}
	if g.Nodes > 0 {
		fit := sizing.Evaluate(c.demand, node, g.Nodes, h, c.layout)
		generic.Load = fit.Load
		if fit.Status != sizing.OK {
			generic.Checks = fit.Checks
		}
	}
	resp := Response{
		Generic:    generic,
		Layout:     c.layout,
		Workload:   w,
		Hypotheses: h,
		Settings:   s,
		Network:    c.traffic,
		Rows:       []Row{},
		Notes:      []string{},
		Clamps:     clamps,
	}
	resp.Notes = append(resp.Notes, indexNotes(req.Workload, w)...)
	if h.Loss == sizing.RackLoss && c.layout.Racks == 1 {
		resp.Notes = append(resp.Notes, "The ceilings are kept after the loss of one node: losing a whole rack can only be planned for with rack-aware AZs in a single region.")
	}
	for i := range catalog.Providers {
		p := &catalog.Providers[i]
		if !slices.Contains(req.Auto, p.Key) {
			continue
		}
		o, ok := sizing.Recommend(p.Instances, c.demand, h, c.layout)
		if !ok {
			resp.Notes = append(resp.Notes, fmt.Sprintf("%s: no instance type of the catalog suits this workload.", p.Label))
			continue
		}
		region, _ := p.Region(p.DefaultRegion)
		resp.Rows = append(resp.Rows, c.row("auto-"+p.Key, true, p, region, o.Instance, o.Nodes))
	}
	for _, r := range req.Rows {
		p, ok := catalog.Get(r.Provider)
		if !ok {
			resp.Notes = append(resp.Notes, fmt.Sprintf("Unknown provider %q ignored.", r.Provider))
			continue
		}
		region, okRegion := p.Region(r.Region)
		inst, okInst := p.Instance(r.Instance)
		if !okRegion || !okInst {
			resp.Notes = append(resp.Notes, fmt.Sprintf("%s: unknown region %q or instance type %q ignored.", p.Label, r.Region, r.Instance))
			continue
		}
		resp.Rows = append(resp.Rows, c.row(r.ID, false, p, region, inst, min(max(r.Nodes, 1), sizing.MaxNodes)))
	}
	for _, r := range resp.Rows {
		resp.Pending = resp.Pending || r.Price.Source == pricing.Pending ||
			(r.Alternative != nil && r.Alternative.Price.Source == pricing.Pending)
	}
	return resp
}

// indexNotes say how the primary index of a request is planned when it cannot be planned where it
// was asked: on flash with the memory engine, which index-type flash does not work with, Bounded
// plans it in RAM. That is a rewrite of a placement, not a bound on a number: it is a note, not a
// clamp (T18).
func indexNotes(asked, w sizing.Workload) []string {
	if asked.IndexOn == sizing.OnFlash && w.IndexOn == sizing.InRAM {
		return []string{"The primary index is planned in RAM: index-type flash does not work with the memory engine (configuration reference, 7.0)."}
	}
	return nil
}

// row evaluates a configuration, with a comfortable alternative when it does not fit well. A
// topology issue (too few nodes, uneven racks) is the node count's: no other shape solves it.
func (c computation) row(id string, auto bool, p *catalog.Provider, region catalog.Region, inst catalog.Instance, nodes int) Row {
	r := c.evaluate(id, auto, p, region, inst, nodes)
	if r.Fit.Status == sizing.OK {
		return r
	}
	if alt, ok := sizing.Comfortable(p.Instances, c.demand, c.hyp, c.layout, nodes); ok && alt.Name != inst.Name {
		a := c.evaluate(id+"/alt", false, p, region, alt, nodes)
		r.Alternative = &a
	}
	return r
}

func (c computation) evaluate(id string, auto bool, p *catalog.Provider, region catalog.Region, inst catalog.Instance, nodes int) Row {
	price := c.quoter.Quote(p.Key, region.ID, inst)
	cost := Cost{Compute: price.Hourly * float64(nodes) * catalog.HoursPerMonth * float64(c.traffic.Clusters), Network: c.traffic.Costs[p.Key], Clusters: c.traffic.Clusters}
	cost.Total = cost.Compute + cost.Network
	return Row{
		ID: id, Auto: auto, Provider: p.Key, Region: region, Instance: inst, Nodes: nodes,
		Fit:   sizing.Evaluate(c.demand, sizing.OfInstance(inst), nodes, c.hyp, c.layout),
		Price: price, Cost: cost,
	}
}
