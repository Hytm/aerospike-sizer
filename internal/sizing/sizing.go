// Package sizing estimates the nodes a workload needs and how well a given configuration fits it.
// Every output is an estimate resting on the hypotheses it was computed with; the constants come
// from the Aerospike capacity planning guide (docs/decisions.md, T9).
package sizing

import (
	"math"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

// Engine is the namespace storage engine.
type Engine string

// Storage engines.
const (
	Device Engine = "device" // data on NVMe
	Memory Engine = "memory" // data in RAM, optionally persisted on a device
)

// Placement is where an index lives.
type Placement string

// Index placements.
const (
	InRAM   Placement = "ram"   // shared memory
	OnFlash Placement = "flash" // NVMe mounts (all-flash)
)

// Workload is what the cluster must hold and serve.
type Workload struct {
	ObjectSize   float64   `json:"object_size"` // average record size as stored, bytes
	Objects      float64   `json:"objects"`     // master records
	ReadsPerSec  float64   `json:"reads_per_sec"`
	WritesPerSec float64   `json:"writes_per_sec"`
	RF           int       `json:"rf"`
	Engine       Engine    `json:"engine"`
	Persist      bool      `json:"persist"`  // memory engine: records are also written to a device
	IndexOn      Placement `json:"index_on"` // primary index placement; in RAM with the memory engine
	// SindexEntries is the number of secondary index entries one record contributes, summed over
	// the indexes: one per scalar bin indexed, one per distinct element for a list or map bin.
	SindexEntries float64   `json:"sindex_entries"`
	SindexOn      Placement `json:"sindex_on"`
	SetIndex      bool      `json:"set_index"` // set indexes on every record
}

// Bounds of a workload: the largest values an estimate accepts, so that every figure stays finite
// whatever the request says. MaxObjectSize is the write block, the largest record Aerospike stores.
const (
	MaxRF            = 8
	MaxObjectSize    = 8 << 20
	MaxObjects       = 1e15
	MaxRate          = 1e9 // transactions per second
	MaxSindexEntries = 1e4
)

// Normalize clamps the workload to meaningful values and drops the placements its engine rules out.
func (w Workload) Normalize() Workload {
	w.ObjectSize = clamp(w.ObjectSize, 0, MaxObjectSize)
	w.Objects = clamp(w.Objects, 0, MaxObjects)
	w.ReadsPerSec = clamp(w.ReadsPerSec, 0, MaxRate)
	w.WritesPerSec = clamp(w.WritesPerSec, 0, MaxRate)
	w.RF = min(max(w.RF, 1), MaxRF)
	if w.Engine != Memory {
		w.Engine = Device
		w.Persist = false
	}
	// index-type flash does not work with storage-engine memory (configuration reference, 7.0).
	if w.IndexOn != OnFlash || w.Engine == Memory {
		w.IndexOn = InRAM
	}
	if w.SindexOn != OnFlash {
		w.SindexOn = InRAM
	}
	w.SindexEntries = clamp(w.SindexEntries, 0, MaxSindexEntries)
	return w
}

func clamp(v, lo, hi float64) float64 { return min(max(v, lo), hi) }

// OnDevice tells whether records are written to a device.
func (w Workload) OnDevice() bool { return w.Engine == Device || w.Persist }

// Grown returns the workload at the planning horizon: records and throughput grow alike.
func (w Workload) Grown(h Hypotheses) Workload {
	g := 1 + h.Growth
	w.Objects *= g
	w.ReadsPerSec *= g
	w.WritesPerSec *= g
	return w
}

// Loss is the failure the ceilings must survive.
type Loss string

// Losses.
const (
	NoLoss   Loss = "none"
	NodeLoss Loss = "node" // N+1
	RackLoss Loss = "rack" // a whole rack (an availability zone)
)

// Hypotheses are the assumptions of every estimate; the interface shows and edits them.
type Hypotheses struct {
	Growth         float64 `json:"growth"`          // growth of the records and of the throughput over the planning horizon (0.3 = +30 %)
	IndexBytes     float64 `json:"index_bytes"`     // primary index per record copy (64 B)
	RecordOverhead float64 `json:"record_overhead"` // bytes added to the object size when it is the payload only
	WriteOverhead  float64 `json:"write_overhead"`  // write-block overhead: device space on top of the stored records (0.1 = +10 %)
	Compression    float64 `json:"compression"`     // stored size of records as a share of their size (1: no compression), on device and in memory
	DefragMult     float64 `json:"defrag_mult"`     // device writes amplification from defragmentation
	CacheHit       float64 `json:"cache_hit"`       // share of reads served without a device read
	StorageFill    float64 `json:"storage_fill"`    // highest device usage per node
	RAMFill        float64 `json:"ram_fill"`        // highest share of the RAM left after the OS reserve
	OSReserveGiB   float64 `json:"os_reserve_gib"`  // memory kept for the OS and the process, at least
	OSReservePct   float64 `json:"os_reserve_pct"`  // … and as a share of the node's RAM
	IOTarget       float64 `json:"io_target"`       // highest share of the sustained IOPS used at peak
	OpsPerVCPU     float64 `json:"ops_per_vcpu"`    // transactions per second one vCPU sustains at 100 %
	CPUTarget      float64 `json:"cpu_target"`      // highest CPU utilisation at peak
	Loss           Loss    `json:"loss"`            // what the ceilings must survive
	// All-flash: a sprig is a 4 KiB index block holding up to 64 entries; sprigs are sized for a
	// fill share, and each costs some RAM.
	SprigFill     float64 `json:"sprig_fill"`
	SprigRAMBytes float64 `json:"sprig_ram_bytes"`
	// Secondary indexes: bytes per entry, and the B-tree sparseness (1.5 on average, 2 at worst).
	SindexEntryBytes float64 `json:"sindex_entry_bytes"`
	SindexSparseness float64 `json:"sindex_sparseness"`
}

// Constants of the Aerospike storage layout.
const (
	Partitions      = 4096 // partitions of a namespace
	RecordsPerSprig = 64   // 64 entries of 64 B fill one 4 KiB index block
	SprigBlockBytes = 4096 // flash index block
	SprigRootBytes  = 5    // sprig roots written at shutdown, on disk
	SetIndexBytes   = 16   // set index per record copy
	// MinSprigsPerPart and MaxSprigsPerPart are the range of partition-tree-sprigs in the
	// configuration reference: default and minimum 256, maximum 256M, always a power of two.
	MinSprigsPerPart = 256
	MaxSprigsPerPart = 1 << 28
	// SupportSprigsPerPart is the value above which the reference asks Enterprise licensees to
	// consult Aerospike Support.
	SupportSprigsPerPart = 1 << 15
	// DeviceBlockBytes is the unit device IO is counted in: the 4 KiB block the sustained random
	// read IOPS of the catalog are measured with.
	DeviceBlockBytes = 4096
)

// Bounds of the hypotheses, for the same reason as the workload's.
const (
	MaxGrowth        = 100 // +10,000 %
	MaxBytesPerEntry = 1 << 20
	MaxWriteOverhead = 10
	MaxDefragMult    = 100
	MaxOpsPerVCPU    = 1e7
	MaxOSReserveGiB  = 1e6
	MaxSparseness    = 100
)

// DefaultHypotheses follow the capacity planning guide: 64 B of primary index per copy, device usage
// planned at 50 % (the default defrag-lwm-pct) with no write overhead on top, 10 % of RAM for the OS,
// sprigs half full with 10 B of RAM each, secondary index entries of 14 B at 1.5 sparseness. The ceilings
// (storage 50 %, RAM 70 %, CPU 60 % of 15,000 transactions per vCPU, IO 100 %), the spare node and the
// 70 % of reads served by the cache are aerospike-wellness's defaults too.
func DefaultHypotheses() Hypotheses {
	return Hypotheses{
		Growth: 0.3, IndexBytes: 64, RecordOverhead: 0, WriteOverhead: 0, Compression: 1, DefragMult: 1.3, CacheHit: 0.7,
		StorageFill: 0.5, RAMFill: 0.7, OSReserveGiB: 5, OSReservePct: 0.1, IOTarget: 1, OpsPerVCPU: 15000, CPUTarget: 0.6,
		Loss: NodeLoss, SprigFill: 0.5, SprigRAMBytes: 10, SindexEntryBytes: 14, SindexSparseness: 1.5,
	}
}

// Normalize clamps the hypotheses to meaningful values.
func (h Hypotheses) Normalize() Hypotheses {
	h.Growth = clamp(h.Growth, 0, MaxGrowth)
	h.IndexBytes = clamp(h.IndexBytes, 0, MaxBytesPerEntry)
	h.RecordOverhead = clamp(h.RecordOverhead, 0, MaxObjectSize)
	h.WriteOverhead = clamp(h.WriteOverhead, 0, MaxWriteOverhead)
	h.Compression = ceiling(h.Compression)
	h.DefragMult = clamp(h.DefragMult, 1, MaxDefragMult)
	h.CacheHit = clamp(h.CacheHit, 0, 1)
	h.StorageFill = ceiling(h.StorageFill)
	h.RAMFill = ceiling(h.RAMFill)
	h.IOTarget = ceiling(h.IOTarget)
	h.CPUTarget = ceiling(h.CPUTarget)
	h.OpsPerVCPU = clamp(h.OpsPerVCPU, 100, MaxOpsPerVCPU)
	h.OSReserveGiB = clamp(h.OSReserveGiB, 0, MaxOSReserveGiB)
	h.OSReservePct = clamp(h.OSReservePct, 0, 0.9)
	if h.Loss != NoLoss && h.Loss != RackLoss {
		h.Loss = NodeLoss
	}
	h.SprigFill = ceiling(h.SprigFill)
	h.SprigRAMBytes = clamp(h.SprigRAMBytes, 0, MaxBytesPerEntry)
	h.SindexEntryBytes = clamp(h.SindexEntryBytes, 0, MaxBytesPerEntry)
	h.SindexSparseness = clamp(h.SindexSparseness, 1, MaxSparseness)
	return h
}

func ceiling(v float64) float64 { return clamp(v, 0.05, 1) }

// Constraint names what drives a node count.
type Constraint string

// Constraints: the four resources, then the topology rules.
const (
	ByStorage  Constraint = "storage"
	ByRAM      Constraint = "ram"
	ByCPU      Constraint = "cpu"
	ByIOPS     Constraint = "iops"
	ByTopology Constraint = "topology" // RF+1 nodes at least
	ByRacks    Constraint = "racks"    // rounded up to fill every rack equally
)

// Resources lists the sized resources.
var Resources = []Constraint{ByStorage, ByRAM, ByCPU, ByIOPS}

// Amounts are quantities of the resources: what a node offers or what the workload needs.
type Amounts struct {
	Storage float64 `json:"storage"` // bytes on NVMe
	RAM     float64 `json:"ram"`     // bytes of memory
	CPU     float64 `json:"cpu"`     // vCPUs
	IOPS    float64 `json:"iops"`    // device operations per second
}

// Of returns the amount of one resource.
func (a Amounts) Of(c Constraint) float64 {
	switch c {
	case ByStorage:
		return a.Storage
	case ByRAM:
		return a.RAM
	case ByCPU:
		return a.CPU
	case ByIOPS:
		return a.IOPS
	}
	return 0
}

// Scaled returns every amount divided by a share: the demand that fills the same capacity to that
// share only.
func (a Amounts) Scaled(share float64) Amounts {
	return Amounts{Storage: a.Storage / share, RAM: a.RAM / share, CPU: a.CPU / share, IOPS: a.IOPS / share}
}

// OfInstance returns the raw resources of an instance type.
func OfInstance(i catalog.Instance) Amounts {
	return Amounts{Storage: i.StorageBytes(), RAM: i.RAMBytes(), CPU: float64(i.VCPU), IOPS: i.IOPS}
}

// OSReserve returns the memory kept for the OS on a node: the fixed amount, or the share of its
// RAM when that is more.
func (h Hypotheses) OSReserve(nodeRAM float64) float64 {
	return max(h.OSReserveGiB*catalog.GiB, h.OSReservePct*nodeRAM)
}

// Capacity returns what utilization is measured against: the raw resources of a node, its RAM
// less the OS reserve.
func (h Hypotheses) Capacity(node Amounts) Amounts {
	return Amounts{Storage: node.Storage, RAM: max(node.RAM-h.OSReserve(node.RAM), 0), CPU: node.CPU, IOPS: node.IOPS}
}

// Usable returns what one node offers at the ceilings.
func (h Hypotheses) Usable(node Amounts) Amounts {
	c := h.Capacity(node)
	return Amounts{Storage: c.Storage * h.StorageFill, RAM: c.RAM * h.RAMFill, CPU: c.CPU * h.CPUTarget, IOPS: c.IOPS * h.IOTarget}
}

// Detail is the demand with what makes it up, every copy, overheads and growth included (growth
// applies to the records and to the throughput alike).
type Detail struct {
	Amounts
	TPS float64 `json:"tps"` // transactions per second at the horizon
	// Storage parts.
	Data        float64 `json:"data"`         // records on device
	FlashIndex  float64 `json:"flash_index"`  // primary index blocks and sprig roots on NVMe
	FlashSindex float64 `json:"flash_sindex"` // secondary indexes on NVMe
	// RAM parts.
	Index    float64 `json:"index"`     // primary index in memory
	Sprigs   float64 `json:"sprigs"`    // memory of the flash index's sprigs
	Sindex   float64 `json:"sindex"`    // secondary indexes in memory
	SetIndex float64 `json:"set_index"` // set indexes
	DataRAM  float64 `json:"data_ram"`  // records in memory
	// IOPS parts, in 4 KiB blocks.
	ReadIOPS      float64 `json:"read_iops"`       // record reads reaching the device
	WriteIOPS     float64 `json:"write_iops"`      // bytes of the record writes, defragmentation included
	IndexIOPS     float64 `json:"index_iops"`      // flash index blocks read and written
	BlocksPerRead float64 `json:"blocks_per_read"` // device blocks one record read fetches
	// SprigsPerPartition is the partition-tree-sprigs an all-flash namespace needs.
	SprigsPerPartition int `json:"sprigs_per_partition"`
}

// Detailed computes what the whole cluster must hold and serve at the planning horizon.
func Detailed(w Workload, h Hypotheses) Detail {
	w = w.Grown(h)
	objects := w.Objects
	copies := objects * float64(w.RF)
	record := (w.ObjectSize + h.RecordOverhead) * copies
	onDevice := record * (1 + h.WriteOverhead) * h.Compression
	stored := (w.ObjectSize + h.RecordOverhead) * h.Compression // one record copy, as stored
	var d Detail
	if w.Engine == Memory {
		// Records are compressed in memory too (storage-engine memory, 7.0+).
		d.DataRAM = record * h.Compression
	}
	if w.OnDevice() {
		d.Data = onDevice
		// Records fill 8 MiB write blocks: the device writes the bytes of every copy, counted in
		// 4 KiB blocks whatever the record size.
		d.WriteIOPS = w.WritesPerSec * float64(w.RF) * stored * h.DefragMult / DeviceBlockBytes
	}
	if w.Engine == Device {
		// A read fetches the 4 KiB blocks its record spans.
		d.BlocksPerRead = max(math.Ceil(stored/DeviceBlockBytes), 1)
		d.ReadIOPS = w.ReadsPerSec * (1 - h.CacheHit) * d.BlocksPerRead
	}
	if w.IndexOn == OnFlash {
		d.SprigsPerPartition = sprigsPerPartition(objects, h.SprigFill)
		sprigs := float64(Partitions) * float64(w.RF) * float64(d.SprigsPerPartition)
		d.FlashIndex = sprigs * (SprigBlockBytes + SprigRootBytes)
		d.Sprigs = sprigs * h.SprigRAMBytes
		// Each read looks its entry up on flash; each replica write reads and writes its block.
		d.IndexIOPS = w.ReadsPerSec + 2*w.WritesPerSec*float64(w.RF)
	} else {
		d.Index = copies * h.IndexBytes
	}
	if w.SindexEntries > 0 {
		sindex := copies * w.SindexEntries * h.SindexEntryBytes * h.SindexSparseness
		if w.SindexOn == OnFlash {
			d.FlashSindex = sindex
		} else {
			d.Sindex = sindex
		}
	}
	if w.SetIndex {
		d.SetIndex = copies * SetIndexBytes
	}
	d.Storage = d.Data + d.FlashIndex + d.FlashSindex
	d.RAM = d.Index + d.Sprigs + d.Sindex + d.SetIndex + d.DataRAM
	d.TPS = w.ReadsPerSec + w.WritesPerSec
	d.CPU = d.TPS / h.OpsPerVCPU
	d.IOPS = d.ReadIOPS + d.WriteIOPS + d.IndexIOPS
	return d
}

// sprigsPerPartition returns the partition-tree-sprigs an all-flash namespace needs: records over
// 64 entries at the fill share, over the partitions, rounded up to a power of two within the
// configurable range.
func sprigsPerPartition(objects, fill float64) int {
	need := math.Ceil(objects / (RecordsPerSprig * fill) / Partitions)
	if need >= MaxSprigsPerPart {
		return MaxSprigsPerPart
	}
	n := MinSprigsPerPart
	for float64(n) < need {
		n <<= 1
	}
	return n
}

// DemandOf returns what the whole cluster must hold and serve.
func DemandOf(w Workload, h Hypotheses) Amounts { return Detailed(w, h).Amounts }

// Layout is how the nodes are spread.
type Layout struct {
	RF    int `json:"rf"`
	AZs   int `json:"azs"`   // availability zones the nodes spread over (1 when not modelled)
	Racks int `json:"racks"` // rack-aware racks, one per AZ (1 without rack awareness)
}

// MinNodes is the topology floor.
func (l Layout) MinNodes() int { return l.RF + 1 }

// Serving returns the nodes left to carry the load after the loss the hypotheses plan for: one
// node, or one rack's worth of nodes (one node when racks are not modelled).
func (h Hypotheses) Serving(nodes int, l Layout) int {
	switch {
	case nodes <= 1 || h.Loss == NoLoss:
		return nodes
	case h.Loss == RackLoss && l.Racks > 1:
		return nodes - int(math.Ceil(float64(nodes)/float64(l.Racks)))
	}
	return nodes - 1
}

// MaxNodes bounds a node count: beyond, the node shape does not suit the workload.
const MaxNodes = 10000

// Sizing is the node count a node shape calls for.
type Sizing struct {
	Nodes   int        `json:"nodes"` // 0 when no sensible node count works
	Binding Constraint `json:"binding"`
	Rounded bool       `json:"rounded"` // nodes added to fill every rack equally
	// Need is the nodes each resource needs, spare nodes included; a resource for which no sensible
	// count works is left out.
	Need map[Constraint]int `json:"need"`
	// Utilization is the share of the capacity (Hypotheses.Capacity) used at peak with every node up.
	Utilization map[Constraint]float64 `json:"utilization"`
}

// Size computes the nodes of one shape the demand needs: each resource of a node is used up to its
// ceiling on the nodes left after the planned loss, never fewer than RF+1, rounded up to fill
// every rack. When a resource has no sensible count, Nodes is 0 and Binding names it.
func Size(d, node Amounts, h Hypotheses, l Layout) Sizing {
	s := Sizing{Nodes: l.MinNodes(), Binding: ByTopology, Need: map[Constraint]int{ByTopology: l.MinNodes()}, Utilization: map[Constraint]float64{}}
	usable := h.Usable(node)
	var failed Constraint
	for _, c := range Resources {
		demand := d.Of(c)
		if demand <= 0 {
			s.Need[c] = 0
			continue
		}
		ratio := demand / usable.Of(c)
		if usable.Of(c) <= 0 || ratio > MaxNodes {
			if failed == "" {
				failed = c
			}
			continue
		}
		need := nodesFor(int(math.Ceil(ratio)), h, l)
		s.Need[c] = need
		if need > s.Nodes {
			s.Nodes, s.Binding = need, c
		}
	}
	if failed != "" {
		return Sizing{Binding: failed, Need: s.Need, Utilization: map[Constraint]float64{}}
	}
	if l.Racks > 1 && s.Nodes%l.Racks != 0 {
		s.Nodes += l.Racks - s.Nodes%l.Racks
		s.Rounded = true
		if s.Binding == ByTopology {
			s.Binding = ByRacks
		}
	}
	s.Utilization = utilization(d, h.Capacity(node), s.Nodes)
	return s
}

// nodesFor returns the fewest nodes that leave the serving count after the planned loss: the
// serving nodes plus the lost ones, even when one node would carry the load.
func nodesFor(serving int, h Hypotheses, l Layout) int {
	need := serving
	if h.Loss != NoLoss {
		need++
	}
	for h.Serving(need, l) < serving {
		need++
	}
	return need
}

// utilization returns the share of the capacity of nodes the demand uses, per resource.
func utilization(d, capacity Amounts, nodes int) map[Constraint]float64 {
	u := map[Constraint]float64{}
	for _, c := range Resources {
		if capacity.Of(c) > 0 && nodes > 0 {
			u[c] = d.Of(c) / (capacity.Of(c) * float64(nodes))
		}
	}
	return u
}
