package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
		Live       map[string]bool    `json:"live_prices"`
		Thresholds map[string]float64 `json:"thresholds"`
		PricesAsOf string             `json:"prices_as_of"`
		RatesAsOf  string             `json:"rates_as_of"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if d.Request.Workload.RF != 2 || len(d.Providers) != 3 || len(d.Request.Auto) != 3 || d.Thresholds["tight"] != sizing.TightLoad || d.PricesAsOf == "" || d.RatesAsOf == "" {
		t.Errorf("defaults = %+v", d)
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
