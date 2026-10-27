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
		// RF3 over 2 racks: one replica crosses, the extra copy stays in the master's rack.
		{"rack-aware RF3 2 AZ", 3, func(s *Settings) { s.AZs = 2 }, 1e6 * (0.5 + 1), 0},
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

func TestMonthlyCost(t *testing.T) {
	tr := Estimate(settings(func(s *Settings) { s.AZs = 2; s.LocalRead = false; s.ActiveRack = false }), workload(2))
	// write: 1e6 × (0.5 + 1), read: 2e6 × 0.5 → 2.5 MB/s over 730 h.
	gb := 2.5e6 * 730 * 3600 / 1e9
	if !near(tr.MonthlyGB, gb) || !near(tr.Costs["aws"], gb*0.02) || !near(tr.Costs["gcp"], gb*0.01) {
		t.Errorf("monthly %v GB, costs %v", tr.MonthlyGB, tr.Costs)
	}
}

func TestXDR(t *testing.T) {
	tr := Estimate(settings(func(s *Settings) { s.Mode = MultiRegion; s.Regions = 3 }), workload(2))
	if !near(tr.BytesPerSec["xdr"], 1e6*2*0.5) || tr.Clusters != 3 {
		t.Errorf("mesh: %v, clusters %d", tr.BytesPerSec, tr.Clusters)
	}
	tr = Estimate(settings(func(s *Settings) { s.Mode = MultiRegion; s.Regions = 3; s.XDRTopology = Hub; s.XDRCompression = false }), workload(2))
	if !near(tr.BytesPerSec["xdr"], 1e6) {
		t.Errorf("hub: %v", tr.BytesPerSec)
	}
}

func TestNormalizeDropsWhatPrerequisitesRuleOut(t *testing.T) {
	s := settings(func(s *Settings) { s.AZs = 1; s.Rates = map[string]float64{"aws": -1, "bogus": 3} })
	if s.RackAware || s.ActiveRack || s.LocalRead {
		t.Errorf("single AZ keeps rack options: %+v", s)
	}
	if s.Rates["aws"] != 0 || s.Rates["gcp"] != 0.01 || len(s.Rates) != 3 {
		t.Errorf("rates = %v", s.Rates)
	}
	if l := settings(func(*Settings) {}).Layout(2); l.Racks != 3 || l.AZs != 3 {
		t.Errorf("layout = %+v", l)
	}
	if l := settings(func(s *Settings) { s.Mode = MultiRegion }).Layout(2); l.Racks != 1 || l.AZs != 1 {
		t.Errorf("XDR layout = %+v", l)
	}
}
