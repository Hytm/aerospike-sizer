// Package network estimates the replication traffic that crosses billing boundaries: between the
// availability zones of one region, or between regions through XDR.
package network

import (
	"fmt"
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
	Mesh = "mesh" // active-active: every write ships to every other region
	Hub  = "hub"  // hub and spoke: every write ships once
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
	// Rates are the fully-loaded $ per GB moved, per provider (both directions when both are billed).
	Rates map[string]float64 `json:"rates"`
}

// DefaultRates are the historical cross-AZ list prices: AWS and Azure bill both directions.
func DefaultRates() map[string]float64 {
	return map[string]float64{"aws": 0.02, "gcp": 0.01, "azure": 0.02}
}

// DefaultSettings are three rack-aware AZs with Local read. Active Rack is off: it needs RF ≥ AZs.
func DefaultSettings() Settings {
	return Settings{
		Mode: MultiAZ, AZs: 3, RackAware: true, LocalRead: true, ClientPlacement: Distributed,
		ResidualRead: 0.03, Regions: 2, XDRTopology: Mesh, XDRCompression: true, CompressionGain: 0.5, Rates: DefaultRates(),
	}
}

// Normalize clamps the settings and drops the options their prerequisites rule out.
func (s Settings) Normalize() Settings {
	if s.Mode != MultiRegion {
		s.Mode = MultiAZ
	}
	s.AZs = min(max(s.AZs, 1), 6)
	s.Regions = min(max(s.Regions, 2), 8)
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
	rates := DefaultRates()
	for k, v := range s.Rates {
		if _, ok := rates[k]; ok {
			rates[k] = max(v, 0)
		}
	}
	s.Rates = rates
	return s
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

// secondsPerMonth matches the 730 hours a month of compute prices.
const secondsPerMonth = 730 * 3600

// Estimate computes the traffic of a workload over normalized settings.
func Estimate(s Settings, w sizing.Workload) Traffic {
	var t Traffic
	if s.Mode == MultiRegion {
		t = xdr(s, w)
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
		t.Costs[p.Key] = t.MonthlyGB * s.Rates[p.Key]
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

	crossReplicas := min(w.RF, s.AZs) - 1 // rack-aware: one copy per rack, extra copies stay in racks that hold one
	activeRack := s.ActiveRack && s.AZs <= w.RF
	coLocated := activeRack && s.ClientPlacement == Single // the clients' AZ is the active rack
	toMaster := remote
	if coLocated {
		toMaster = 0
	}
	write = writes * (toMaster + float64(crossReplicas))
	fmt.Fprintf(&note, "Rack-aware: each write ships its replica(s) to %d other rack(s)", crossReplicas)
	if w.RF > s.AZs {
		note.WriteString(" (copies beyond one per rack stay in a rack that already holds one)")
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

func warnings(s Settings, rf int) []string {
	var out []string
	switch {
	case s.AZs == 1:
		out = append(out, "Single-AZ topology: replicas protect against a node failure but not against an availability-zone outage.")
	case s.RackAware && s.ActiveRack && s.AZs > rf:
		out = append(out, fmt.Sprintf("Active Rack only engages when every rack holds a full copy (AZ count ≤ RF, Aerospike EE 7.2+): with %d AZs and RF%d the traffic is computed without it. Raise the RF to %d or use fewer AZs.", s.AZs, rf, s.AZs))
	}
	if s.AZs > 1 && rf > s.AZs {
		out = append(out, fmt.Sprintf("RF%d over %d AZs: a rack holds two copies of some records, so an AZ outage loses more than one copy. Align the AZ count with the RF, or lower the RF.", rf, s.AZs))
	}
	return out
}

func xdr(s Settings, w sizing.Workload) Traffic {
	factor, how := float64(s.Regions-1), "(regions − 1) [active-active mesh: each write ships to every other region]"
	if s.XDRTopology == Hub {
		factor, how = 1, "1 [hub and spoke: each write ships once, between a spoke and the hub]"
	}
	bytes := w.WritesPerSec * w.ObjectSize * factor
	note := "XDR volume = writes/s × size × " + how
	if s.XDRCompression {
		bytes *= 1 - s.CompressionGain
		note += fmt.Sprintf(" × (1 − %s compression gain)", units.Pct(s.CompressionGain))
	}
	note += fmt.Sprintf(". Compute is counted for one cluster in each of the %d regions; cross-AZ replication inside each region is not included.", s.Regions)
	return Traffic{BytesPerSec: map[string]float64{"xdr": bytes}, Clusters: s.Regions, Note: note}
}
