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
	IndexOn      Placement `json:"index_on"` // primary index placement
	// SindexEntries is the number of secondary index entries one record contributes, summed over
	// the indexes: one per scalar bin indexed, one per distinct element for a list or map bin.
	SindexEntries float64   `json:"sindex_entries"`
	SindexOn      Placement `json:"sindex_on"`
	SetIndex      bool      `json:"set_index"` // set indexes on every record
}

// MaxRF bounds the replication factor.
const MaxRF = 8

// Normalize clamps the workload to meaningful values.
func (w Workload) Normalize() Workload {
	w.ObjectSize = max(w.ObjectSize, 0)
	w.Objects = max(w.Objects, 0)
	w.ReadsPerSec = max(w.ReadsPerSec, 0)
	w.WritesPerSec = max(w.WritesPerSec, 0)
	w.RF = min(max(w.RF, 1), MaxRF)
	if w.Engine != Memory {
		w.Engine = Device
		w.Persist = false
	}
	if w.IndexOn != OnFlash {
		w.IndexOn = InRAM
	}
	if w.SindexOn != OnFlash {
		w.SindexOn = InRAM
	}
	w.SindexEntries = max(w.SindexEntries, 0)
	return w
}

// OnDevice tells whether records are written to a device.
func (w Workload) OnDevice() bool { return w.Engine == Device || w.Persist }

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
	Growth         float64 `json:"growth"`          // growth of the records over the planning horizon (0.3 = +30 %)
	IndexBytes     float64 `json:"index_bytes"`     // primary index per record copy (64 B)
	RecordOverhead float64 `json:"record_overhead"` // bytes added to the object size when it is the payload only
	WriteOverhead  float64 `json:"write_overhead"`  // fragmentation on top of the storage fill
	Compression    float64 `json:"compression"`     // stored size of records as a share of their size (1: no compression)
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
	Partitions       = 4096 // partitions of a namespace
	RecordsPerSprig  = 64   // 64 entries of 64 B fill one 4 KiB index block
	SprigBlockBytes  = 4096 // flash index block
	SprigRootBytes   = 5    // sprig roots written at shutdown, on disk
	SetIndexBytes    = 16   // set index per record copy
	MaxSprigsPerPart = 1 << 16
)

// DefaultHypotheses follow the capacity planning guide: 64 B of primary index per copy, device usage
// planned at 50 % (defrag-lwm-pct), 10 % of RAM for the OS, sprigs half full with 10 B of RAM each,
// secondary index entries of 14 B at 1.5 sparseness; with the ceilings of aerospike-wellness for
// RAM (70 %), CPU (60 % of 15,000 transactions per vCPU) and IO, and one spare node.
func DefaultHypotheses() Hypotheses {
	return Hypotheses{
		Growth: 0.3, IndexBytes: 64, RecordOverhead: 0, WriteOverhead: 0, Compression: 1, DefragMult: 1.3, CacheHit: 0.7,
		StorageFill: 0.5, RAMFill: 0.7, OSReserveGiB: 5, OSReservePct: 0.1, IOTarget: 1, OpsPerVCPU: 15000, CPUTarget: 0.6,
		Loss: NodeLoss, SprigFill: 0.5, SprigRAMBytes: 10, SindexEntryBytes: 14, SindexSparseness: 1.5,
	}
}

// Normalize clamps the hypotheses to meaningful values.
func (h Hypotheses) Normalize() Hypotheses {
	h.Growth = max(h.Growth, 0)
	h.IndexBytes = max(h.IndexBytes, 0)
	h.RecordOverhead = max(h.RecordOverhead, 0)
	h.WriteOverhead = max(h.WriteOverhead, 0)
	h.Compression = ceiling(h.Compression)
	h.DefragMult = max(h.DefragMult, 1)
	h.CacheHit = min(max(h.CacheHit, 0), 1)
	h.StorageFill = ceiling(h.StorageFill)
	h.RAMFill = ceiling(h.RAMFill)
	h.IOTarget = ceiling(h.IOTarget)
	h.CPUTarget = ceiling(h.CPUTarget)
	h.OpsPerVCPU = max(h.OpsPerVCPU, 100)
	h.OSReserveGiB = max(h.OSReserveGiB, 0)
	h.OSReservePct = min(max(h.OSReservePct, 0), 0.9)
	if h.Loss != NoLoss && h.Loss != RackLoss {
		h.Loss = NodeLoss
	}
	h.SprigFill = ceiling(h.SprigFill)
	h.SprigRAMBytes = max(h.SprigRAMBytes, 0)
	h.SindexEntryBytes = max(h.SindexEntryBytes, 0)
	h.SindexSparseness = max(h.SindexSparseness, 1)
	return h
}

func ceiling(v float64) float64 { return min(max(v, 0.05), 1) }

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
// applies to the records, not to the throughput).
type Detail struct {
	Amounts
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
	// IOPS parts.
	ReadIOPS  float64 `json:"read_iops"`  // record reads reaching the device
	WriteIOPS float64 `json:"write_iops"` // record writes, defragmentation included
	IndexIOPS float64 `json:"index_iops"` // flash index blocks read and written
	// SprigsPerPartition is the partition-tree-sprigs an all-flash namespace needs.
	SprigsPerPartition int `json:"sprigs_per_partition"`
}

// Detailed computes what the whole cluster must hold and serve.
func Detailed(w Workload, h Hypotheses) Detail {
	g := 1 + h.Growth
	objects := w.Objects * g
	copies := objects * float64(w.RF)
	record := (w.ObjectSize + h.RecordOverhead) * copies
	onDevice := record * (1 + h.WriteOverhead) * h.Compression
	var d Detail
	if w.Engine == Memory {
		d.DataRAM = record
	}
	if w.OnDevice() {
		d.Data = onDevice
		d.WriteIOPS = w.WritesPerSec * float64(w.RF) * h.DefragMult
	}
	if w.Engine == Device {
		d.ReadIOPS = w.ReadsPerSec * (1 - h.CacheHit)
	}
	if w.IndexOn == OnFlash {
		// Sprigs per partition: records over 64 entries at the fill share, over the partitions,
		// rounded up to a power of two; every copy of every partition has that many.
		perPartition := math.Ceil(objects / (RecordsPerSprig * h.SprigFill) / Partitions)
		d.SprigsPerPartition = min(nextPowerOfTwo(perPartition), MaxSprigsPerPart)
		sprigs := float64(Partitions * w.RF * d.SprigsPerPartition)
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
	d.CPU = (w.ReadsPerSec + w.WritesPerSec) / h.OpsPerVCPU
	d.IOPS = d.ReadIOPS + d.WriteIOPS + d.IndexIOPS
	return d
}

func nextPowerOfTwo(v float64) int {
	n := 1
	for float64(n) < v {
		n *= 2
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
	Nodes   int                `json:"nodes"` // 0 when no sensible node count works
	Binding Constraint         `json:"binding"`
	Rounded bool               `json:"rounded"` // nodes added to fill every rack equally
	Need    map[Constraint]int `json:"need"`    // nodes each resource needs, spare nodes included
	// Utilization is the share of the capacity (Hypotheses.Capacity) used at peak with every node up.
	Utilization map[Constraint]float64 `json:"utilization"`
}

// Size computes the nodes of one shape the demand needs: each resource of a node is used up to its
// ceiling on the nodes left after the planned loss, never fewer than RF+1, rounded up to fill
// every rack.
func Size(d, node Amounts, h Hypotheses, l Layout) Sizing {
	s := Sizing{Nodes: l.MinNodes(), Binding: ByTopology, Need: map[Constraint]int{ByTopology: l.MinNodes()}, Utilization: map[Constraint]float64{}}
	usable := h.Usable(node)
	for _, c := range Resources {
		demand := d.Of(c)
		if demand <= 0 {
			s.Need[c] = 0
			continue
		}
		ratio := demand / usable.Of(c)
		if usable.Of(c) <= 0 || ratio > MaxNodes {
			return Sizing{Binding: c, Need: s.Need, Utilization: s.Utilization}
		}
		need := int(math.Ceil(ratio))
		for h.Serving(need, l) < int(math.Ceil(ratio)) {
			need++
		}
		s.Need[c] = need
		if need > s.Nodes {
			s.Nodes, s.Binding = need, c
		}
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
