package plan

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/network"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

// referenceQuoter prices every node at its reference price, live for AWS (×1.1) to tell sources
// apart; pending makes every quote pending, pendingFor one instance type only.
type referenceQuoter struct {
	pending    bool
	pendingFor string
}

func (q referenceQuoter) Quote(provider, _ string, inst catalog.Instance) pricing.Quote {
	switch {
	case q.pending || inst.Name == q.pendingFor:
		return pricing.Quote{Hourly: inst.PriceHour, Source: pricing.Pending}
	case provider == "aws":
		return pricing.Quote{Hourly: inst.PriceHour * 1.1, Source: pricing.Live}
	}
	return pricing.Quote{Hourly: inst.PriceHour, Source: pricing.Reference}
}

func near(a, b, tolerance float64) bool { return math.Abs(a-b) <= tolerance }

func TestDefaultRequest(t *testing.T) {
	resp := Compute(DefaultRequest(), referenceQuoter{})
	// 240,000 tx/s grow to 312,000: 20.8 vCPU at 60 % of 16 vCPU nodes, plus a spare, over 3 racks.
	if resp.Generic.Nodes != 6 || resp.Generic.Binding != sizing.ByCPU || resp.Generic.Explain == "" || len(resp.Generic.Checks) != 0 {
		t.Errorf("generic = %+v", resp.Generic)
	}
	// The figures decisions T12 and T15 quote: 293,800 IOPS (33,800 of writes, no read cache) and 466,452 GB a month.
	d := resp.Generic.Demand
	if !near(d.IOPS, 293800, 1) || !near(d.WriteIOPS, 33800, 1) || !near(resp.Network.MonthlyGB, 466452.48, 0.01) {
		t.Errorf("IOPS %v (writes %v), %v GB/month", d.IOPS, d.WriteIOPS, resp.Network.MonthlyGB)
	}
	// Storage, RAM and IOPS fit on one node: with the spare, two.
	if resp.Generic.Need[sizing.ByStorage] != 2 || resp.Generic.Need[sizing.ByCPU] != 4 {
		t.Errorf("need = %v", resp.Generic.Need)
	}
	if len(resp.Rows) != 3 || resp.Rows[0].ID != "auto-aws" || resp.Rows[1].Provider != "gcp" || resp.Rows[2].Provider != "azure" {
		t.Fatalf("rows = %+v", resp.Rows)
	}
	for _, r := range resp.Rows {
		if !r.Auto || r.Fit.Status != sizing.OK || r.Nodes%3 != 0 || r.Alternative != nil {
			t.Errorf("%s: auto %v, %s × %d, %s", r.ID, r.Auto, r.Instance.Name, r.Nodes, r.Fit.Status)
		}
		p, _ := catalog.Get(r.Provider)
		if r.Region.ID != p.DefaultRegion {
			t.Errorf("%s: region %s", r.ID, r.Region.ID)
		}
	}
	// Azure does not bill cross-AZ traffic: its network cost is nil.
	if resp.Rows[2].Cost.Network != 0 || resp.Rows[0].Cost.Network <= 0 {
		t.Errorf("network costs: aws %v, azure %v", resp.Rows[0].Cost.Network, resp.Rows[2].Cost.Network)
	}
	if resp.Pending || len(resp.Notes) != 0 {
		t.Errorf("pending %v, notes %q", resp.Pending, resp.Notes)
	}
}

func TestRowCostUsesTheQuotedPrice(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Rows = []RowRequest{{ID: "r1", Provider: "aws", Region: "eu-west-3", Instance: "i4i.xlarge", Nodes: 3}}
	r := Compute(req, referenceQuoter{}).Rows[0]
	if r.Price.Source != pricing.Live || !near(r.Price.Hourly, 0.343*1.1, 1e-9) || !near(r.Cost.Compute, 0.343*1.1*3*730, 1e-6) {
		t.Errorf("row = price %+v, cost %+v", r.Price, r.Cost)
	}
}

func TestRowCostCountsEveryClusterAndTheNetwork(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Network.Mode = network.MultiRegion
	req.Network.Regions = 3
	req.Rows = []RowRequest{{ID: "r1", Provider: "gcp", Region: "europe-west1", Instance: "z3-highmem-16-highlssd", Nodes: 4}}
	resp := Compute(req, referenceQuoter{})
	if len(resp.Rows) != 1 {
		t.Fatalf("rows = %+v", resp.Rows)
	}
	r := resp.Rows[0]
	p, _ := catalog.Get("gcp")
	inst, _ := p.Instance("z3-highmem-16-highlssd")
	want := inst.PriceHour * 4 * 730 * 3
	if !near(r.Cost.Compute, want, 1e-6) || r.Cost.Clusters != 3 || r.Cost.Network != resp.Network.Costs["gcp"] || !near(r.Cost.Total, r.Cost.Compute+r.Cost.Network, 1e-6) {
		t.Errorf("cost = %+v, want compute %v", r.Cost, want)
	}
	// Inter-region traffic is priced with the region rates (0.02 for every provider by default).
	if !near(resp.Network.Costs["gcp"], resp.Network.MonthlyGB*0.02, 1e-6) || !near(resp.Network.Costs["azure"], resp.Network.MonthlyGB*0.02, 1e-6) {
		t.Errorf("network costs = %v for %v GB", resp.Network.Costs, resp.Network.MonthlyGB)
	}
}

func TestNetworkTrafficGrowsWithTheThroughput(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Network.Mode = network.MultiRegion
	req.Network.Regions = 3
	resp := Compute(req, referenceQuoter{})
	// 40,000 writes/s × 1.3 growth × 1,024 B × (3 − 1) regions × (1 − 50 % compression).
	if want := 40000 * 1.3 * 1024 * 2 * 0.5; !near(resp.Network.BytesPerSec["xdr"], want, 1e-6) {
		t.Errorf("xdr = %v B/s, want %v", resp.Network.BytesPerSec["xdr"], want)
	}
}

// Reads that find no record still cost CPU and cross the network; only their device blocks go.
func TestReadsThatFindNothingKeepTheirCPUAndNetwork(t *testing.T) {
	all := Compute(DefaultRequest(), referenceQuoter{})
	if all.Workload.ReadsFound == nil || *all.Workload.ReadsFound != 1 {
		t.Fatalf("default share of reads found = %v, want 1", all.Workload.ReadsFound)
	}
	req := DefaultRequest()
	found := 0.37
	req.Workload.ReadsFound = &found
	resp := Compute(req, referenceQuoter{})
	d, base := resp.Generic.Demand, all.Generic.Demand
	// 260,000 reads/s at the horizon, 37 % of them on the device: 96,200 read IOPS next to 33,800 of writes.
	if !near(d.ReadIOPS, 96200, 1e-6) || !near(d.IOPS, 96200+33800, 1e-6) || *resp.Workload.ReadsFound != 0.37 {
		t.Errorf("read IOPS %v, IOPS %v, share echoed %v", d.ReadIOPS, d.IOPS, *resp.Workload.ReadsFound)
	}
	if d.CPU != base.CPU || d.TPS != base.TPS || resp.Network.MonthlyGB != all.Network.MonthlyGB || resp.Generic.Nodes != all.Generic.Nodes {
		t.Errorf("CPU %v (was %v), traffic %v GB/month (was %v), nodes %d (was %d)", d.CPU, base.CPU, resp.Network.MonthlyGB, all.Network.MonthlyGB, resp.Generic.Nodes, all.Generic.Nodes)
	}
}

func TestTightRowGetsAComfortableAlternative(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Workload.ReadsPerSec = 50000 // 117,000 tx/s at the horizon: CPU at 81 % of the usable vCPUs, RAM is the matter
	// 3 × i4i.2xlarge, one down: 2 × 40.3 GiB of usable RAM (64 GiB − 6.4 GiB reserve, × 70 %) for 77.5 GiB of index (96 %): tight.
	req.Rows = []RowRequest{{ID: "r1", Provider: "aws", Region: "eu-west-3", Instance: "i4i.2xlarge", Nodes: 3}}
	resp := Compute(req, referenceQuoter{})
	r := resp.Rows[0]
	if r.Fit.Status != sizing.Tight || !near(r.Fit.Load[sizing.ByRAM], 0.961, 0.001) || r.Alternative == nil {
		t.Fatalf("row = %+v", r)
	}
	if r.Alternative.ID != "r1/alt" || r.Alternative.Instance.Name != "i4i.4xlarge" || r.Alternative.Nodes != 3 || r.Alternative.Fit.Status != sizing.OK {
		t.Errorf("alternative = %+v", r.Alternative)
	}
	// A pending price on the alternative alone keeps the answer pending.
	resp = Compute(req, referenceQuoter{pendingFor: "i4i.4xlarge"})
	if r := resp.Rows[0]; !resp.Pending || r.Price.Source != pricing.Live || r.Alternative.Price.Source != pricing.Pending {
		t.Errorf("pending %v, row price %+v, alternative price %+v", resp.Pending, r.Price, r.Alternative.Price)
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

// A shape whose RAM all goes to the OS reserve is skipped: the alternative is the next one that fits.
func TestAlternativeSkipsShapesWithoutUsableRAM(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Hypotheses.OSReserveGiB = 16
	req.Workload = sizing.Workload{ObjectSize: 1024, Objects: 125e6, ReadsPerSec: 1000, WritesPerSec: 100, RF: 2, Engine: sizing.Device}
	req.Rows = []RowRequest{{ID: "r1", Provider: "aws", Region: "eu-west-3", Instance: "i4i.xlarge", Nodes: 3}}
	r := Compute(req, referenceQuoter{}).Rows[0]
	if r.Fit.Status != sizing.Tight || r.Alternative == nil || r.Alternative.Instance.Name != "i4i.2xlarge" {
		t.Errorf("row = %s, alternative %+v", r.Fit.Status, r.Alternative)
	}
}

func TestOversizedWorkloadLeavesANotePerProvider(t *testing.T) {
	req := DefaultRequest()
	req.Workload.Objects = 1e14 // 16 PB of primary index
	resp := Compute(req, referenceQuoter{})
	if resp.Generic.Nodes != 0 || len(resp.Rows) != 0 || len(resp.Notes) != 3 || resp.Notes[0] != "AWS: no instance type of the catalog suits this workload." {
		t.Errorf("generic nodes %d, rows %d, notes %q", resp.Generic.Nodes, len(resp.Rows), resp.Notes)
	}
}

func TestGenericChecksAndNotes(t *testing.T) {
	// RF1 over three AZs without rack-awareness: two nodes meet the RF+1 floor but not the AZ spread.
	req := DefaultRequest()
	req.Auto = nil
	req.Workload = sizing.Workload{ObjectSize: 100, Objects: 1e6, ReadsPerSec: 100, WritesPerSec: 10, RF: 1, Engine: sizing.Device}
	req.Network.RackAware = false
	resp := Compute(req, referenceQuoter{})
	if resp.Generic.Nodes != 2 || len(resp.Generic.Checks) != 1 || !strings.Contains(resp.Generic.Checks[0], "Fewer nodes than configured AZs (3)") {
		t.Errorf("generic = %d nodes, checks %q", resp.Generic.Nodes, resp.Generic.Checks)
	}
	// A rack loss cannot be planned for without rack-aware AZs: the note says what is planned instead.
	req = DefaultRequest()
	req.Auto = nil
	req.Hypotheses.Loss = sizing.RackLoss
	req.Network.RackAware = false
	if resp := Compute(req, referenceQuoter{}); len(resp.Notes) != 1 || !strings.Contains(resp.Notes[0], "loss of one node") {
		t.Errorf("notes = %q", resp.Notes)
	}
	req.Network.RackAware = true
	if resp := Compute(req, referenceQuoter{}); len(resp.Notes) != 0 {
		t.Errorf("notes = %q", resp.Notes)
	}
	req.Network.Mode = network.MultiRegion
	if resp := Compute(req, referenceQuoter{}); len(resp.Notes) != 1 {
		t.Errorf("notes = %q", resp.Notes)
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

func TestNodeAmountsAreBounded(t *testing.T) {
	n := Node{VCPU: -1, RAMGiB: 1e30, StorageTB: 1e30, IOPS: 1e30}
	a := n.Amounts()
	if a.CPU != 0 || a.RAM != MaxNodeRAMGiB*catalog.GiB || a.Storage != MaxNodeStorageTB*1e12 || a.IOPS != MaxNodeIOPS {
		t.Errorf("amounts = %+v", a)
	}
	want := []sizing.Clamp{
		{Field: "node.vcpu", Given: -1, Used: 0}, {Field: "node.ram_gib", Given: 1e30, Used: MaxNodeRAMGiB},
		{Field: "node.storage_tb", Given: 1e30, Used: MaxNodeStorageTB}, {Field: "node.iops", Given: 1e30, Used: MaxNodeIOPS},
	}
	if _, clamps := n.Bounded(); !slices.Equal(clamps, want) {
		t.Errorf("clamps = %+v", clamps)
	}
	if _, clamps := DefaultRequest().Node.Bounded(); len(clamps) != 0 {
		t.Errorf("default node clamps = %+v", clamps)
	}
}

// A memory namespace is sized with the RAM ceiling its records call for: between the storage fill
// (records) and the RAM fill (indexes), and the answer says so.
func TestMemoryNamespaceIsPlannedAtTheStorageFill(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Workload.Engine = sizing.Memory
	resp := Compute(req, referenceQuoter{})
	d := resp.Generic.Demand
	share := d.DataRAM / d.RAM
	if want := 1 / ((1-share)/0.7 + share/0.5); !near(resp.Hypotheses.RAMFill, want, 1e-9) || resp.Hypotheses.RAMFill >= 0.7 || resp.Hypotheses.RAMFill <= 0.5 {
		t.Errorf("RAM fill = %v, want %v", resp.Hypotheses.RAMFill, want)
	}
	if !near(resp.Generic.Usable.RAM, resp.Hypotheses.RAMFill*(128-12.8)*catalog.GiB, 1) {
		t.Errorf("usable RAM = %v", resp.Generic.Usable.RAM)
	}
	if resp.Generic.Binding != sizing.ByRAM || !strings.Contains(resp.Generic.Explain, "51% of RAM") || !strings.Contains(resp.Generic.Explain, "50% storage fill of the records in memory") {
		t.Errorf("explanation = %q", resp.Generic.Explain)
	}
	req.Workload.Engine = sizing.Device
	if resp := Compute(req, referenceQuoter{}); resp.Hypotheses.RAMFill != 0.7 {
		t.Errorf("device engine: RAM fill = %v", resp.Hypotheses.RAMFill)
	}
}
