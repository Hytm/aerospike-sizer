package sizing

import (
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

// The basis decides the ranking: b is cheaper on demand, a with a one-year commitment (T21).
func TestRecommendRanksOnThePricingBasis(t *testing.T) {
	shape := catalog.Instance{VCPU: 16, RAMGiB: 128, Drives: 1, DriveGB: 3750, IOPS: 200000}
	a, b := shape, shape
	a.Name, a.PriceHour, a.Reserved1YHour = "a", 1.0, 0.70
	b.Name, b.PriceHour, b.Reserved1YHour = "b", 0.9, 0.75
	d, h, l := Amounts{Storage: 4e12}, DefaultHypotheses(), Layout{RF: 2, AZs: 1, Racks: 1}
	for basis, want := range map[catalog.Basis]string{catalog.OnDemand: "b", catalog.Commitment1Y: "a"} {
		if o, ok := Recommend([]catalog.Instance{a, b}, d, h, l, basis); !ok || o.Instance.Name != want {
			t.Errorf("%s: %+v, want %s", basis, o, want)
		}
		if inst, ok := Comfortable([]catalog.Instance{a, b}, d, h, l, 6, basis); !ok || inst.Name != want {
			t.Errorf("%s alternative: %s, want %s", basis, inst.Name, want)
		}
	}
}

// Ties go to fewer nodes, then to the name, whatever the order the types come in:
// aerospike-wellness lists its catalog in another order.
func TestRecommendBreaksTiesOnNodesThenName(t *testing.T) {
	x := catalog.Instance{Name: "x", PriceHour: 1}
	y := catalog.Instance{Name: "y", PriceHour: 0.375}
	var c candidate
	c.consider(y, 50, catalog.OnDemand) // 0.375 × 50 × 2 = 37.5
	c.consider(x, 25, catalog.OnDemand) // 1 × 25 × 1.5 = 37.5
	if c.Instance.Name != "x" || c.score != 37.5 {
		t.Errorf("tie on the score: %+v, want x with fewer nodes", c)
	}
	i8g := catalog.Instance{Name: "i8g.24xlarge", PriceHour: 8.2368}
	i4i := catalog.Instance{Name: "i4i.24xlarge", PriceHour: 8.2368}
	for _, order := range [][]catalog.Instance{{i8g, i4i}, {i4i, i8g}} {
		var d candidate
		for _, inst := range order {
			d.consider(inst, 3, catalog.OnDemand)
		}
		if d.Instance.Name != "i4i.24xlarge" {
			t.Errorf("tie on the score and the nodes: %s, want the first name", d.Instance.Name)
		}
	}
	if score(1, nodePenaltyPer) != 2*nodePenaltyPer || nodePenaltyPer != 50 || TightLoad != 0.85 {
		t.Error("the score doubles at 50 nodes and comfortable means every load under 85 %, as in aerospike-wellness")
	}
}

// The alternative takes the cheapest comfortable type, on a tie the name, whatever the order.
func TestComfortableBreaksTiesOnTheName(t *testing.T) {
	shape := catalog.Instance{VCPU: 16, RAMGiB: 128, Drives: 1, DriveGB: 3750, IOPS: 200000, PriceHour: 1}
	a, b := shape, shape
	a.Name, b.Name = "a", "b"
	d, h, l := Amounts{Storage: 4e12}, DefaultHypotheses(), Layout{RF: 2, AZs: 1, Racks: 1}
	for _, insts := range [][]catalog.Instance{{a, b}, {b, a}} {
		if inst, ok := Comfortable(insts, d, h, l, 6, catalog.OnDemand); !ok || inst.Name != "a" {
			t.Errorf("%s then %s: %s, want a", insts[0].Name, insts[1].Name, inst.Name)
		}
	}
}
