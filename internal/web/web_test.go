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
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Slot: "b"}
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

func TestHomeServesTheSizer(t *testing.T) {
	srv := newTestServer(t)
	resp, body := get(t, srv.URL+"/")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Aerospike Cluster Sizer") {
		t.Errorf("home: status %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown page: status %d", resp.StatusCode)
	}
}
