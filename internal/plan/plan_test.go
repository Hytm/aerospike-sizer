package plan

import (
	"math"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/network"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

// referenceQuoter prices every node at its reference price, live for AWS to tell sources apart.
type referenceQuoter struct{ pending bool }

func (q referenceQuoter) Quote(provider, _ string, inst catalog.Instance) pricing.Quote {
	switch {
	case q.pending:
		return pricing.Quote{Hourly: inst.PriceHour, Source: pricing.Pending}
	case provider == "aws":
		return pricing.Quote{Hourly: inst.PriceHour * 1.1, Source: pricing.Live}
	}
	return pricing.Quote{Hourly: inst.PriceHour, Source: pricing.Reference}
}

func TestDefaultRequest(t *testing.T) {
	resp := Compute(DefaultRequest(), referenceQuoter{})
	// 240,000 tx/s grow to 312,000: 20.8 vCPU at 60 % of 16 vCPU nodes, plus a spare, over 3 racks.
	if resp.Generic.Nodes != 6 || resp.Generic.Binding != sizing.ByCPU || resp.Generic.Explain == "" {
		t.Errorf("generic = %+v", resp.Generic)
	}
	if len(resp.Rows) != 3 || resp.Rows[0].ID != "auto-aws" || resp.Rows[1].Provider != "gcp" || resp.Rows[2].Provider != "azure" {
		t.Fatalf("rows = %+v", resp.Rows)
	}
	for _, r := range resp.Rows {
		if !r.Auto || r.Fit.Status == sizing.Short || r.Nodes%3 != 0 {
			t.Errorf("%s: auto %v, %s × %d, %s", r.ID, r.Auto, r.Instance.Name, r.Nodes, r.Fit.Status)
		}
		p, _ := catalog.Get(r.Provider)
		if r.Region.ID != p.DefaultRegion {
			t.Errorf("%s: region %s", r.ID, r.Region.ID)
		}
	}
	if resp.Pending || len(resp.Notes) != 0 {
		t.Errorf("pending %v, notes %q", resp.Pending, resp.Notes)
	}
}

func TestRowCostCountsEveryClusterAndTheNetwork(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Network.Mode = network.MultiRegion
	req.Network.Regions = 3
	req.Rows = []RowRequest{{ID: "r1", Provider: "gcp", Region: "europe-west9", Instance: "z3-highmem-16-highlssd", Nodes: 4}}
	resp := Compute(req, referenceQuoter{})
	if len(resp.Rows) != 1 {
		t.Fatalf("rows = %+v", resp.Rows)
	}
	r := resp.Rows[0]
	p, _ := catalog.Get("gcp")
	inst, _ := p.Instance("z3-highmem-16-highlssd")
	want := inst.PriceHour * 4 * 730 * 3
	if math.Abs(r.Cost.Compute-want) > 1e-6 || r.Cost.Clusters != 3 || r.Cost.Network != resp.Network.Costs["gcp"] || math.Abs(r.Cost.Total-(r.Cost.Compute+r.Cost.Network)) > 1e-6 {
		t.Errorf("cost = %+v, want compute %v", r.Cost, want)
	}
}

func TestNetworkTrafficGrowsWithTheThroughput(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Network.Mode = network.MultiRegion
	req.Network.Regions = 3
	resp := Compute(req, referenceQuoter{})
	// 40,000 writes/s × 1.3 growth × 1,024 B × (3 − 1) regions × (1 − 50 % compression).
	if want := 40000 * 1.3 * 1024 * 2 * 0.5; math.Abs(resp.Network.BytesPerSec["xdr"]-want) > 1e-6 {
		t.Errorf("xdr = %v B/s, want %v", resp.Network.BytesPerSec["xdr"], want)
	}
}

func TestTightRowGetsAComfortableAlternative(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Workload.ReadsPerSec = 50000 // 117,000 tx/s at the horizon: CPU at 81 % of the usable vCPUs, RAM is the matter
	// 3 × i4i.2xlarge, one down: 2 × 41.3 GiB of usable RAM for 77.5 GiB of index (94 %): tight.
	req.Rows = []RowRequest{{ID: "r1", Provider: "aws", Region: "eu-west-3", Instance: "i4i.2xlarge", Nodes: 3}}
	resp := Compute(req, referenceQuoter{})
	r := resp.Rows[0]
	if r.Fit.Status != sizing.Tight || r.Alternative == nil {
		t.Fatalf("row = %+v", r)
	}
	if r.Alternative.ID != "r1/alt" || r.Alternative.Instance.Name != "i4i.4xlarge" || r.Alternative.Nodes != 3 || r.Alternative.Fit.Status != sizing.OK {
		t.Errorf("alternative = %+v", r.Alternative)
	}
}

func TestAlternativesOnlyWhenAnotherShapeSolvesIt(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Workload.ReadsPerSec = 50000
	req.Rows = []RowRequest{
		{ID: "uneven", Provider: "aws", Region: "eu-west-3", Instance: "i4i.4xlarge", Nodes: 4}, // tight: 4 nodes over 3 racks
		{ID: "short", Provider: "aws", Region: "eu-west-3", Instance: "i4i.large", Nodes: 3},    // insufficient RAM
	}
	resp := Compute(req, referenceQuoter{})
	if r := resp.Rows[0]; r.Fit.Status != sizing.Tight || r.Alternative != nil {
		t.Errorf("uneven racks: %s, alternative %+v", r.Fit.Status, r.Alternative)
	}
	if r := resp.Rows[1]; r.Fit.Status != sizing.Short || r.Alternative == nil || r.Alternative.Fit.Status != sizing.OK {
		t.Errorf("insufficient RAM: %s, alternative %+v", r.Fit.Status, r.Alternative)
	}
}

func TestUnknownRowsAreIgnoredWithANote(t *testing.T) {
	req := DefaultRequest()
	req.Auto = []string{"aws"}
	req.Rows = []RowRequest{
		{ID: "a", Provider: "oracle", Region: "x", Instance: "y", Nodes: 3},
		{ID: "b", Provider: "aws", Region: "mars-1", Instance: "i4i.large", Nodes: 3},
		{ID: "c", Provider: "aws", Region: "eu-west-3", Instance: "i4i.large", Nodes: -4},
	}
	resp := Compute(req, referenceQuoter{pending: true})
	if len(resp.Rows) != 2 || resp.Rows[1].ID != "c" || resp.Rows[1].Nodes != 1 || len(resp.Notes) != 2 {
		t.Errorf("rows %+v, notes %q", resp.Rows, resp.Notes)
	}
	if !resp.Pending {
		t.Error("pending prices must be reported")
	}
}

func TestSettingsComeBackNormalized(t *testing.T) {
	req := DefaultRequest()
	req.Network.AZs = 1
	resp := Compute(req, referenceQuoter{})
	if resp.Settings.RackAware || resp.Layout.Racks != 1 || resp.Network.MonthlyGB != 0 {
		t.Errorf("settings %+v, layout %+v, traffic %v GB", resp.Settings, resp.Layout, resp.Network.MonthlyGB)
	}
}
