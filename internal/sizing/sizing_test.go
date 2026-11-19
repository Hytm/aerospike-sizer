package sizing

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

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
	d := Detailed(workload(), h)
	if want := 1024 * 500e6 * 2 * 1.1 * 1.3; !near(d.Storage, want) {
		t.Errorf("storage = %v, want %v", d.Storage, want)
	}
	if want := 500e6 * 2 * 64 * 1.3; !near(d.RAM, want) {
		t.Errorf("RAM = %v, want %v (index only on device)", d.RAM, want)
	}
	// 1 KiB records: a quarter of a 4 KiB block per copy written, one block per read (no cache by default).
	if want := (40000*2*1024.0/4096*1.3 + 200000) * 1.3; !near(d.IOPS, want) || d.BlocksPerRead != 1 {
		t.Errorf("IOPS = %v, want %v (throughput grows too)", d.IOPS, want)
	}
	if want := 240000 * 1.3 / 15000.0; !near(d.CPU, want) || !near(d.TPS, 240000*1.3) {
		t.Errorf("CPU = %v vCPU for %v tx/s, want %v", d.CPU, d.TPS, want)
	}
	if grown := workload().Grown(h); !near(grown.Objects, 500e6*1.3) || !near(grown.ReadsPerSec, 200000*1.3) || !near(grown.WritesPerSec, 40000*1.3) || grown.RF != 2 {
		t.Errorf("grown workload = %+v", grown)
	}
	h.RecordOverhead, h.Compression = 56, 0.5
	if d := DemandOf(workload(), h); !near(d.Storage, 1080*500e6*2*1.1*1.3*0.5) || !near(d.IOPS, (40000*2*540.0/4096*1.3+200000)*1.3) {
		t.Errorf("with overhead and compression, storage = %v, IOPS = %v", d.Storage, d.IOPS)
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
	if !near(d.Storage, 1024*500e6*2*1.3) || !near(d.IOPS, 40000*2*1024.0/4096*1.3*1.3) || d.ReadIOPS != 0 {
		t.Errorf("persisted memory engine demand = %+v", d)
	}
	// Records are compressed in memory as on the device (7.0+).
	h.Compression = 0.5
	d = Detailed(w, h)
	if !near(d.DataRAM, 1024*500e6*2*1.3*0.5) || !near(d.Storage, 1024*500e6*2*1.3*0.5) || !near(d.WriteIOPS, 40000*2*512.0/4096*1.3*1.3) {
		t.Errorf("compressed memory engine demand = %+v", d)
	}
}

// Device IO is counted in 4 KiB blocks, as in the Aerospike sizing estimator spreadsheet: a read
// fetches every block its record spans, a write puts the bytes of every copy into 8 MiB write blocks.
func TestDeviceIOCountsBlocks(t *testing.T) {
	h := DefaultHypotheses()
	h.Growth, h.CacheHit, h.DefragMult, h.RecordOverhead = 0, 0, 1, 64
	// 12,064 B stored span 3 blocks; 2 copies × 1,000 writes/s × 12,064 B = 5,890.6 blocks/s.
	large := Workload{ObjectSize: 12000, Objects: 1e6, ReadsPerSec: 50000, WritesPerSec: 1000, RF: 2, Engine: Device, IndexOn: InRAM}
	if d := Detailed(large, h); d.BlocksPerRead != 3 || !near(d.ReadIOPS, 150000) || !near(d.WriteIOPS, 2*1000*12064/4096.0) {
		t.Errorf("large records: %v blocks per read, read %v, write %v", d.BlocksPerRead, d.ReadIOPS, d.WriteIOPS)
	}
	// 364 B stored: one block per read; 50,000 writes/s × 2 copies write 8,887 blocks/s, not 100,000.
	small := Workload{ObjectSize: 300, Objects: 1e6, ReadsPerSec: 50000, WritesPerSec: 50000, RF: 2, Engine: Device, IndexOn: InRAM}
	d := Detailed(small, h)
	if d.BlocksPerRead != 1 || !near(d.ReadIOPS, 50000) || !near(d.WriteIOPS, 2*50000*364/4096.0) {
		t.Errorf("small records: %v blocks per read, read %v, write %v", d.BlocksPerRead, d.ReadIOPS, d.WriteIOPS)
	}
	h.Compression = 0.25 // 3,016 B stored: one block
	if d := Detailed(large, h); d.BlocksPerRead != 1 || !near(d.WriteIOPS, 2*1000*3016/4096.0) {
		t.Errorf("compressed large records: %v blocks per read, write %v", d.BlocksPerRead, d.WriteIOPS)
	}
	// A record of exactly one block as stored reads one, though its size computes an ulp above (T18);
	// a byte more spans two.
	h.Compression, h.RecordOverhead = 1, 0
	for size, want := range map[float64]float64{math.Nextafter(4096, 5000): 1, 4097: 2} {
		if d := Detailed(Workload{ObjectSize: size, Objects: 1, ReadsPerSec: 1, RF: 1, Engine: Device}, h); d.BlocksPerRead != want {
			t.Errorf("%v B stored: %v blocks per read, want %v", size, d.BlocksPerRead, want)
		}
	}
	e := (Sizing{Nodes: 3, Binding: ByIOPS}).Explain(large, Detailed(large, DefaultHypotheses()), Amounts{IOPS: 1e5}, DefaultHypotheses(), Layout{RF: 2})
	if !strings.Contains(e, "IOPS of 4 KiB") || !strings.Contains(e, "record reads of 3 blocks") {
		t.Errorf("explanation = %q", e)
	}
}

func share(v float64) *float64 { return &v }

// A read that finds no record is answered from the primary index: it costs CPU (and a flash index
// lookup) like any read, but no record block on the device.
func TestDeviceReadBlocksCountTheReadsThatFindARecord(t *testing.T) {
	h := DefaultHypotheses()
	all := Detailed(workload(), h) // field absent: every read finds its record, as before
	if !near(all.ReadIOPS, 200000*1.3) || all.BlocksPerRead != 1 {
		t.Fatalf("reads all found: read IOPS %v, %v blocks per read", all.ReadIOPS, all.BlocksPerRead)
	}
	for _, c := range []struct {
		found *float64
		want  float64 // share of the read blocks charged
	}{
		{nil, 1}, {share(1), 1}, {share(0.37), 0.37}, {share(0), 0}, {share(-0.5), 0}, {share(1.7), 1},
	} {
		w := workload()
		w.ReadsFound = c.found
		d := Detailed(w, h)
		if !near(d.ReadIOPS, all.ReadIOPS*c.want) || !near(d.WriteIOPS, all.WriteIOPS) || !near(d.CPU, all.CPU) || !near(d.TPS, all.TPS) {
			t.Errorf("found %v: read IOPS %v (want %v), writes %v, CPU %v", w.FoundShare(), d.ReadIOPS, all.ReadIOPS*c.want, d.WriteIOPS, d.CPU)
		}
	}
	// The cache share is measured over the reads that reach the storage: it applies on top.
	h.CacheHit = 0.5
	w := workload()
	w.ReadsFound = share(0.37)
	if d := Detailed(w, h); !near(d.ReadIOPS, 200000*1.3*0.37*0.5) {
		t.Errorf("found 37 %%, cache 50 %%: read IOPS %v", d.ReadIOPS)
	}
	// Large records: the found reads fetch every block their record spans.
	h = DefaultHypotheses()
	h.Growth = 0
	large := Workload{ObjectSize: 12000, Objects: 1e6, ReadsPerSec: 50000, RF: 2, Engine: Device, IndexOn: OnFlash, ReadsFound: share(0.37)}
	d := Detailed(large, h)
	if !near(d.ReadIOPS, 50000*0.37*3) || !near(d.IndexIOPS, 50000) {
		t.Errorf("large records on an all-flash index: read IOPS %v, index IOPS %v (every read looks its entry up)", d.ReadIOPS, d.IndexIOPS)
	}
	e := (Sizing{Nodes: 3, Binding: ByIOPS}).Explain(large, d, Amounts{IOPS: 1e5}, h, Layout{RF: 2})
	if !strings.Contains(e, "record reads of 3 blocks for the 37% of the reads that find a record") {
		t.Errorf("explanation = %q", e)
	}
	if e := (Sizing{Nodes: 3, Binding: ByIOPS}).Explain(workload(), all, Amounts{IOPS: 1e5}, DefaultHypotheses(), Layout{RF: 2}); strings.Contains(e, "find a record") {
		t.Errorf("every read found: explanation = %q", e)
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
	e := (Sizing{Nodes: 4, Binding: ByStorage}).Explain(w, d, Amounts{Storage: 1e15, RAM: 1e15, CPU: 1e3, IOPS: 1e6}, h, Layout{RF: 2})
	if !strings.Contains(e, "32768 sprigs per partition") || strings.Contains(e, "Aerospike Support") {
		t.Errorf("explanation = %q", e)
	}
}

// partition-tree-sprigs stays in the configurable range: 256 at least, 256M at most, a power of two.
func TestSprigsPerPartitionStayInTheConfigurableRange(t *testing.T) {
	h := DefaultHypotheses()
	h.Growth = 0
	w := Workload{ObjectSize: 100, RF: 2, Engine: Device, IndexOn: OnFlash}
	for _, c := range []struct {
		objects, fill float64
		want          int
	}{
		{0, 0.5, 256},
		{1e6, 0.5, 256},
		{4e9, 0.5, 32768},
		{1e10, 0.5, 131072},
		{math.Nextafter(256*4096*32, 1e10), 0.5, 256}, // 256 sprigs exactly, an ulp above (T18)
		{256*4096*32 + 1, 0.5, 512},
		{1e13, 0.05, MaxSprigsPerPart},
		{1e24, 0.5, MaxSprigsPerPart}, // used to overflow the rounding loop and hang
	} {
		w.Objects, h.SprigFill = c.objects, c.fill
		d := Detailed(w, h)
		if d.SprigsPerPartition != c.want {
			t.Errorf("%v records at %v fill: %d sprigs per partition, want %d", c.objects, c.fill, d.SprigsPerPartition, c.want)
		}
		if want := 4096.0 * 2 * float64(c.want) * (4096 + 5); !near(d.FlashIndex, want) || !near(d.Sprigs, 4096*2*float64(c.want)*10) {
			t.Errorf("%v records: flash index %v (want %v), sprig RAM %v", c.objects, d.FlashIndex, want, d.Sprigs)
		}
	}
	w.Objects, h.SprigFill = 1e10, 0.5
	e := (Sizing{Nodes: 4, Binding: ByStorage}).Explain(w, Detailed(w, h), Amounts{Storage: 1e15, RAM: 1e15, CPU: 1e3, IOPS: 1e6}, h, Layout{RF: 2})
	if !strings.Contains(e, "131072 sprigs per partition (above 32,768: ask Aerospike Support)") {
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

func TestZeroWorkloadNeedsTheTopologyFloorOnly(t *testing.T) {
	h := DefaultHypotheses()
	node := Amounts{Storage: 1e12, RAM: 64 * catalog.GiB, CPU: 8, IOPS: 1e5}
	l := Layout{RF: 2, AZs: 1, Racks: 1}
	// Memory engine: nothing at all to hold or serve.
	d := Detailed(Workload{RF: 2, Engine: Memory}.Normalize(), h)
	if d.Amounts != (Amounts{}) || d.BlocksPerRead != 0 {
		t.Errorf("empty memory namespace demand = %+v", d)
	}
	if s := Size(d.Amounts, node, h, l); s.Nodes != 3 || s.Binding != ByTopology || s.Need[ByStorage] != 0 || s.Need[ByRAM] != 0 {
		t.Errorf("sizing = %+v", s)
	}
	// All-flash: the smallest configurable index is still allocated.
	d = Detailed(Workload{RF: 2, Engine: Device, IndexOn: OnFlash}.Normalize(), h)
	if d.SprigsPerPartition != 256 || !near(d.Storage, 4096*2*256*4101) || !near(d.RAM, 4096*2*256*10) || d.IOPS != 0 || d.BlocksPerRead != 0 {
		t.Errorf("empty all-flash namespace demand = %+v", d)
	}
	if s := Size(d.Amounts, node, h, l); s.Nodes != 3 || s.Binding != ByTopology || s.Need[ByStorage] != 2 {
		t.Errorf("sizing = %+v", s)
	}
}

// The largest values Normalize lets through keep every figure finite and the computation short.
func TestExtremeInputsStayFinite(t *testing.T) {
	w := Workload{ObjectSize: 1e300, Objects: 1e300, ReadsPerSec: 1e300, WritesPerSec: 1e300, RF: 99, Engine: Device, IndexOn: OnFlash, SindexEntries: 1e300, SindexOn: OnFlash, SetIndex: true}.Normalize()
	h := Hypotheses{Growth: 1e300, IndexBytes: 1e300, RecordOverhead: 1e300, WriteOverhead: 1e300, Compression: 1, DefragMult: 1e300, StorageFill: 1, RAMFill: 1,
		OSReserveGiB: 1e300, OSReservePct: 0, IOTarget: 1, OpsPerVCPU: 100, CPUTarget: 1, SprigFill: 0.05, SprigRAMBytes: 1e300, SindexEntryBytes: 1e300, SindexSparseness: 1e300}.Normalize()
	node := Amounts{Storage: 1e17, RAM: 1e6 * catalog.GiB, CPU: 1e5, IOPS: 1e9}
	l := Layout{RF: w.RF, AZs: 12, Racks: 12}
	done := make(chan struct{})
	var d Detail
	var s Sizing
	var f Fit
	go func() {
		defer close(done)
		d = Detailed(w, h)
		s = Size(d.Amounts, node, h, l)
		f = Evaluate(d.Amounts, node, 3, h, l)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sizing the largest workload did not finish")
	}
	finite := func(name string, v float64) {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			t.Errorf("%s = %v", name, v)
		}
	}
	for _, c := range Resources {
		finite("demand "+string(c), d.Of(c))
		finite("utilization "+string(c), s.Utilization[c])
		finite("load "+string(c), f.Load[c])
	}
	finite("TPS", d.TPS)
	if d.SprigsPerPartition != MaxSprigsPerPart || w.RF != MaxRF || s.Nodes != 0 {
		t.Errorf("sprigs %d, RF %d, nodes %d", d.SprigsPerPartition, w.RF, s.Nodes)
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
	// A resource one node would carry still needs its spare when a loss is planned.
	h.Loss = NodeLoss
	if s := Size(Amounts{Storage: 1e11}, node, h, Layout{RF: 2, AZs: 1, Racks: 1}); s.Need[ByStorage] != 2 || s.Nodes != 3 || s.Binding != ByTopology {
		t.Errorf("one node's worth of storage: sizing = %+v", s)
	}
}

// A ratio past a whole number by the error of the arithmetic only is that number; a real excess,
// however small, is one more (T18).
func TestCeilRatioToleratesOnlyTheArithmetic(t *testing.T) {
	for _, c := range []struct{ ratio, want float64 }{
		{0, 0}, {7, 7}, {math.Nextafter(7, 8), 7}, {7 * (1 + 1e-11), 8}, {6.5, 7}, {math.Inf(1), math.Inf(1)},
	} {
		if got := ceilRatio(c.ratio); got != c.want {
			t.Errorf("ceilRatio(%v) = %v, want %v", c.ratio, got, c.want)
		}
	}
	if RatioTolerance != 1e-12 {
		t.Errorf("tolerance = %v, want aerospike-wellness's 1e-12", RatioTolerance)
	}
	if exceeds(1, 1) || exceeds(math.Nextafter(1, 2), 1) || !exceeds(1+1e-11, 1) || exceeds(math.Nextafter(TightLoad, 1), TightLoad) || !exceeds(0.86, TightLoad) {
		t.Error("exceeds must ignore the error of the arithmetic, and only that")
	}
}

// A demand that is an exact multiple of a node needs that many nodes, whatever order the arithmetic
// took (T18), and Evaluate finds them enough: 1,008,000 tx/s are 7 nodes of 16 vCPU at 60 % of
// 15,000 tx/s per vCPU, though 1,008,000 ÷ 15,000 ÷ (16 × 0.6) computes as 7.000000000000001;
// 28.125 TB of records, a mean record (aerospike-wellness's) times its count, are 15 nodes of
// 3.75 TB at 50 %. One transaction per second or one megabyte more is one node more.
func TestSizeCountsAnExactMultipleOnce(t *testing.T) {
	cpu := func(tps float64) Workload {
		return Workload{ObjectSize: 100, Objects: 1, ReadsPerSec: tps / 2, WritesPerSec: tps / 2, RF: 1, Engine: Memory}
	}
	records := func(bytes float64) Workload {
		const objects = 6_719_163_893
		return Workload{ObjectSize: bytes / objects, Objects: objects, RF: 1, Engine: Device}
	}
	node := Amounts{Storage: 3.75e12, RAM: 1 << 50, CPU: 16, IOPS: 1e9}
	l := Layout{RF: 1, AZs: 1, Racks: 1}
	for _, c := range []struct {
		name   string
		w      Workload
		growth float64
		loss   Loss
		by     Constraint
		want   int
	}{
		{"1,008,000 tx/s", cpu(1_008_000), 0, NoLoss, ByCPU, 7},
		{"one transaction more", cpu(1_008_001), 0, NoLoss, ByCPU, 8},
		{"with a spare node", cpu(1_008_000), 0, NodeLoss, ByCPU, 8},
		{"default hypotheses", cpu(12_960_000), 0.3, NodeLoss, ByCPU, 118},
		{"28.125 TB of records", records(2.8125e13), 0, NoLoss, ByStorage, 15},
		{"one megabyte more", records(2.8125e13 + 1e6), 0, NoLoss, ByStorage, 16},
	} {
		h := DefaultHypotheses()
		h.Growth, h.Loss = c.growth, c.loss
		d := DemandOf(c.w, h)
		s := Size(d, node, h, l)
		if s.Nodes != c.want || s.Binding != c.by {
			t.Errorf("%s: %d nodes bound by %s, want %d by %s", c.name, s.Nodes, s.Binding, c.want, c.by)
		}
		if f := Evaluate(d, node, s.Nodes, h, l); f.Status == Short {
			t.Errorf("%s: the %d nodes Size counts are short: %q", c.name, s.Nodes, f.Checks)
		}
	}
}

// Every exact multiple of a node's CPU is sized at that count and holds.
func TestExactMultiplesOfANodeHold(t *testing.T) {
	h := DefaultHypotheses()
	h.Growth, h.Loss = 0, NoLoss
	l := Layout{RF: 1, AZs: 1, Racks: 1}
	for _, vcpu := range []float64{2, 4, 8, 16, 32, 48, 64, 96, 128, 192} {
		node := Amounts{Storage: 1e13, RAM: 1 << 50, CPU: vcpu, IOPS: 1e9}
		for k := 1; k <= 500; k++ {
			half := float64(k) * vcpu * h.OpsPerVCPU * h.CPUTarget / 2
			d := DemandOf(Workload{ObjectSize: 100, Objects: 1, ReadsPerSec: half, WritesPerSec: half, RF: 1, Engine: Memory}, h)
			s := Size(d, node, h, l)
			if checks := strings.Join(Evaluate(d, node, k, h, l).Checks, " "); s.Need[ByCPU] != k || strings.Contains(checks, "Not enough CPU") {
				t.Fatalf("%d nodes' worth of %v vCPU: %d nodes for the CPU; %d nodes: %s", k, vcpu, s.Need[ByCPU], k, checks)
			}
		}
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
	// The other resources are still sized; the failing one is left out.
	if _, ok := s.Need[ByRAM]; ok || s.Need[ByStorage] != 4 || s.Need[ByCPU] != 2 || s.Need[ByIOPS] != 2 || s.Need[ByTopology] != 3 {
		t.Errorf("need = %v", s.Need)
	}
	if e := s.Explain(w, d, tiny, h, Layout{RF: 2}); !strings.Contains(e, "no usable RAM") {
		t.Errorf("explanation = %q", e)
	}
	// Too much demand for the shape: no sensible count either.
	small := Amounts{Storage: 1e8, RAM: 1e15, CPU: 1e6, IOPS: 1e9}
	s = Size(d.Amounts, small, h, Layout{RF: 2, AZs: 1, Racks: 1})
	if e := s.Explain(w, d, small, h, Layout{RF: 2}); s.Nodes != 0 || s.Binding != ByStorage || !strings.Contains(e, "offers too little storage") {
		t.Errorf("sizing = %+v, explanation = %q", s, e)
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
	// A shape without usable RAM is short whatever the count.
	f = Evaluate(Amounts{RAM: 1e9}, Amounts{Storage: 1e12, RAM: 4 * catalog.GiB, CPU: 8, IOPS: 1e5}, 3, h, l)
	if f.Status != Short || !strings.Contains(f.Checks[0], "No usable RAM on this node shape.") || len(f.Load) != 0 {
		t.Errorf("no usable RAM: %+v", f)
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
	score := func(inst catalog.Instance, n int) float64 {
		return inst.PriceHour * float64(n) * (1 + float64(n)/nodePenaltyPer)
	}
	for _, inst := range p.Instances {
		if n, ok := comfortableNodes(d, OfInstance(inst), h, l); ok && score(inst, n) < score(o.Instance, o.Nodes)-1e-9 {
			t.Errorf("%s × %d scores %v, under %s × %d at %v", inst.Name, n, score(inst, n), o.Instance.Name, o.Nodes, score(o.Instance, o.Nodes))
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
	// 700 TB hold on 1,001 nodes at 100 %; 1,178 keep the load under 85 % with one node down.
	o, ok := Recommend(p, Amounts{Storage: 7e14}, h, l)
	if !ok || o.Nodes != 1178 {
		t.Errorf("recommendation = %+v, %v", o, ok)
	}
	if f := Evaluate(Amounts{Storage: 7e14}, OfInstance(p.Instances[0]), o.Nodes, h, l); f.Status != OK || f.Load[ByStorage] > TightLoad {
		t.Errorf("fit = %+v", f)
	}
	// Beyond MaxNodes only the holding count is offered, if any.
	if o, ok := Recommend(p, Amounts{Storage: 6.9e15}, h, l); !ok || o.Nodes != 9859 || Evaluate(Amounts{Storage: 6.9e15}, OfInstance(p.Instances[0]), o.Nodes, h, l).Status != Tight {
		t.Errorf("recommendation near the bound = %+v, %v", o, ok)
	}
	if _, ok := Recommend(p, Amounts{RAM: 1e18}, h, l); ok {
		t.Error("an impossible demand has no recommendation")
	}
}

func TestComfortableIsTheCheapestShapeThatFitsWell(t *testing.T) {
	h := DefaultHypotheses()
	p, _ := catalog.Get("azure")
	l := Layout{RF: 2, AZs: 3, Racks: 3}
	d := DemandOf(workload(), h)
	inst, ok := Comfortable(p, d, h, l, 3)
	if !ok {
		t.Fatal("no comfortable instance")
	}
	if f := Evaluate(d, OfInstance(inst), 3, h, l); f.Status != OK {
		t.Errorf("%s: %+v", inst.Name, f)
	}
	smaller := p.Instances[0]
	if smaller.Name != inst.Name && Evaluate(d, OfInstance(smaller), 3, h, l).Status == OK {
		t.Errorf("%s fits well too and costs less than %s", smaller.Name, inst.Name)
	}
	if _, ok := Comfortable(p, Amounts{RAM: 1e18}, h, l, 3); ok {
		t.Error("an impossible demand has no comfortable shape")
	}
}

// A shape with no usable RAM is skipped, so that a larger shape that fits is offered.
func TestComfortableSkipsShapesWithAnUnusableResource(t *testing.T) {
	h := DefaultHypotheses()
	h.OSReserveGiB = 16 // the whole RAM of an i4i.large
	p, _ := catalog.Get("aws")
	l := Layout{RF: 2, AZs: 3, Racks: 3}
	w := Workload{ObjectSize: 1024, Objects: 125e6, ReadsPerSec: 1000, WritesPerSec: 100, RF: 2, Engine: Device}
	// i4i.large: no usable RAM; i4i.xlarge: 19.4 GiB of index on 2 × 11.2 GiB, tight; i4i.2xlarge fits.
	if inst, ok := Comfortable(p, DemandOf(w, h), h, l, 3); !ok || inst.Name != "i4i.2xlarge" {
		t.Errorf("comfortable = %+v, %v", inst, ok)
	}
}

func TestExplainShowsTheThroughputAtTheHorizon(t *testing.T) {
	h := DefaultHypotheses()
	w := workload()
	d := Detailed(w, h)
	node := Amounts{Storage: 1e13, RAM: 1000 * catalog.GiB, CPU: 16, IOPS: 1e7}
	l := Layout{RF: 2, AZs: 1, Racks: 1}
	// 312,000 tx/s = 20.8 vCPU on 9.6 usable per node: 3 serving nodes plus the spare.
	s := Size(d.Amounts, node, h, l)
	if e := s.Explain(w, d, node, h, l); s.Nodes != 4 || s.Binding != ByCPU || !strings.Contains(e, "312,000 transactions/s") || !strings.Contains(e, "one node down") {
		t.Errorf("sizing %+v, explanation = %q", s, e)
	}
}

func TestExplainNamesWhatDrivesTheSizing(t *testing.T) {
	h := DefaultHypotheses()
	w := workload()
	w.Objects = 5e9 // 774.86 GiB of primary index
	d := Detailed(w, h)
	node := Amounts{Storage: 1e13, RAM: 128 * catalog.GiB, CPU: 16, IOPS: 1e7}
	l := Layout{RF: 2, AZs: 1, Racks: 1}
	s := Size(d.Amounts, node, h, l)
	e := s.Explain(w, d, node, h, l)
	if s.Nodes != 11 || s.Binding != ByRAM || !strings.Contains(e, "Memory drives the sizing: 774.86 GiB (primary index 774.86 GiB; RF2, 30% growth included), 80.64 GiB usable per node") {
		t.Errorf("sizing %+v, explanation = %q", s, e)
	}
	big := Amounts{Storage: 1e15, RAM: 1e15, CPU: 1e6, IOPS: 1e9}
	l = Layout{RF: 2, AZs: 2, Racks: 2}
	s = Size(d.Amounts, big, h, l)
	if e := s.Explain(w, d, big, h, l); !strings.Contains(e, "topology floor (RF+1 = 3)") || !strings.Contains(e, "Rounded up to 4 nodes so that each of the 2 racks") {
		t.Errorf("sizing %+v, explanation = %q", s, e)
	}
}

func TestNormalize(t *testing.T) {
	w := Workload{ObjectSize: -1, RF: 0, Engine: "bogus", Persist: true, IndexOn: "pmem", SindexEntries: -2}.Normalize()
	if w.ObjectSize != 0 || w.RF != 1 || w.Engine != Device || w.Persist || w.IndexOn != InRAM || w.SindexOn != InRAM || w.SindexEntries != 0 {
		t.Errorf("workload = %+v", w)
	}
	// The memory engine keeps its primary index in memory; the secondary indexes may go on flash.
	w = Workload{RF: 2, Engine: Memory, IndexOn: OnFlash, SindexOn: OnFlash}.Normalize()
	if w.Engine != Memory || w.IndexOn != InRAM || w.SindexOn != OnFlash {
		t.Errorf("memory engine workload = %+v", w)
	}
	w = Workload{ObjectSize: 1e30, Objects: 1e30, ReadsPerSec: 1e30, WritesPerSec: 1e30, RF: 99, SindexEntries: 1e30}.Normalize()
	if w.ObjectSize != MaxObjectSize || w.Objects != MaxObjects || w.ReadsPerSec != MaxRate || w.WritesPerSec != MaxRate || w.RF != MaxRF || w.SindexEntries != MaxSindexEntries {
		t.Errorf("bounded workload = %+v", w)
	}
	// Secondary index bytes measured over few or merged records come as many entries per record:
	// they are used as given.
	if w := (Workload{SindexEntries: 25000}).Normalize(); w.SindexEntries != 25000 {
		t.Errorf("25,000 secondary index entries per record: %v used", w.SindexEntries)
	}
	// The share of reads that find a record is 1 when absent and stays within [0, 1].
	for _, c := range []struct {
		in   *float64
		want float64
	}{{nil, 1}, {share(0.37), 0.37}, {share(-1), 0}, {share(0), 0}, {share(1), 1}, {share(5), 1}} {
		w := Workload{ReadsFound: c.in}.Normalize()
		if w.ReadsFound == nil || *w.ReadsFound != c.want || w.FoundShare() != c.want {
			t.Errorf("reads found %v: normalized to %v", c.in, w.ReadsFound)
		}
	}
	// The caller's value is left alone: Normalize works on a copy.
	if v := 5.0; (Workload{ReadsFound: &v}).Normalize().FoundShare() != 1 || v != 5 {
		t.Errorf("Normalize changed the caller's share to %v", v)
	}
	h := Hypotheses{CacheHit: 2, StorageFill: 0, RAMFill: 3, DefragMult: 0.5, Loss: "all", Compression: 0, SindexSparseness: 0.2, OSReservePct: 2}.Normalize()
	if h.CacheHit != 1 || h.StorageFill != 0.05 || h.RAMFill != 1 || h.DefragMult != 1 || h.Loss != NodeLoss || h.Compression != 0.05 || h.SindexSparseness != 1 || h.OSReservePct != 0.9 {
		t.Errorf("hypotheses = %+v", h)
	}
	h = Hypotheses{Growth: 1e9, IndexBytes: 1e9, RecordOverhead: 1e9, WriteOverhead: 1e9, DefragMult: 1e9, OpsPerVCPU: 1e9, OSReserveGiB: 1e9, SprigRAMBytes: 1e9, SindexEntryBytes: 1e9, SindexSparseness: 1e9}.Normalize()
	if h.Growth != MaxGrowth || h.IndexBytes != MaxBytesPerEntry || h.RecordOverhead != MaxObjectSize || h.WriteOverhead != MaxWriteOverhead || h.DefragMult != MaxDefragMult ||
		h.OpsPerVCPU != MaxOpsPerVCPU || h.OSReserveGiB != MaxOSReserveGiB || h.SprigRAMBytes != MaxBytesPerEntry || h.SindexEntryBytes != MaxBytesPerEntry || h.SindexSparseness != MaxSparseness {
		t.Errorf("bounded hypotheses = %+v", h)
	}
}

// Records in memory fill write blocks like a device's: the write-block overhead applies to them, and
// ForDemand plans them at the storage fill while the indexes keep the RAM fill.
func TestMemoryRecordsArePlannedAtTheStorageFill(t *testing.T) {
	h := DefaultHypotheses()
	h.WriteOverhead = 0.1
	w := workload()
	w.Engine = Memory
	d := Detailed(w, h)
	if !near(d.DataRAM, 1024*500e6*2*1.3*1.1) {
		t.Errorf("records in memory = %v", d.DataRAM)
	}
	share := d.DataRAM / d.RAM
	if want := 1 / ((1-share)/0.7 + share/0.5); !near(h.ForDemand(d).RAMFill, want) || h.ForDemand(d).StorageFill != 0.5 {
		t.Errorf("RAM fill for the demand = %v, want %v", h.ForDemand(d).RAMFill, want)
	}
	if hd := h.ForDemand(Detail{Amounts: Amounts{RAM: 100}, DataRAM: 100}); hd.RAMFill != 0.5 {
		t.Errorf("records only: RAM fill %v, want the storage fill", hd.RAMFill)
	}
	if hd := h.ForDemand(Detail{Amounts: Amounts{RAM: 100}}); hd.RAMFill != 0.7 {
		t.Errorf("indexes only: RAM fill %v, want the RAM fill", hd.RAMFill)
	}
	// 100 GiB of records with a negligible index need as many nodes in memory as on a device of
	// the same size (the OS reserve aside): the memory engine is sized with the storage fill.
	big := Workload{ObjectSize: 1 << 20, Objects: 1e5, RF: 1, Engine: Memory}
	h = DefaultHypotheses()
	h.OSReserveGiB, h.OSReservePct = 0, 0
	memory := Detailed(big, h)
	hm := h.ForDemand(memory)
	inMemory := Size(memory.Amounts, Amounts{Storage: 1, RAM: 64 * catalog.GiB, CPU: 64, IOPS: 1e6}, hm, Layout{RF: 1, AZs: 1, Racks: 1})
	big.Engine = Device
	onDevice := Size(Detailed(big, h).Amounts, Amounts{Storage: 64 * catalog.GiB, RAM: 1e15, CPU: 64, IOPS: 1e6}, h, Layout{RF: 1, AZs: 1, Racks: 1})
	if inMemory.Nodes != onDevice.Nodes || inMemory.Binding != ByRAM || onDevice.Binding != ByStorage {
		t.Errorf("in memory %+v, on device %+v", inMemory, onDevice)
	}
}

// Every bound Normalize applies is told, with the value given and the one used; what is within
// its bounds, a share left out and a placement the engine rules out are not.
func TestBoundedTellsEveryClamp(t *testing.T) {
	_, clamps := Workload{ObjectSize: 98500039, Objects: -1, ReadsPerSec: 2e9, WritesPerSec: 10, RF: 10, Engine: Memory, IndexOn: OnFlash, SindexEntries: 2e9, ReadsFound: share(1.5)}.Bounded()
	want := []Clamp{
		{"workload.object_size", 98500039, MaxObjectSize}, {"workload.objects", -1, 0}, {"workload.reads_per_sec", 2e9, MaxRate},
		{"workload.rf", 10, MaxRF}, {"workload.sindex_entries", 2e9, MaxSindexEntries}, {"workload.reads_found", 1.5, 1},
	}
	if !slices.Equal(clamps, want) {
		t.Errorf("workload clamps = %+v", clamps)
	}
	if _, clamps := (Workload{ObjectSize: 1024, Objects: 5e8, RF: 2, SindexEntries: 25000}).Bounded(); len(clamps) != 0 {
		t.Errorf("clamps within the bounds = %+v", clamps)
	}
	h := DefaultHypotheses()
	h.Growth, h.StorageFill, h.Loss = 200, 0.01, "all"
	if _, clamps := h.Bounded(); !slices.Equal(clamps, []Clamp{{"hypotheses.growth", 200, MaxGrowth}, {"hypotheses.storage_fill", 0.01, MinCeiling}}) {
		t.Errorf("hypotheses clamps = %+v", clamps)
	}
	if _, clamps := DefaultHypotheses().Bounded(); len(clamps) != 0 {
		t.Errorf("default hypotheses clamps = %+v", clamps)
	}
}

// No record on the device, or records of no bytes: reads find nothing there and fetch no block,
// as in aerospike-wellness (its ReadsReachDrives); they keep their CPU.
func TestReadsFetchNoBlockWithoutRecordBytes(t *testing.T) {
	h := DefaultHypotheses()
	for _, w := range []Workload{
		{ObjectSize: 0, Objects: 1e6, ReadsPerSec: 1e5, RF: 2, Engine: Device},
		{ObjectSize: 1024, Objects: 0, ReadsPerSec: 1e5, RF: 2, Engine: Device},
	} {
		d := Detailed(w.Normalize(), h)
		if d.ReadIOPS != 0 || d.BlocksPerRead != 0 || d.CPU == 0 {
			t.Errorf("%+v: read IOPS %v, blocks %v, CPU %v", w, d.ReadIOPS, d.BlocksPerRead, d.CPU)
		}
	}
	// A record overhead gives the records bytes: reads fetch them again.
	h.RecordOverhead = 64
	if d := Detailed(Workload{Objects: 1e6, ReadsPerSec: 1e5, RF: 2, Engine: Device}.Normalize(), h); d.BlocksPerRead != 1 || !near(d.ReadIOPS, 1.3e5) {
		t.Errorf("records of 64 B: blocks %v, read IOPS %v", d.BlocksPerRead, d.ReadIOPS)
	}
}
