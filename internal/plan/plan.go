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
	IOPS      float64 `json:"iops"`       // sustained planning IOPS
}

// Amounts returns the raw resources of the node.
func (n Node) Amounts() sizing.Amounts {
	return sizing.Amounts{Storage: max(n.StorageTB, 0) * 1e12, RAM: max(n.RAMGiB, 0) * catalog.GiB, CPU: max(n.VCPU, 0), IOPS: max(n.IOPS, 0)}
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

// DefaultRequest is the form as the interface opens.
func DefaultRequest() Request {
	auto := make([]string, 0, len(catalog.Providers))
	for _, p := range catalog.Providers {
		auto = append(auto, p.Key)
	}
	return Request{
		Workload:   sizing.Workload{ObjectSize: 1024, Objects: 500e6, ReadsPerSec: 200000, WritesPerSec: 40000, RF: 2, Engine: sizing.Device, IndexOn: sizing.InRAM, SindexOn: sizing.InRAM},
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
	Load    map[sizing.Constraint]float64 `json:"load"`
	Explain string                        `json:"explain"`
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
	// Alternative is the smallest instance type that fits comfortably with as many nodes, offered
	// when this configuration is tight or insufficient and another shape solves it.
	Alternative *Row `json:"alternative,omitempty"`
}

// Response is what the interface shows.
type Response struct {
	Generic    Generic           `json:"generic"`
	Layout     sizing.Layout     `json:"layout"`
	Workload   sizing.Workload   `json:"workload"`   // as normalized
	Hypotheses sizing.Hypotheses `json:"hypotheses"` // as normalized
	Settings   network.Settings  `json:"settings"`   // as normalized: options their prerequisites rule out are off
	Network    network.Traffic   `json:"network"`
	Rows       []Row             `json:"rows"`
	Pending    bool              `json:"pending"` // live prices are being looked up: ask again shortly
	Notes      []string          `json:"notes"`
	Defaults   sizing.Hypotheses `json:"defaults"`
}

// Quoter prices one node; *pricing.Book implements it.
type Quoter interface {
	Quote(provider, region string, inst catalog.Instance) pricing.Quote
}

// hoursPerMonth turns hourly prices into monthly ones, as providers do.
const hoursPerMonth = 730

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
	w := req.Workload.Normalize()
	h := req.Hypotheses.Normalize()
	s := req.Network.Normalize()
	node := req.Node.Amounts()
	detail := sizing.Detailed(w, h)
	c := computation{demand: detail.Amounts, hyp: h, layout: s.Layout(w.RF), traffic: network.Estimate(s, w.Grown(h)), quoter: q}

	g := sizing.Size(c.demand, node, h, c.layout)
	load := map[sizing.Constraint]float64{}
	if g.Nodes > 0 {
		load = sizing.Evaluate(c.demand, node, g.Nodes, h, c.layout).Load
	}
	resp := Response{
		Generic:    Generic{Demand: detail, Node: node, Usable: h.Usable(node), Sizing: g, Load: load, Explain: g.Explain(w, detail, node, h, c.layout)},
		Layout:     c.layout,
		Workload:   w,
		Hypotheses: h,
		Settings:   s,
		Network:    c.traffic,
		Rows:       []Row{},
		Notes:      []string{},
		Defaults:   sizing.DefaultHypotheses(),
	}
	for i := range catalog.Providers {
		p := &catalog.Providers[i]
		if !slices.Contains(req.Auto, p.Key) {
			continue
		}
		o, ok := sizing.Recommend(p, c.demand, h, c.layout)
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

// row evaluates a configuration, with a comfortable alternative when it does not fit well. A
// topology issue (too few nodes, uneven racks) is the node count's: no other shape solves it.
func (c computation) row(id string, auto bool, p *catalog.Provider, region catalog.Region, inst catalog.Instance, nodes int) Row {
	r := c.evaluate(id, auto, p, region, inst, nodes)
	if r.Fit.Status == sizing.OK {
		return r
	}
	if alt, ok := sizing.Comfortable(p, c.demand, c.hyp, c.layout, nodes); ok && alt.Name != inst.Name {
		if a := c.evaluate(id+"/alt", false, p, region, alt, nodes); a.Fit.Status == sizing.OK {
			r.Alternative = &a
		}
	}
	return r
}

func (c computation) evaluate(id string, auto bool, p *catalog.Provider, region catalog.Region, inst catalog.Instance, nodes int) Row {
	price := c.quoter.Quote(p.Key, region.ID, inst)
	cost := Cost{Compute: price.Hourly * float64(nodes) * hoursPerMonth * float64(c.traffic.Clusters), Network: c.traffic.Costs[p.Key], Clusters: c.traffic.Clusters}
	cost.Total = cost.Compute + cost.Network
	return Row{
		ID: id, Auto: auto, Provider: p.Key, Region: region, Instance: inst, Nodes: nodes,
		Fit:   sizing.Evaluate(c.demand, sizing.OfInstance(inst), nodes, c.hyp, c.layout),
		Price: price, Cost: cost,
	}
}
