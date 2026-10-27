package sizing

import (
	"math"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

// workload is the sizer's default profile: 500 M records of 1 KiB, RF2, index in RAM.
func workload() Workload {
	return Workload{ObjectSize: 1024, Objects: 500e6, ReadsPerSec: 200000, WritesPerSec: 40000, RF: 2, Engine: Device, IndexOn: InRAM, SindexOn: InRAM}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b)) }

func TestDemandCountsEveryCopyWithOverheadAndGrowth(t *testing.T) {
	h := DefaultHypotheses()
	h.WriteOverhead = 0.1
	d := DemandOf(workload(), h)
	if want := 1024 * 500e6 * 2 * 1.1 * 1.3; !near(d.Storage, want) {
		t.Errorf("storage = %v, want %v", d.Storage, want)
	}
	if want := 500e6 * 2 * 64 * 1.3; !near(d.RAM, want) {
		t.Errorf("RAM = %v, want %v (index only on device)", d.RAM, want)
	}
	if want := 40000*2*1.3 + 200000*0.3; !near(d.IOPS, want) {
		t.Errorf("IOPS = %v, want %v", d.IOPS, want)
	}
	if want := 240000 / 15000.0; !near(d.CPU, want) {
		t.Errorf("CPU = %v vCPU, want %v", d.CPU, want)
	}
	h.RecordOverhead, h.Compression = 56, 0.5
	if d := DemandOf(workload(), h); !near(d.Storage, 1080*500e6*2*1.1*1.3*0.5) {
		t.Errorf("with overhead and compression, storage = %v", d.Storage)
	}
}

func TestMemoryEngineKeepsRecordsInRAMAndPersistsThemOnRequest(t *testing.T) {
	h := DefaultHypotheses()
	w := workload()
	w.Engine = Memory
	d := Detailed(w, h)
	if d.Storage != 0 || !near(d.DataRAM, 1024*500e6*2*1.3) || !near(d.RAM, (1024+64)*500e6*2*1.3) || d.IOPS != 0 {
		t.Errorf("memory engine demand = %+v", d)
	}
	w.Persist = true
	d = Detailed(w, h)
	if !near(d.Storage, 1024*500e6*2*1.3) || !near(d.IOPS, 40000*2*1.3) || d.ReadIOPS != 0 {
		t.Errorf("persisted memory engine demand = %+v", d)
	}
}

// The capacity planning guide's all-flash example: 4 billion records, RF2, sprigs half full.
func TestFlashIndexFollowsTheCapacityPlanningGuide(t *testing.T) {
	h := DefaultHypotheses()
	h.Growth = 0
	w := Workload{ObjectSize: 1024, Objects: 4e9, ReadsPerSec: 1000, WritesPerSec: 100, RF: 2, Engine: Device, IndexOn: OnFlash}
	d := Detailed(w, h)
	if d.SprigsPerPartition != 32768 {
		t.Errorf("sprigs per partition = %d, want 32768", d.SprigsPerPartition)
	}
	sprigs := 4096.0 * 2 * 32768 // 2^28
	if !near(d.FlashIndex, sprigs*(4096+5)) || d.Index != 0 {
		t.Errorf("flash index = %v, want 1 TiB of blocks plus 1.25 GiB of roots", d.FlashIndex)
	}
	if !near(d.Sprigs, sprigs*10) || !near(d.RAM, sprigs*10) { // 2.5 GiB
		t.Errorf("sprig RAM = %v, RAM = %v", d.Sprigs, d.RAM)
	}
	if !near(d.IndexIOPS, 1000+2*100*2) || !near(d.Storage, d.Data+d.FlashIndex) {
		t.Errorf("index IOPS = %v, storage = %v", d.IndexIOPS, d.Storage)
	}
	if e := (Sizing{Nodes: 4, Binding: ByStorage}).Explain(w, d, Amounts{Storage: 1e15, RAM: 1e15, CPU: 1e3, IOPS: 1e6}, h, Layout{RF: 2}); !strings.Contains(e, "32768 sprigs per partition") {
		t.Errorf("explanation = %q", e)
	}
}

// The secondary index guide's example: 10 million entries take 10^7 × 14 B × 1.5 = 200 MiB.
func TestSecondaryAndSetIndexes(t *testing.T) {
	h := DefaultHypotheses()
	h.Growth = 0
	w := Workload{ObjectSize: 100, Objects: 1e7, RF: 1, Engine: Device, IndexOn: InRAM, SindexEntries: 1, SindexOn: InRAM, SetIndex: true}
	d := Detailed(w, h)
	if !near(d.Sindex, 1e7*14*1.5) || !near(d.SetIndex, 1e7*16) || !near(d.RAM, 1e7*(64+21+16)) {
		t.Errorf("indexes = sindex %v, set %v, RAM %v", d.Sindex, d.SetIndex, d.RAM)
	}
	w.RF, w.SindexEntries, w.SindexOn = 2, 3, OnFlash
	d = Detailed(w, h)
	if !near(d.FlashSindex, 2e7*3*14*1.5) || d.Sindex != 0 || !near(d.Storage, d.Data+d.FlashSindex) {
		t.Errorf("sindex on flash = %v, storage %v", d.FlashSindex, d.Storage)
	}
}

func TestUsableAppliesCeilingsAndOSReserve(t *testing.T) {
	h := DefaultHypotheses()
	h.StorageFill = 0.7
	// 10 % of 128 GiB is more than the 5 GiB floor.
	u := h.Usable(Amounts{Storage: 3.75e12, RAM: 128 * catalog.GiB, CPU: 16, IOPS: 200000})
	if !near(u.Storage, 3.75e12*0.7) || !near(u.RAM, 115.2*catalog.GiB*0.7) || !near(u.CPU, 9.6) || u.IOPS != 200000 {
		t.Errorf("usable = %+v", u)
	}
	if r := h.OSReserve(32 * catalog.GiB); r != 5*catalog.GiB {
		t.Errorf("OS reserve on 32 GiB = %v, want the 5 GiB floor", r)
	}
}

func TestServingNodesAfterTheLoss(t *testing.T) {
	h := DefaultHypotheses()
	three := Layout{RF: 2, AZs: 3, Racks: 3}
	cases := []struct {
		loss  Loss
		l     Layout
		nodes int
		want  int
	}{
		{NoLoss, three, 6, 6},
		{NodeLoss, three, 6, 5},
		{RackLoss, three, 6, 4},
		{RackLoss, three, 7, 4},
		{RackLoss, Layout{RF: 2, AZs: 1, Racks: 1}, 6, 5},
		{NodeLoss, three, 1, 1},
	}
	for _, c := range cases {
		h.Loss = c.loss
		if got := h.Serving(c.nodes, c.l); got != c.want {
			t.Errorf("%s, %d nodes over %d racks: serving %d, want %d", c.loss, c.nodes, c.l.Racks, got, c.want)
		}
	}
}

func TestSizeKeepsHeadroomAndFillsRacks(t *testing.T) {
	h := DefaultHypotheses()
	h.StorageFill = 0.7
	node := Amounts{Storage: 1e12, RAM: 1000 * catalog.GiB, CPU: 1000, IOPS: 1e7}
	d := Amounts{Storage: 3.5e12} // 3.5e12 / (1e12 × 0.7) = 5 serving nodes

	s := Size(d, node, h, Layout{RF: 2, AZs: 1, Racks: 1})
	if s.Nodes != 6 || s.Binding != ByStorage || s.Need[ByStorage] != 6 || s.Rounded {
		t.Errorf("one node down: sizing = %+v", s)
	}
	s = Size(d, node, h, Layout{RF: 2, AZs: 4, Racks: 4})
	if s.Nodes != 8 || s.Binding != ByStorage || !s.Rounded {
		t.Errorf("with 4 racks, sizing = %+v", s)
	}
	h.Loss = RackLoss
	// 8 nodes over 3 racks leave 5 after a rack loss; rounded up to 9.
	s = Size(d, node, h, Layout{RF: 2, AZs: 3, Racks: 3})
	if s.Nodes != 9 || s.Need[ByStorage] != 8 || !s.Rounded {
		t.Errorf("one rack down: sizing = %+v", s)
	}
	h.Loss = NoLoss
	s = Size(d, node, h, Layout{RF: 2, AZs: 1, Racks: 1})
	if s.Nodes != 5 || !near(s.Utilization[ByStorage], 3.5e12/5e12) {
		t.Errorf("without loss, sizing = %+v", s)
	}
}

func TestSizeFloorsAtRFPlusOne(t *testing.T) {
	h := DefaultHypotheses()
	big := Amounts{Storage: 1e15, RAM: 1e15, CPU: 1e6, IOPS: 1e9}
	s := Size(DemandOf(workload(), h), big, h, Layout{RF: 2, AZs: 1, Racks: 1})
	if s.Nodes != 3 || s.Binding != ByTopology {
		t.Errorf("sizing = %+v", s)
	}
	s = Size(DemandOf(workload(), h), big, h, Layout{RF: 2, AZs: 2, Racks: 2})
	if s.Nodes != 4 || s.Binding != ByRacks {
		t.Errorf("RF+1 over two racks: sizing = %+v", s)
	}
}

func TestSizeWithoutUsableResource(t *testing.T) {
	h := DefaultHypotheses()
	tiny := Amounts{Storage: 1e12, RAM: 4 * catalog.GiB, CPU: 64, IOPS: 1e6} // all RAM goes to the OS reserve
	w := workload()
	d := Detailed(w, h)
	s := Size(d.Amounts, tiny, h, Layout{RF: 2, AZs: 1, Racks: 1})
	if s.Nodes != 0 || s.Binding != ByRAM {
		t.Fatalf("sizing = %+v", s)
	}
	if e := s.Explain(w, d, tiny, h, Layout{RF: 2}); !strings.Contains(e, "no usable RAM") {
		t.Errorf("explanation = %q", e)
	}
}

func TestEvaluateGradesLoadWithOneNodeDown(t *testing.T) {
	h := DefaultHypotheses()
	h.StorageFill = 0.7
	node := Amounts{Storage: 1e12, RAM: 1000 * catalog.GiB, CPU: 1000, IOPS: 1e7}
	l := Layout{RF: 2, AZs: 1, Racks: 1}
	// 4 nodes, one down: 3 × 0.7 TB = 2.1 TB usable.
	cases := []struct {
		storage float64
		status  Status
		check   string
	}{
		{1.5e12, OK, "Comfortable"},
		{2.0e12, Tight, "storage tight: 95%"},
		{2.2e12, Short, "Not enough storage: 2.20 TB needed vs 2.10 TB usable (70% fill, one node down)"},
	}
	for _, c := range cases {
		f := Evaluate(Amounts{Storage: c.storage}, node, 4, h, l)
		if f.Status != c.status || !strings.Contains(strings.ToLower(strings.Join(f.Checks, " ")), strings.ToLower(c.check)) {
			t.Errorf("storage %v: %+v", c.storage, f)
		}
	}
	h.Loss = RackLoss
	// 6 nodes over 3 racks, one rack down: 4 × 0.7 TB = 2.8 TB usable.
	f := Evaluate(Amounts{Storage: 3e12}, node, 6, h, Layout{RF: 2, AZs: 3, Racks: 3})
	if f.Status != Short || !strings.Contains(f.Checks[0], "one rack down") {
		t.Errorf("rack loss: %+v", f)
	}
}

func TestEvaluateTopology(t *testing.T) {
	h := DefaultHypotheses()
	node := Amounts{Storage: 1e13, RAM: 1000 * catalog.GiB, CPU: 1000, IOPS: 1e7}
	if f := Evaluate(Amounts{Storage: 1e9}, node, 2, h, Layout{RF: 2, AZs: 1, Racks: 1}); f.Status != Short {
		t.Errorf("2 nodes at RF2: %+v", f)
	}
	if f := Evaluate(Amounts{Storage: 1e9}, node, 5, h, Layout{RF: 2, AZs: 3, Racks: 3}); f.Status != Tight || !strings.Contains(f.Checks[0], "split evenly") {
		t.Errorf("5 nodes over 3 racks: %+v", f)
	}
	if f := Evaluate(Amounts{Storage: 1e9}, node, 3, h, Layout{RF: 3, AZs: 4, Racks: 1}); f.Status != Short || !strings.Contains(strings.Join(f.Checks, " "), "Fewer nodes than configured AZs") {
		t.Errorf("3 nodes over 4 AZs at RF3: %+v", f)
	}
}

func TestRecommendPicksTheCheapestComfortableFit(t *testing.T) {
	h := DefaultHypotheses()
	p, _ := catalog.Get("aws")
	l := Layout{RF: 2, AZs: 3, Racks: 3}
	d := DemandOf(workload(), h)
	o, ok := Recommend(p, d, h, l)
	if !ok {
		t.Fatal("no recommendation")
	}
	if f := Evaluate(d, OfInstance(o.Instance), o.Nodes, h, l); f.Status != OK {
		t.Errorf("the recommendation must be comfortable: %+v", f)
	}
	// No other instance type reaches a comfortable fit for less, nodes penalty included.
	score := func(inst catalog.Instance, n int) float64 { return inst.PriceHour * float64(n) * (1 + float64(n)/50) }
	for _, inst := range p.Instances {
		if s := Size(d, OfInstance(inst), h, l); s.Nodes > 0 {
			if n, ok := comfortableNodes(d, OfInstance(inst), s.Nodes, h, l); ok && score(inst, n) < score(o.Instance, o.Nodes)-1e-9 {
				t.Errorf("%s × %d scores %v, under %s × %d at %v", inst.Name, n, score(inst, n), o.Instance.Name, o.Nodes, score(o.Instance, o.Nodes))
			}
		}
	}
	if o.Nodes%3 != 0 {
		t.Errorf("%d nodes do not fill 3 racks", o.Nodes)
	}
}

func TestRecommendAddsNodesUntilComfortable(t *testing.T) {
	h := DefaultHypotheses()
	h.StorageFill = 0.7
	p := &catalog.Provider{Instances: []catalog.Instance{{Name: "one", VCPU: 64, RAMGiB: 512, Drives: 1, DriveGB: 1000, IOPS: 1e6, PriceHour: 1}}}
	l := Layout{RF: 2, AZs: 1, Racks: 1}
	// 1.3 TB on 3 nodes, 2 serving × 700 GB usable: 93 %, tight; a fourth node brings it to 62 %.
	if o, ok := Recommend(p, Amounts{Storage: 1.3e12}, h, l); !ok || o.Nodes != 4 {
		t.Errorf("recommendation = %+v, %v", o, ok)
	}
	// 700 TB holds on 1,001 nodes; comfort would take more than 64 more: the holding one is kept.
	if o, ok := Recommend(p, Amounts{Storage: 7e14}, h, l); !ok || o.Nodes != 1001 {
		t.Errorf("recommendation = %+v, %v", o, ok)
	}
	if _, ok := Recommend(p, Amounts{RAM: 1e18}, h, l); ok {
		t.Error("an impossible demand has no recommendation")
	}
}

func TestComfortableStaysUnderTheComfortLoad(t *testing.T) {
	h := DefaultHypotheses()
	p, _ := catalog.Get("azure")
	l := Layout{RF: 2, AZs: 3, Racks: 3}
	d := DemandOf(workload(), h)
	inst, ok := Comfortable(p, d, h, l, 3)
	if !ok {
		t.Fatal("no comfortable instance")
	}
	f := Evaluate(d, OfInstance(inst), 3, h, l)
	if f.MaxLoad() > ComfortLoad {
		t.Errorf("%s: max load %v", inst.Name, f.MaxLoad())
	}
	smaller := p.Instances[0]
	if smaller.Name != inst.Name && Evaluate(d, OfInstance(smaller), 3, h, l).MaxLoad() <= ComfortLoad {
		t.Errorf("%s is comfortable too and smaller than %s", smaller.Name, inst.Name)
	}
}

func TestNormalize(t *testing.T) {
	w := Workload{ObjectSize: -1, RF: 0, Engine: "bogus", Persist: true, IndexOn: "pmem", SindexEntries: -2}.Normalize()
	if w.ObjectSize != 0 || w.RF != 1 || w.Engine != Device || w.Persist || w.IndexOn != InRAM || w.SindexOn != InRAM || w.SindexEntries != 0 {
		t.Errorf("workload = %+v", w)
	}
	h := Hypotheses{CacheHit: 2, StorageFill: 0, RAMFill: 3, DefragMult: 0.5, Loss: "all", Compression: 0, SindexSparseness: 0.2, OSReservePct: 2}.Normalize()
	if h.CacheHit != 1 || h.StorageFill != 0.05 || h.RAMFill != 1 || h.DefragMult != 1 || h.Loss != NodeLoss || h.Compression != 0.05 || h.SindexSparseness != 1 || h.OSReservePct != 0.9 {
		t.Errorf("hypotheses = %+v", h)
	}
}
