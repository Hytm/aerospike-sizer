// Package network estimates the replication traffic that crosses billing boundaries: between the
// availability zones of one region, or between regions through XDR.
package network

import (
	"fmt"
	"math"
	"strings"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
	"github.com/Hytm/aerospike-sizer/internal/units"
)

// Mode is the deployment the traffic is estimated for.
type Mode string

// Modes.
const (
	MultiAZ     Mode = "az"     // one region, nodes spread over availability zones
	MultiRegion Mode = "region" // one cluster per region, linked by XDR
)

// Client placements.
const (
	Distributed = "distributed" // clients in every AZ
	Single      = "single"      // clients in one AZ
)

// XDR topologies.
const (
	Mesh = "mesh" // active-active: every region ships its writes to every other region
	Hub  = "hub"  // hub and spoke: the hub ships every write to each spoke
)

// Settings describe the deployment and the rates of the moved traffic.
type Settings struct {
	Mode            Mode    `json:"mode"`
	AZs             int     `json:"azs"`
	RackAware       bool    `json:"rack_aware"`  // one rack ID per AZ
	ActiveRack      bool    `json:"active_rack"` // EE 7.2+: every master on one rack
	LocalRead       bool    `json:"local_read"`  // clients prefer the replica of their own rack
	ClientPlacement string  `json:"client_placement"`
	ResidualRead    float64 `json:"residual_read"` // share of reads still crossing AZs next to the active rack
	Regions         int     `json:"regions"`
	XDRTopology     string  `json:"xdr_topology"`
	XDRCompression  bool    `json:"xdr_compression"`
	CompressionGain float64 `json:"compression_gain"` // share of the XDR volume compression saves
	// Rates are the $ per GB moved between the AZs of a region, per provider (both directions
	// when both are billed); RegionRates the $ per GB shipped between regions by XDR (egress).
	Rates       map[string]float64 `json:"rates"`
	RegionRates map[string]float64 `json:"region_rates"`
}

// RatesAsOf is the date the default rates were read on the providers' pricing pages.
const RatesAsOf = "2026-11-11"

// DefaultRates are the list prices of traffic between the availability zones of a region: AWS
// bills $0.01 per GB in each direction, Google Cloud $0.01 per GiB on egress only (entered per
// GB), Azure nothing since May 2024.
func DefaultRates() map[string]float64 {
	return map[string]float64{"aws": 0.02, "gcp": 0.01, "azure": 0}
}

// DefaultRegionRates are the list prices of traffic between two regions of the same continent,
// billed on egress by every provider: $0.02 per GB (AWS, Azure) or per GiB (Google Cloud); other
// region pairs cost more.
func DefaultRegionRates() map[string]float64 {
	return map[string]float64{"aws": 0.02, "gcp": 0.02, "azure": 0.02}
}

// DefaultSettings are three rack-aware AZs with Local read. Active Rack is off: it needs RF ≥ AZs.
func DefaultSettings() Settings {
	return Settings{
		Mode: MultiAZ, AZs: 3, RackAware: true, LocalRead: true, ClientPlacement: Distributed,
		ResidualRead: 0.03, Regions: 2, XDRTopology: Mesh, XDRCompression: true, CompressionGain: 0.5,
		Rates: DefaultRates(), RegionRates: DefaultRegionRates(),
	}
}

// Bounds of the topology.
const (
	MaxAZs     = 12
	MaxRegions = 8
)

// Normalize clamps the settings and drops the options their prerequisites rule out.
func (s Settings) Normalize() Settings {
	if s.Mode != MultiRegion {
		s.Mode = MultiAZ
	}
	s.AZs = min(max(s.AZs, 1), MaxAZs)
	s.Regions = min(max(s.Regions, 2), MaxRegions)
	if s.ClientPlacement != Single {
		s.ClientPlacement = Distributed
	}
	if s.XDRTopology != Hub {
		s.XDRTopology = Mesh
	}
	s.ResidualRead = min(max(s.ResidualRead, 0), 1)
	s.CompressionGain = min(max(s.CompressionGain, 0), 0.9)
	if s.AZs == 1 {
		s.RackAware = false
	}
	if !s.RackAware {
		s.ActiveRack, s.LocalRead = false, false
	}
	s.Rates = normalizeRates(s.Rates, DefaultRates())
	s.RegionRates = normalizeRates(s.RegionRates, DefaultRegionRates())
	return s
}

// normalizeRates keeps the given rates of the known providers, never negative, and the defaults
// for the others.
func normalizeRates(given, defaults map[string]float64) map[string]float64 {
	for k, v := range given {
		if _, ok := defaults[k]; ok {
			defaults[k] = max(v, 0)
		}
	}
	return defaults
}

// Layout returns how the nodes of one cluster spread over racks.
func (s Settings) Layout(rf int) sizing.Layout {
	if s.Mode == MultiRegion {
		return sizing.Layout{RF: rf, AZs: 1, Racks: 1}
	}
	racks := 1
	if s.RackAware {
		racks = s.AZs
	}
	return sizing.Layout{RF: rf, AZs: s.AZs, Racks: racks}
}

// Traffic is the estimated billed traffic and its monthly cost.
type Traffic struct {
	BytesPerSec map[string]float64 `json:"bytes_per_sec"` // "write" and "read", or "xdr"
	MonthlyGB   float64            `json:"monthly_gb"`
	Costs       map[string]float64 `json:"costs"`    // $ per month, per provider
	Clusters    int                `json:"clusters"` // clusters to pay for: one per region with XDR
	Note        string             `json:"note"`
	Warnings    []string           `json:"warnings"`
}

// secondsPerMonth matches the hours a month compute is priced over.
const secondsPerMonth = catalog.HoursPerMonth * 3600

// Estimate computes the traffic of a workload over normalized settings. The workload is taken as
// given: the plan passes it grown to the planning horizon (sizing.Workload.Grown), so the traffic
// follows the growth hypothesis like the CPU and the IOPS do.
func Estimate(s Settings, w sizing.Workload) Traffic {
	var t Traffic
	rates := s.Rates
	if s.Mode == MultiRegion {
		t = xdr(s, w)
		rates = s.RegionRates
	} else {
		t = crossAZ(s, w)
	}
	total := 0.0
	for _, v := range t.BytesPerSec {
		total += v
	}
	t.MonthlyGB = total * secondsPerMonth / 1e9
	t.Costs = map[string]float64{}
	for _, p := range catalog.Providers {
		t.Costs[p.Key] = t.MonthlyGB * rates[p.Key]
	}
	return t
}

func crossAZ(s Settings, w sizing.Workload) Traffic {
	t := Traffic{Clusters: 1, Warnings: warnings(s, w.RF)}
	if s.AZs == 1 {
		t.BytesPerSec = map[string]float64{"write": 0, "read": 0}
		t.Note = "Single-AZ deployment: replicas are spread across nodes only, not zones, so replication stays on the intra-zone network. Network cost is $0, but there is no zone-level fault tolerance."
		return t
	}
	writes, reads := w.WritesPerSec*w.ObjectSize, w.ReadsPerSec*w.ObjectSize
	remote := float64(s.AZs-1) / float64(s.AZs) // chance that a hash-placed node sits in another AZ
	share := units.Pct(remote)
	var write, read float64
	var note strings.Builder
	if !s.RackAware {
		write = writes * (remote + float64(w.RF-1)*remote)
		read = reads * remote
		fmt.Fprintf(&note, "No rack-awareness: placement ignores AZ boundaries, so the client→master hop of a write, the client→node hop of a read and each of the RF−1 replica writes land in another AZ with probability (AZs−1)/AZs = %s, whatever the client placement.", share)
		t.BytesPerSec = map[string]float64{"write": write, "read": read}
		t.Note = note.String()
		return t
	}

	crossReplicas := min(w.RF, s.AZs) - 1 // rack-aware: one copy per rack
	extra := max(w.RF-s.AZs, 0)           // copies beyond one per rack: placed on random nodes
	activeRack := s.ActiveRack && s.AZs <= w.RF
	coLocated := activeRack && s.ClientPlacement == Single // the clients' AZ is the active rack
	toMaster := remote
	if coLocated {
		toMaster = 0
	}
	write = writes * (toMaster + float64(crossReplicas) + float64(extra)*remote)
	fmt.Fprintf(&note, "Rack-aware: each write ships one replica to each of the %d other rack(s)", crossReplicas)
	if extra > 0 {
		fmt.Fprintf(&note, " and its %d extra cop%s to random nodes, in another AZ %s of the time", extra, plural(extra, "y", "ies"), share)
	}
	switch {
	case coLocated:
		note.WriteString("; Active Rack pins every master on the clients' AZ, so the client→master hop stays local.")
	case activeRack:
		fmt.Fprintf(&note, "; Active Rack pins every master on one rack, which clients in other AZs reach across AZs (%s of writes).", share)
	default:
		fmt.Fprintf(&note, "; the master is hash-placed, so the client→master hop crosses AZs for %s of writes.", share)
	}
	switch {
	case coLocated:
		read = reads * s.ResidualRead
		fmt.Fprintf(&note, " Reads land on the local master; only the residual rate (%s, fallback and rebalancing) crosses AZs.", units.Pct(s.ResidualRead))
	case s.LocalRead:
		hit := min(1, float64(w.RF)/float64(s.AZs))
		read = reads * (1 - hit)
		fmt.Fprintf(&note, " Local read: a client reads the replica of its own rack when one exists, which is min(1, RF/AZs) = %s of the time, so %s of reads cross AZs.", units.Pct(hit), units.Pct(1-hit))
	default:
		read = reads * remote
		fmt.Fprintf(&note, " Local read is off: reads go to the master, %s of them in another AZ.", share)
	}
	t.BytesPerSec = map[string]float64{"write": write, "read": read}
	t.Note = note.String()
	return t
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func warnings(s Settings, rf int) []string {
	var out []string
	switch {
	case s.AZs == 1:
		out = append(out, "Single-AZ topology: replicas protect against a node failure but not against an availability-zone outage.")
	case !s.RackAware && rf > 1:
		// Hash placement puts every copy of a record in the master's AZ with probability (1/AZs)^(RF−1).
		out = append(out, fmt.Sprintf("No rack-awareness over %d AZs: hash placement puts every copy of about one record in %s in the same AZ, which an AZ outage loses. Set one rack-id per AZ.", s.AZs, units.Count(math.Pow(float64(s.AZs), float64(rf-1)))))
	case s.ActiveRack && s.AZs > rf:
		out = append(out, fmt.Sprintf("Active Rack only engages when every rack holds a full copy (AZ count ≤ RF, Aerospike EE 7.2+): with %d AZs and RF%d the traffic is computed without it. Raise the RF to %d or use fewer AZs.", s.AZs, rf, s.AZs))
	}
	if s.AZs > 1 && rf > s.AZs {
		out = append(out, fmt.Sprintf("RF%d over %d AZs: an AZ holds two copies of some records, so an AZ outage loses more than one copy. Align the AZ count with the RF, or lower the RF.", rf, s.AZs))
	}
	return out
}

func xdr(s Settings, w sizing.Workload) Traffic {
	how := "(regions − 1) [active-active mesh: every region ships its writes to every other region]"
	if s.XDRTopology == Hub {
		how = "(regions − 1) [hub and spoke: the hub ships every write to each spoke, and forwards a spoke's writes to the other spokes]"
	}
	bytes := w.WritesPerSec * w.ObjectSize * float64(s.Regions-1)
	note := "XDR volume = writes/s × size × " + how
	if s.XDRCompression {
		bytes *= 1 - s.CompressionGain
		note += fmt.Sprintf(" × (1 − %s compression gain)", units.Pct(s.CompressionGain))
	}
	note += fmt.Sprintf(". Compute is counted for one cluster in each of the %d regions, each sized for the reads/s and writes/s entered (every region receives every write; enter the reads one region serves if they differ); cross-AZ replication inside each region is not included.", s.Regions)
	return Traffic{BytesPerSec: map[string]float64{"xdr": bytes}, Clusters: s.Regions, Note: note}
}
