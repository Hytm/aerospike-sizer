package plan

import (
	"bufio"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

type pick struct {
	instance string
	nodes    int
}

// The form opens on demand in each provider's default region, with the picks of decision T19; on
// the one-year commitment basis in US East, as aerospike-wellness asks, every row is priced at the
// reference one-year rate, never pending (T21).
func TestPricingChoosesTheRegionAndTheBasis(t *testing.T) {
	want := map[string]pick{"aws": {"i8g.xlarge", 12}, "gcp": {"c3-standard-22-lssd", 3}, "azure": {"L8as_v3", 9}}
	def := Compute(DefaultRequest(), referenceQuoter{})
	if def.Pricing.Basis != catalog.OnDemand || len(def.Rows) != 3 {
		t.Fatalf("default: pricing %+v, %d rows", def.Pricing, len(def.Rows))
	}
	for _, r := range def.Rows {
		p, _ := catalog.Get(r.Provider)
		if r.Region.ID != p.DefaultRegion || def.Pricing.Regions[r.Provider] != p.DefaultRegion || r.Price.Basis != catalog.OnDemand ||
			want[r.Provider] != (pick{r.Instance.Name, r.Nodes}) {
			t.Errorf("default %s: %s %s × %d (%s)", r.Provider, r.Region.ID, r.Instance.Name, r.Nodes, r.Price.Basis)
		}
	}

	req := DefaultRequest()
	req.Pricing = Pricing{Basis: catalog.Commitment1Y, Regions: catalog.PricesRegion}
	resp := Compute(req, referenceQuoter{pending: true})
	if resp.Pending || resp.Pricing.Basis != catalog.Commitment1Y || len(resp.Notes) != 0 || len(resp.Rows) != 3 {
		t.Fatalf("commitment: pending %v, pricing %+v, notes %q, %d rows", resp.Pending, resp.Pricing, resp.Notes, len(resp.Rows))
	}
	// The same types: $1,949, $1,757 and $2,623 of compute a month.
	compute := map[string]float64{"aws": 1949, "gcp": 1757, "azure": 2623}
	for _, r := range resp.Rows {
		q := r.Price
		if r.Region.ID != catalog.PricesRegion[r.Provider] || want[r.Provider] != (pick{r.Instance.Name, r.Nodes}) ||
			q != (pricing.Quote{Hourly: r.Instance.Reserved1YHour, Source: pricing.Reference, Basis: catalog.Commitment1Y}) ||
			!near(r.Cost.Compute, q.Hourly*float64(r.Nodes)*catalog.HoursPerMonth, 1e-6) || !near(r.Cost.Compute, compute[r.Provider], 0.5) {
			t.Errorf("commitment %s: %s %s × %d, %+v, compute %v", r.Provider, r.Region.ID, r.Instance.Name, r.Nodes, q, r.Cost.Compute)
		}
	}
}

// A configuration the user added keeps its own region, and the basis prices it and its alternative.
func TestPricingBasisPricesAddedRowsInTheirRegion(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	req.Pricing.Basis = catalog.Commitment1Y
	req.Rows = []RowRequest{{ID: "tight", Provider: "aws", Region: "eu-west-3", Instance: "i4i.2xlarge", Nodes: 3}}
	resp := Compute(req, referenceQuoter{})
	if len(resp.Rows) != 1 {
		t.Fatalf("rows %+v", resp.Rows)
	}
	r := resp.Rows[0]
	if r.Region.ID != "eu-west-3" || r.Price.Basis != catalog.Commitment1Y || r.Price.Hourly != r.Instance.Reserved1YHour || r.Alternative == nil ||
		r.Alternative.Price.Basis != catalog.Commitment1Y || r.Alternative.Price.Hourly != r.Alternative.Instance.Reserved1YHour {
		t.Errorf("row %+v, alternative %+v", r, r.Alternative)
	}
}

func TestUnknownPricingFallsBackWithANote(t *testing.T) {
	req := DefaultRequest()
	req.Pricing = Pricing{Basis: "reserved", Regions: map[string]string{"aws": "mars-1", "gcp": "us-east1"}}
	resp := Compute(req, referenceQuoter{})
	wantNotes := []string{`Unknown pricing basis "reserved": on-demand prices used.`, `AWS: unknown region "mars-1" for the recommendation, eu-west-3 · Paris used instead.`}
	if !slices.Equal(resp.Notes, wantNotes) || resp.Pricing.Basis != catalog.OnDemand ||
		resp.Pricing.Regions["aws"] != "eu-west-3" || resp.Pricing.Regions["gcp"] != "us-east1" || resp.Pricing.Regions["azure"] != "francecentral" {
		t.Errorf("notes %q, pricing %+v", resp.Notes, resp.Pricing)
	}
	for _, r := range resp.Rows {
		if r.Region.ID != resp.Pricing.Regions[r.Provider] || r.Price.Basis != catalog.OnDemand {
			t.Errorf("%s: region %s, basis %s", r.Provider, r.Region.ID, r.Price.Basis)
		}
	}
}

// A request decodes into DefaultRequest's map of regions: each call gives a new one, so that the
// regions of a request never reach the next.
func TestDefaultPricingIsNewOnEveryCall(t *testing.T) {
	req := DefaultRequest()
	if err := json.Unmarshal([]byte(`{"pricing":{"regions":{"aws":"us-east-1"}}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Pricing.Regions["aws"] != "us-east-1" || req.Pricing.Regions["gcp"] != "europe-west1" {
		t.Errorf("decoded regions %v: a region left out keeps its default", req.Pricing.Regions)
	}
	if got := DefaultRequest().Pricing.Regions["aws"]; got != "eu-west-3" {
		t.Errorf("the next default request recommends AWS in %s", got)
	}
}

// The requests aerospike-wellness sends for six synthetic clusters get the configurations its
// machine comparison recommends: its TestOptionsFollowTheSizersRecommendation holds the same table
// (decision T21), and both change together.
func TestWellnessRequestsGetWellnessPicks(t *testing.T) {
	want := map[string]map[string]pick{
		"device-storage": {"aws": {"i4i.2xlarge", 9}, "gcp": {"c3-standard-8-lssd", 18}, "azure": {"L8as_v3", 9}},
		"memory-ram":     {"aws": {"r6id.4xlarge", 14}, "gcp": {"c4a-highmem-32-lssd", 8}, "azure": {"L16as_v3", 14}},
		"cpu-3-racks":    {"aws": {"i8g.16xlarge", 6}, "gcp": {"c3-standard-22-lssd", 15}, "azure": {"L32as_v3", 12}},
		"rf3-rack-loss":  {"aws": {"i8g.2xlarge", 15}, "gcp": {"z3-highmem-16-highlssd", 6}, "azure": {"L16as_v3", 9}},
		"floor":          {"aws": {"r6id.large", 3}, "gcp": {"c3-standard-4-lssd", 3}, "azure": {"L8as_v3", 3}},
		"huge":           {"aws": {"i7ie.48xlarge", 52}, "gcp": {"z3-highmem-88-highlssd", 160}, "azure": {"L80as_v3", 320}},
	}
	f, err := os.Open("testdata/wellness-requests.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in := bufio.NewScanner(f)
	seen := map[string]bool{}
	for in.Scan() {
		var v struct {
			Key     string          `json:"key"`
			Request json.RawMessage `json:"request"`
		}
		req := DefaultRequest()
		if err := json.Unmarshal(in.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(v.Request, &req); err != nil {
			t.Fatalf("%s: %v", v.Key, err)
		}
		seen[v.Key] = true
		resp := Compute(req, referenceQuoter{})
		if len(resp.Rows) != 3 || len(resp.Notes) != 0 || resp.Pricing.Basis != catalog.Commitment1Y {
			t.Errorf("%s: %d rows, notes %q, pricing %+v", v.Key, len(resp.Rows), resp.Notes, resp.Pricing)
		}
		for _, r := range resp.Rows {
			if got := (pick{r.Instance.Name, r.Nodes}); got != want[v.Key][r.Provider] || r.Fit.Status != sizing.OK {
				t.Errorf("%s %s: %v (%s), want %v", v.Key, r.Provider, got, r.Fit.Status, want[v.Key][r.Provider])
			}
		}
	}
	if err := in.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(want) {
		t.Errorf("%d vectors, want %d", len(seen), len(want))
	}
}
