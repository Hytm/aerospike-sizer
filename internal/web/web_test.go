package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/plan"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
	"github.com/Hytm/aerospike-sizer/internal/service"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

func newServer(prices *pricing.Book) *Server {
	return &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Slot: "b", Prices: prices}
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newServer(&pricing.Book{}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func post(t *testing.T, url, contentType, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func TestHealthTellsWhoAnswers(t *testing.T) {
	srv := newTestServer(t)
	resp, body := get(t, srv.URL+"/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var h service.Health
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatal(err)
	}
	if h.Status != "ok" || h.PID != os.Getpid() || h.Slot != "b" {
		t.Errorf("health = %+v", h)
	}
}

func TestPagesAndAssets(t *testing.T) {
	srv := newTestServer(t)
	resp, body := get(t, srv.URL+"/")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Aerospike Sizer") || !strings.Contains(body, "/static/app.js?v="+assetsVersion) {
		t.Errorf("home: status %d", resp.StatusCode)
	}
	// The workload form edits the share of reads that find a record, in percent, bounded like the API.
	if want := `data-path="workload.reads_found" data-scale="100" min="0" max="100"`; !strings.Contains(body, want) {
		t.Errorf("home: no %s input", want)
	}
	// The primary index takes any decimal, as wellness sends its effective bytes per record copy.
	if want := `<span class="unit">B per record copy</span><input type="number" data-path="hypotheses.index_bytes" min="0" step="any">`; !strings.Contains(body, want) {
		t.Errorf("home: no %s input", want)
	}
	// The comparison chooses its pricing basis; the regions of the recommendations come from the catalog.
	if want := `data-path="pricing.basis"><button type="button" data-v="on_demand">On demand</button><button type="button" data-v="commitment_1y">1-year commitment</button></div>`; !strings.Contains(body, want) || !strings.Contains(body, `id="pricing"`) {
		t.Errorf("home: no pricing basis %s", want)
	}
	// The notes of the inputs the server bounded have their place under the prefill notice.
	if !strings.Contains(body, `<div id="clamps"></div>`) {
		t.Errorf("home: no place for the clamp notes")
	}
	for _, path := range []string{"/static/app.js", "/static/app.css"} {
		if resp, _ := get(t, srv.URL+path); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", path, resp.StatusCode)
		}
	}
	if resp, _ := get(t, srv.URL+"/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown page: status %d", resp.StatusCode)
	}
}

type fakeFetcher struct{}

func (fakeFetcher) Fetch(context.Context, string, string) (float64, error) { return 1, nil }

func TestDefaults(t *testing.T) {
	srv := newTestServer(t)
	_, body := get(t, srv.URL+"/api/defaults")
	var d struct {
		Request   plan.Request `json:"request"`
		Providers []struct {
			Key string `json:"key"`
		} `json:"providers"`
		Live        map[string]bool    `json:"live_prices"`
		Thresholds  map[string]float64 `json:"thresholds"`
		PricesAsOf  string             `json:"prices_as_of"`
		PricesTerms map[string]string  `json:"prices_terms"`
		RatesAsOf   string             `json:"rates_as_of"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if d.Request.Workload.RF != 2 || len(d.Providers) != 3 || len(d.Request.Auto) != 3 || d.Thresholds["tight"] != sizing.TightLoad || d.Thresholds["tolerance"] != sizing.RatioTolerance || d.PricesAsOf == "" || d.RatesAsOf == "" {
		t.Errorf("defaults = %+v", d)
	}
	// The form opens on demand, each provider's recommendation in its default region, and names the
	// one-year terms of every provider for the other basis (T21).
	if p := d.Request.Pricing; p.Basis != catalog.OnDemand || !maps.Equal(p.Regions, plan.DefaultPricing().Regions) ||
		len(d.PricesTerms) != 3 || d.PricesTerms["aws"] != catalog.PricesTerms["aws"] {
		t.Errorf("pricing %+v, terms %v", p, d.PricesTerms)
	}
	// The form opens with every read finding its record; a number, so that the URL can prefill it.
	if f := d.Request.Workload.ReadsFound; f == nil || *f != 1 {
		t.Errorf("default share of reads found = %v, want 1", f)
	}
	// An offline book looks nothing up: the interface must not claim live prices.
	if d.Live["aws"] || d.Live["azure"] || d.Live["gcp"] {
		t.Errorf("live prices = %v on an offline book", d.Live)
	}
	live := httptest.NewServer(newServer(&pricing.Book{Fetchers: map[string]pricing.Fetcher{"aws": fakeFetcher{}}}).Handler())
	t.Cleanup(live.Close)
	_, body = get(t, live.URL+"/api/defaults")
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if !d.Live["aws"] || d.Live["azure"] {
		t.Errorf("live prices = %v with an AWS fetcher", d.Live)
	}
}

func TestPlanKeepsDefaultsForMissingFields(t *testing.T) {
	srv := newTestServer(t)
	resp, body := post(t, srv.URL+"/api/plan", "application/json", `{"workload":{"objects":2e9},"auto":["gcp"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var r plan.Response
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.Workload.Objects != 2e9 || r.Workload.ObjectSize != 1024 || r.Workload.ReadsPerSec != 200000 || r.Hypotheses.Growth != 0.3 || r.Settings.AZs != 3 || r.Settings.Rates["gcp"] != 0.01 {
		t.Errorf("plan echoes workload %+v, hypotheses %+v, settings %+v", r.Workload, r.Hypotheses, r.Settings)
	}
	if r.Generic.Nodes < 3 || r.Generic.Demand.Storage == 0 || len(r.Rows) != 1 || r.Rows[0].Provider != "gcp" || r.Rows[0].Price.Source != pricing.Reference {
		t.Errorf("plan = %+v", r)
	}
	// Nothing was bounded: the list is there, empty, like the notes.
	if !strings.Contains(body, `"clamps":[]`) {
		t.Errorf("plan without clamps: %s", body)
	}
	// Every text comes with its code and values (T22): the explanation, the fit checks and the
	// notes, even when there are none.
	for _, field := range []string{`"explain_messages":[{"code":"explain.`, `"check_messages":[]`, `"note_messages":[]`, `"messages":[{"code":"check.comfortable","text":"Comfortable margin on storage, RAM, CPU and IOPS."}]`} {
		if !strings.Contains(body, field) {
			t.Errorf("plan without %s: %s", field, body)
		}
	}
}

// The pricing of a request picks the basis and the region of each recommendation; a provider left
// out keeps its default region, and the next request starts from the defaults again (T21).
func TestPlanTakesThePricing(t *testing.T) {
	srv := newTestServer(t)
	plans := func(body string) plan.Response {
		t.Helper()
		resp, out := post(t, srv.URL+"/api/plan", "application/json", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, out)
		}
		var r plan.Response
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := plans(`{"auto":["gcp"],"pricing":{"basis":"commitment_1y","regions":{"gcp":"us-east1"}}}`)
	if r.Pricing.Basis != catalog.Commitment1Y || r.Pricing.Regions["gcp"] != "us-east1" || r.Pricing.Regions["aws"] != "eu-west-3" ||
		len(r.Rows) != 1 || r.Rows[0].Region.ID != "us-east1" || r.Rows[0].Price.Basis != catalog.Commitment1Y || r.Rows[0].Price.Source != pricing.Reference {
		t.Errorf("pricing %+v, rows %+v", r.Pricing, r.Rows)
	}
	r = plans(`{"auto":["gcp"]}`)
	if r.Pricing.Basis != catalog.OnDemand || r.Pricing.Regions["gcp"] != "europe-west1" || len(r.Rows) != 1 || r.Rows[0].Region.ID != "europe-west1" {
		t.Errorf("a request without pricing: %+v, rows %+v", r.Pricing, r.Rows)
	}
}

// workload.reads_found is the share of reads that find a record, a fraction: absent or null it is 1
// (older callers and links keep their figures), and it is clamped to [0, 1]. Only the device read
// blocks follow it.
func TestPlanChargesReadBlocksOnTheReadsFound(t *testing.T) {
	srv := newTestServer(t)
	// Wellness's workload as it posted it before the field existed.
	older := `"object_size":1024,"objects":5e8,"reads_per_sec":200000,"writes_per_sec":40000,"rf":2,"engine":"device","index_on":"ram","sindex_on":"ram"`
	for _, c := range []struct {
		workload       string
		found, readIOP float64
	}{
		{``, 1, 260000},
		{older, 1, 260000},
		{`"reads_found":null`, 1, 260000},
		{`"reads_found":1`, 1, 260000},
		{`"reads_found":0.37`, 0.37, 96200},
		{older + `,"reads_found":0.37`, 0.37, 96200},
		{`"reads_found":0`, 0, 0},
		{`"reads_found":-0.2`, 0, 0},
		{`"reads_found":1.5`, 1, 260000},
	} {
		resp, body := post(t, srv.URL+"/api/plan", "application/json", `{"workload":{`+c.workload+`},"auto":[]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", c.workload, resp.StatusCode, body)
		}
		var r plan.Response
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		if r.Workload.ReadsFound == nil || *r.Workload.ReadsFound != c.found || math.Abs(r.Generic.Demand.ReadIOPS-c.readIOP) > 1e-6 || math.Abs(r.Generic.Demand.TPS-312000) > 1e-6 {
			t.Errorf("%s: reads found %v, read IOPS %v (want %v), %v tx/s", c.workload, r.Workload.ReadsFound, r.Generic.Demand.ReadIOPS, c.readIOP, r.Generic.Demand.TPS)
		}
		if !strings.Contains(body, `"reads_found":`) {
			t.Errorf("%s: the answer does not echo reads_found", c.workload)
		}
	}
}

func TestAPIRejectsWhatItCannotRead(t *testing.T) {
	srv := newTestServer(t)
	// A form posted by another site arrives as text/plain: it must not be served.
	if resp, _ := post(t, srv.URL+"/api/plan", "text/plain", `{}`); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: status %d", resp.StatusCode)
	}
	if resp, _ := post(t, srv.URL+"/api/plan", "application/json", `{"workload":`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("truncated JSON: status %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/api/plan"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET plan: status %d", resp.StatusCode)
	}
}

func TestRefreshAnswersNoContent(t *testing.T) {
	srv := newTestServer(t)
	if resp, _ := post(t, srv.URL+"/api/refresh", "application/json", `{"provider":"aws","region":"eu-west-3","instance":"i4i.large"}`); resp.StatusCode != http.StatusNoContent {
		t.Errorf("refresh: status %d", resp.StatusCode)
	}
}

// A page on another domain pointed at 127.0.0.1 by DNS rebinding sends its own host: refused.
func TestForeignHostIsRefused(t *testing.T) {
	srv := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/defaults", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("foreign host: status %d", resp.StatusCode)
	}
	for _, host := range []string{"localhost:7310", "127.0.0.1", "[::1]:7310", "LOCALHOST"} {
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("host %s: status %d", host, resp.StatusCode)
		}
	}
	// The configured listen host is served too.
	s := newServer(&pricing.Book{})
	s.Addr = "sizer.lan:7310"
	rec := httptest.NewRecorder()
	req.Host = "sizer.lan:7310"
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("configured host: status %d", rec.Code)
	}
}

func TestUnencodableResponseIsAFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&pricing.Book{}).writeJSON(rec, math.NaN())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d", rec.Code)
	}
}
