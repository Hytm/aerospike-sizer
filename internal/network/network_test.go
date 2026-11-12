package network

import (
	"math"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

// 1 000 writes/s and 2 000 reads/s of 1 000-byte records keep the arithmetic readable.
func workload(rf int) sizing.Workload {
	return sizing.Workload{ObjectSize: 1000, Objects: 1e6, ReadsPerSec: 2000, WritesPerSec: 1000, RF: rf, Engine: sizing.Device}
}

func settings(edit func(*Settings)) Settings {
	s := DefaultSettings()
	edit(&s)
	return s.Normalize()
}

func near(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

func TestCrossAZTraffic(t *testing.T) {
	cases := []struct {
		name        string
		rf          int
		edit        func(*Settings)
		write, read float64
	}{
		{"single AZ", 2, func(s *Settings) { s.AZs = 1 }, 0, 0},
		// Hash placement: the master hop and each replica cross with probability 2/3.
		{"no rack-awareness", 2, func(s *Settings) { s.RackAware = false }, 1e6 * (2.0/3 + 2.0/3), 2e6 * 2.0 / 3},
		// RF2 over 3 racks: Active Rack does not engage (3 AZs > RF2), Local read hits 2/3 of the time.
		{"rack-aware RF2 3 AZ", 2, func(*Settings) {}, 1e6 * (2.0/3 + 1), 2e6 * (1 - 2.0/3)},
		// Clients in one AZ without Active Rack: the master is still hash-placed.
		{"rack-aware, single clients", 2, func(s *Settings) { s.ClientPlacement = Single }, 1e6 * (2.0/3 + 1), 2e6 * (1 - 2.0/3)},
		// RF3 over 2 racks: one replica crosses, the extra copy lands on a random node, in the other AZ half the time.
		{"rack-aware RF3 2 AZ", 3, func(s *Settings) { s.AZs = 2 }, 1e6 * (0.5 + 1 + 0.5), 0},
		{"rack-aware RF4 2 AZ", 4, func(s *Settings) { s.AZs = 2 }, 1e6 * (0.5 + 1 + 2*0.5), 0},
		{"active rack co-located", 3, func(s *Settings) { s.ActiveRack = true; s.ClientPlacement = Single }, 1e6 * 2, 2e6 * 0.03},
		{"active rack, distributed clients", 3, func(s *Settings) { s.ActiveRack = true; s.LocalRead = false }, 1e6 * (2.0/3 + 2), 2e6 * 2.0 / 3},
	}
	for _, c := range cases {
		tr := Estimate(settings(c.edit), workload(c.rf))
		if !near(tr.BytesPerSec["write"], c.write) || !near(tr.BytesPerSec["read"], c.read) {
			t.Errorf("%s: write %v read %v, want %v %v", c.name, tr.BytesPerSec["write"], tr.BytesPerSec["read"], c.write, c.read)
		}
		if tr.Clusters != 1 || tr.Note == "" {
			t.Errorf("%s: clusters %d, note %q", c.name, tr.Clusters, tr.Note)
		}
	}
	if tr := Estimate(settings(func(s *Settings) { s.AZs = 2 }), workload(4)); !strings.Contains(tr.Note, "2 extra copies to random nodes, in another AZ 50% of the time") {
		t.Errorf("note = %q", tr.Note)
	}
}

func TestActiveRackNeedsAFullCopyPerRack(t *testing.T) {
	tr := Estimate(settings(func(s *Settings) { s.ActiveRack = true; s.ClientPlacement = Single }), workload(2))
	if len(tr.Warnings) != 1 || !strings.Contains(tr.Warnings[0], "computed without it") {
		t.Errorf("warnings = %q", tr.Warnings)
	}
	// Not engaged: the client→master hop still crosses AZs.
	if want := 1e6 * (2.0/3 + 1); !near(tr.BytesPerSec["write"], want) {
		t.Errorf("write = %v, want %v", tr.BytesPerSec["write"], want)
	}
}

func TestWarnings(t *testing.T) {
	cases := []struct {
		name string
		rf   int
		edit func(*Settings)
		want []string
	}{
		{"single AZ", 2, func(s *Settings) { s.AZs = 1 }, []string{"Single-AZ topology"}},
		{"no rack-awareness", 2, func(s *Settings) { s.RackAware = false }, []string{"every copy of about one record in 3 in the same AZ"}},
		{"no rack-awareness RF3", 3, func(s *Settings) { s.RackAware = false }, []string{"one record in 9"}},
		{"no rack-awareness RF1", 1, func(s *Settings) { s.RackAware = false }, nil},
		{"RF3 over 2 racks", 3, func(s *Settings) { s.AZs = 2 }, []string{"RF3 over 2 AZs: an AZ holds two copies"}},
		{"RF3 over 2 racks with Active Rack", 3, func(s *Settings) { s.AZs = 2; s.ActiveRack = true }, []string{"RF3 over 2 AZs"}},
		{"RF3 over 2 AZs without racks", 3, func(s *Settings) { s.AZs = 2; s.RackAware = false }, []string{"one record in 4", "RF3 over 2 AZs"}},
		{"default", 2, func(*Settings) {}, nil},
	}
	for _, c := range cases {
		got := Estimate(settings(c.edit), workload(c.rf)).Warnings
		if len(got) != len(c.want) {
			t.Errorf("%s: warnings = %q, want %d", c.name, got, len(c.want))
			continue
		}
		for i, w := range c.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: warning %d = %q, want %q", c.name, i, got[i], w)
			}
		}
	}
}

func TestMonthlyCost(t *testing.T) {
	tr := Estimate(settings(func(s *Settings) { s.AZs = 2; s.LocalRead = false; s.ActiveRack = false }), workload(2))
	// write: 1e6 × (0.5 + 1), read: 2e6 × 0.5 → 2.5 MB/s over 730 h.
	gb := 2.5e6 * 730 * 3600 / 1e9
	if !near(tr.MonthlyGB, gb) || !near(tr.Costs["aws"], gb*0.02) || !near(tr.Costs["gcp"], gb*0.01) || tr.Costs["azure"] != 0 {
		t.Errorf("monthly %v GB, costs %v", tr.MonthlyGB, tr.Costs)
	}
}

func TestXDR(t *testing.T) {
	tr := Estimate(settings(func(s *Settings) { s.Mode = MultiRegion; s.Regions = 3 }), workload(2))
	if !near(tr.BytesPerSec["xdr"], 1e6*2*0.5) || tr.Clusters != 3 || len(tr.Warnings) != 0 {
		t.Errorf("mesh: %v, clusters %d, warnings %q", tr.BytesPerSec, tr.Clusters, tr.Warnings)
	}
	// Inter-region traffic is priced with the region rates.
	if gb := 1e6 * 730 * 3600 / 1e9; !near(tr.Costs["gcp"], gb*0.02) || !near(tr.Costs["azure"], gb*0.02) {
		t.Errorf("costs = %v", tr.Costs)
	}
	// The hub ships every write to each spoke: as many bytes as the mesh.
	tr = Estimate(settings(func(s *Settings) { s.Mode = MultiRegion; s.Regions = 3; s.XDRTopology = Hub; s.XDRCompression = false }), workload(2))
	if !near(tr.BytesPerSec["xdr"], 2e6) || !strings.Contains(tr.Note, "hub and spoke") {
		t.Errorf("hub: %v, %q", tr.BytesPerSec, tr.Note)
	}
	tr = Estimate(settings(func(s *Settings) { s.Mode = MultiRegion; s.Regions = 2; s.XDRTopology = Hub }), workload(2))
	if !near(tr.BytesPerSec["xdr"], 1e6*0.5) || !strings.Contains(tr.Note, "each sized for the reads/s and writes/s entered") {
		t.Errorf("hub with compression: %v, %q", tr.BytesPerSec, tr.Note)
	}
}

func TestNormalizeDropsWhatPrerequisitesRuleOut(t *testing.T) {
	s := settings(func(s *Settings) {
		s.AZs = 1
		s.Rates = map[string]float64{"aws": -1, "bogus": 3}
		s.RegionRates = map[string]float64{"gcp": 0.05}
	})
	if s.RackAware || s.ActiveRack || s.LocalRead {
		t.Errorf("single AZ keeps rack options: %+v", s)
	}
	if s.Rates["aws"] != 0 || s.Rates["gcp"] != 0.01 || s.Rates["azure"] != 0 || len(s.Rates) != 3 {
		t.Errorf("rates = %v", s.Rates)
	}
	if s.RegionRates["gcp"] != 0.05 || s.RegionRates["aws"] != 0.02 || len(s.RegionRates) != 3 {
		t.Errorf("region rates = %v", s.RegionRates)
	}
	if s := settings(func(s *Settings) { s.AZs = 40; s.Regions = 1 }); s.AZs != MaxAZs || s.Regions != 2 {
		t.Errorf("clamped up: %+v", s)
	}
	if s := settings(func(s *Settings) { s.AZs = 0; s.Regions = 9 }); s.AZs != 1 || s.RackAware || s.Regions != MaxRegions {
		t.Errorf("clamped down: %+v", s)
	}
	if l := settings(func(*Settings) {}).Layout(2); l.Racks != 3 || l.AZs != 3 {
		t.Errorf("layout = %+v", l)
	}
	if l := settings(func(s *Settings) { s.Mode = MultiRegion }).Layout(2); l.Racks != 1 || l.AZs != 1 {
		t.Errorf("XDR layout = %+v", l)
	}
}
