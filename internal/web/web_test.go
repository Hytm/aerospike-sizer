package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/plan"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Slot: "b", Prices: &pricing.Book{}}
	srv := httptest.NewServer(s.Handler())
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
	var h struct {
		Status string `json:"status"`
		PID    int    `json:"pid"`
		Slot   string `json:"slot"`
	}
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

func TestDefaults(t *testing.T) {
	srv := newTestServer(t)
	_, body := get(t, srv.URL+"/api/defaults")
	var d struct {
		Request   plan.Request `json:"request"`
		Providers []struct {
			Key string `json:"key"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if d.Request.Workload.RF != 2 || len(d.Providers) != 3 || len(d.Request.Auto) != 3 {
		t.Errorf("defaults = %+v", d)
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
	if resp, _ := post(t, srv.URL+"/api/refresh", "application/json", `{"provider":"aws","region":"eu-west-3","instance":"i4i.large"}`); resp.StatusCode != http.StatusNoContent {
		t.Errorf("refresh: status %d", resp.StatusCode)
	}
}
