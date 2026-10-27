package sizing

import (
	"math"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

// workload is the sizer's default profile: 500 M records of 1 KiB, RF2.
func workload() Workload {
	return Workload{ObjectSize: 1024, Objects: 500e6, ReadsPerSec: 200000, WritesPerSec: 40000, RF: 2, Engine: Device}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b)) }

func TestDemandCountsEveryCopyWithOverheadAndGrowth(t *testing.T) {
	h := DefaultHypotheses()
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

	w := workload()
	w.Engine = Memory
	m := DemandOf(w, h)
	if m.Storage != 0 || !near(m.RAM, (1024*500e6*2*1.1+500e6*2*64)*1.3) || !near(m.IOPS, 40000*2*1.3) {
		t.Errorf("memory engine demand = %+v", m)
	}
}

func TestUsableAppliesCeilingsAndOSReserve(t *testing.T) {
	u := DefaultHypotheses().Usable(Amounts{Storage: 3.75e12, RAM: 128 * catalog.GiB, CPU: 16, IOPS: 200000})
	if !near(u.Storage, 3.75e12*0.7) || !near(u.RAM, 123*catalog.GiB*0.7) || !near(u.CPU, 9.6) || u.IOPS != 200000 {
		t.Errorf("usable = %+v", u)
	}
}

func TestSizeKeepsASpareNodeAndFillsRacks(t *testing.T) {
	h := DefaultHypotheses()
	node := Amounts{Storage: 1e12, RAM: 1000 * catalog.GiB, IOPS: 1e7}
	d := Amounts{Storage: 3.5e12} // 3.5e12 / (1e12 × 0.7) = 5 nodes, + 1 spare

	s := Size(d, node, h, Layout{RF: 2, AZs: 1, Racks: 1})
	if s.Nodes != 6 || s.Binding != ByStorage || s.Need[ByStorage] != 6 || s.Rounded {
		t.Errorf("sizing = %+v", s)
	}
	s = Size(d, node, h, Layout{RF: 2, AZs: 4, Racks: 4})
	if s.Nodes != 8 || s.Binding != ByStorage || !s.Rounded {
		t.Errorf("with 4 racks, sizing = %+v", s)
	}
	h.NodeLoss = false
	if s := Size(d, node, h, Layout{RF: 2, AZs: 1, Racks: 1}); s.Nodes != 5 {
		t.Errorf("without node loss, nodes = %d", s.Nodes)
	}
	if !near(s.Utilization[ByStorage], 3.5e12/8e12) {
		t.Errorf("utilization = %v", s.Utilization)
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
	s := Size(DemandOf(workload(), h), tiny, h, Layout{RF: 2, AZs: 1, Racks: 1})
	if s.Nodes != 0 || s.Binding != ByRAM {
		t.Fatalf("sizing = %+v", s)
	}
	if e := s.Explain(workload(), DemandOf(workload(), h), tiny, h, Layout{RF: 2}); !strings.Contains(e, "no usable RAM") {
		t.Errorf("explanation = %q", e)
	}
}

func TestEvaluateGradesLoadWithOneNodeDown(t *testing.T) {
	h := DefaultHypotheses()
	node := Amounts{Storage: 1e12, RAM: 1000 * catalog.GiB, IOPS: 1e7}
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
}

func TestEvaluateTopology(t *testing.T) {
	h := DefaultHypotheses()
	node := Amounts{Storage: 1e13, RAM: 1000 * catalog.GiB, IOPS: 1e7}
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
	// 16 vCPU at 60 %: 9 × i4i.xlarge ($3.09/h) beats 3 × i4i.4xlarge ($4.12/h) and
	// 6 × i4i.2xlarge ($4.12/h); 3 × i4i.2xlarge would hold the data but not the CPU.
	if o.Instance.Name != "i4i.xlarge" || o.Nodes != 9 {
		t.Errorf("recommendation = %s × %d", o.Instance.Name, o.Nodes)
	}
	if f := Evaluate(d, OfInstance(o.Instance), o.Nodes, h, l); f.Status != OK {
		t.Errorf("the recommendation must be comfortable: %+v", f)
	}
}

func TestRecommendAddsNodesUntilComfortable(t *testing.T) {
	h := DefaultHypotheses()
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
	w := Workload{ObjectSize: -1, RF: 0, Engine: "bogus"}.Normalize()
	if w.ObjectSize != 0 || w.RF != 1 || w.Engine != Device {
		t.Errorf("workload = %+v", w)
	}
	h := Hypotheses{CacheHit: 2, StorageFill: 0, RAMFill: 3, DefragMult: 0.5}.Normalize()
	if h.CacheHit != 1 || h.StorageFill != 0.05 || h.RAMFill != 1 || h.DefragMult != 1 {
		t.Errorf("hypotheses = %+v", h)
	}
}
