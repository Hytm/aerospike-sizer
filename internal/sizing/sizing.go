// Package sizing estimates the nodes a workload needs and how well a given configuration fits it.
// Every output is an estimate resting on the hypotheses it was computed with.
package sizing

import (
	"math"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

// Engine is the namespace storage engine.
type Engine string

// Storage engines.
const (
	Device Engine = "device" // data on NVMe, primary index in RAM
	Memory Engine = "memory" // data and primary index in RAM
)

// Workload is what the cluster must hold and serve.
type Workload struct {
	ObjectSize   float64 `json:"object_size"` // average record size, bytes
	Objects      float64 `json:"objects"`     // master records
	ReadsPerSec  float64 `json:"reads_per_sec"`
	WritesPerSec float64 `json:"writes_per_sec"`
	RF           int     `json:"rf"`
	Engine       Engine  `json:"engine"`
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
	}
	return w
}

// Hypotheses are the assumptions of every estimate; the interface shows and edits them.
type Hypotheses struct {
	Growth        float64 `json:"growth"`         // data growth over the planning horizon (0.3 = +30 %)
	IndexBytes    float64 `json:"index_bytes"`    // primary index per record copy
	WriteOverhead float64 `json:"write_overhead"` // write-block / fragmentation overhead on stored data
	DefragMult    float64 `json:"defrag_mult"`    // device writes amplification from defragmentation
	CacheHit      float64 `json:"cache_hit"`      // share of reads served without a device read
	StorageFill   float64 `json:"storage_fill"`   // highest NVMe usage per node (stop-writes-used-pct 70 %)
	RAMFill       float64 `json:"ram_fill"`       // highest share of the RAM left after the OS reserve
	OSReserveGiB  float64 `json:"os_reserve_gib"` // memory kept for the OS and the process
	IOTarget      float64 `json:"io_target"`      // highest share of the sustained IOPS used at peak
	OpsPerVCPU    float64 `json:"ops_per_vcpu"`   // transactions per second one vCPU sustains at 100 %
	CPUTarget     float64 `json:"cpu_target"`     // highest CPU utilisation at peak
	NodeLoss      bool    `json:"node_loss"`      // keep N+1: the ceilings hold with one node down
}

// DefaultHypotheses are the sizer's historical defaults (growth, index, overhead, defrag, cache) with
// the ceilings of aerospike-wellness: storage and RAM at 70 %, 5 GiB for the OS, CPU at 60 % of
// 15,000 transactions per vCPU (simple key-value operations of about 1 KiB on NVMe), N+1.
func DefaultHypotheses() Hypotheses {
	return Hypotheses{
		Growth: 0.3, IndexBytes: 64, WriteOverhead: 0.1, DefragMult: 1.3, CacheHit: 0.7,
		StorageFill: 0.7, RAMFill: 0.7, OSReserveGiB: 5, IOTarget: 1, OpsPerVCPU: 15000, CPUTarget: 0.6, NodeLoss: true,
	}
}

// Normalize clamps the hypotheses to meaningful values.
func (h Hypotheses) Normalize() Hypotheses {
	h.Growth = max(h.Growth, 0)
	h.IndexBytes = max(h.IndexBytes, 0)
	h.WriteOverhead = max(h.WriteOverhead, 0)
	h.DefragMult = max(h.DefragMult, 1)
	h.CacheHit = min(max(h.CacheHit, 0), 1)
	h.StorageFill = ceiling(h.StorageFill)
	h.RAMFill = ceiling(h.RAMFill)
	h.IOTarget = ceiling(h.IOTarget)
	h.CPUTarget = ceiling(h.CPUTarget)
	h.OpsPerVCPU = max(h.OpsPerVCPU, 100)
	h.OSReserveGiB = max(h.OSReserveGiB, 0)
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

// Capacity returns what utilization is measured against: the raw resources of a node, its RAM
// less the OS reserve.
func (h Hypotheses) Capacity(node Amounts) Amounts {
	return Amounts{Storage: node.Storage, RAM: max(node.RAM-h.OSReserveGiB*catalog.GiB, 0), CPU: node.CPU, IOPS: node.IOPS}
}

// Usable returns what one node offers at the ceilings.
func (h Hypotheses) Usable(node Amounts) Amounts {
	c := h.Capacity(node)
	return Amounts{Storage: c.Storage * h.StorageFill, RAM: c.RAM * h.RAMFill, CPU: c.CPU * h.CPUTarget, IOPS: c.IOPS * h.IOTarget}
}

// DemandOf returns what the whole cluster must hold and serve: every copy, overheads and growth
// included (growth applies to the data, not to the throughput). Writes reach the device in every
// engine (memory namespaces persist them); reads only with data on device, less what caches absorb.
// CPU is the vCPUs the transactions need at OpsPerVCPU.
func DemandOf(w Workload, h Hypotheses) Amounts {
	g := 1 + h.Growth
	copies := w.Objects * float64(w.RF)
	data := w.ObjectSize * copies * (1 + h.WriteOverhead)
	index := copies * h.IndexBytes
	d := Amounts{
		CPU:  (w.ReadsPerSec + w.WritesPerSec) / h.OpsPerVCPU,
		IOPS: w.WritesPerSec * float64(w.RF) * h.DefragMult,
	}
	if w.Engine == Memory {
		d.RAM = (data + index) * g
		return d
	}
	d.Storage = data * g
	d.RAM = index * g
	d.IOPS += w.ReadsPerSec * (1 - h.CacheHit)
	return d
}

// Layout is how the nodes are spread.
type Layout struct {
	RF    int `json:"rf"`
	AZs   int `json:"azs"`   // availability zones the nodes spread over (1 when not modelled)
	Racks int `json:"racks"` // rack-aware racks, one per AZ (1 without rack awareness)
}

// MinNodes is the topology floor.
func (l Layout) MinNodes() int { return l.RF + 1 }

// MaxNodes bounds a node count: beyond, the node shape does not suit the workload.
const MaxNodes = 10000

// Sizing is the node count a node shape calls for.
type Sizing struct {
	Nodes   int                `json:"nodes"` // 0 when no sensible node count works
	Binding Constraint         `json:"binding"`
	Rounded bool               `json:"rounded"` // nodes added to fill every rack equally
	Need    map[Constraint]int `json:"need"`    // nodes each resource needs, spare node included
	// Utilization is the share of the capacity (Hypotheses.Capacity) used at peak with every node up.
	Utilization map[Constraint]float64 `json:"utilization"`
}

// Size computes the nodes of one shape the demand needs: each resource of a node is used up to its
// ceiling, plus a spare node with NodeLoss, never fewer than RF+1, rounded up to fill every rack.
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
		if h.NodeLoss {
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
