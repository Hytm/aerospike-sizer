package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/plan"
)

// formBound finds a bounded field of the form: its path, its scale and its min and max.
var formBound = regexp.MustCompile(`data-path="([a-z_.]+)"(?: data-scale="(\d+)")? min="([^"]+)"(?: max="([^"]+)")?`)

// uxFloors are the form's own floors, above the server's: a record has bytes, a node has
// resources. Every other bound of the form is the server's, so that a value typed is sized as the
// API sizes it (a 7 % storage fill was sized at 10 % by the page, at 7 % by the API and Wellness).
var uxFloors = map[string]bool{"workload.object_size": true, "node.vcpu": true, "node.ram_gib": true, "node.storage_tb": true, "node.iops": true}

func TestFormBoundsAreTheServers(t *testing.T) {
	srv := newTestServer(t)
	_, page := get(t, srv.URL+"/")
	matches := formBound.FindAllStringSubmatch(page, -1)
	if len(matches) < 20 {
		t.Fatalf("%d bounded fields found", len(matches))
	}
	for _, m := range matches {
		path, scale := m[1], 1.0
		if uxFloors[path] {
			continue
		}
		if m[2] != "" {
			scale, _ = strconv.ParseFloat(m[2], 64)
		}
		for i, side := range []float64{-1, 1} {
			bound := m[3+i]
			if bound == "" {
				continue
			}
			b, _ := strconv.ParseFloat(bound, 64)
			used := b / scale
			c, ok := clampOf(t, srv.URL, path, used+side*0.5/scale)
			if !ok || c.Used != used {
				t.Errorf("%s: the form bounds it at %v, the server clamps %v to %+v", path, bound, used+side*0.5/scale, c)
			}
			if _, ok := clampOf(t, srv.URL, path, used); ok {
				t.Errorf("%s: %v, the form's bound, is clamped by the server", path, used)
			}
		}
	}
}

// clampOf posts the default form with one field set and returns its clamp, if any.
func clampOf(t *testing.T, base, path string, v float64) (plan.ClampNote, bool) {
	t.Helper()
	section, field, _ := strings.Cut(path, ".")
	resp, body := post(t, base+"/api/plan", "application/json", fmt.Sprintf(`{%q:{%q:%v},"auto":[]}`, section, field, v))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s=%v: status %d: %s", path, v, resp.StatusCode, body)
	}
	var r plan.Response
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Clamps {
		if c.Field == path {
			return c, true
		}
	}
	return plan.ClampNote{}, false
}

// Wellness listens on any loopback address and names its page by the host the browser used: every
// one of them gets the link back, nothing else does.
func TestLocalBack(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1:7300/a/Acme/x?growth=40": "http://127.0.0.1:7300/a/Acme/x?growth=40",
		"http://127.0.0.2:7300/a":                  "http://127.0.0.2:7300/a",
		"http://[::1]:7300/a":                      "http://[::1]:7300/a",
		"http://localhost:7300/":                   "http://localhost:7300/",
		"http://LocalHost:7300/":                   "http://LocalHost:7300/",
		"http://[::ffff:127.0.0.1]:7300/":          "http://[::ffff:127.0.0.1]:7300/",
		"https://127.0.0.1:7300/":                  "",
		"http://example.com/":                      "",
		"http://127.0.0.1.example.com/":            "",
		"http://user@127.0.0.1:7300/":              "",
		"javascript:alert(1)":                      "",
		"//127.0.0.1/x":                            "",
		"":                                         "",
	} {
		if got := localBack(raw); got != want {
			t.Errorf("localBack(%q) = %q, want %q", raw, got, want)
		}
	}
	srv := newTestServer(t)
	if _, body := get(t, srv.URL+"/?workload.rf=3&back="+"http://%5B::1%5D:7300/a/x"); !strings.Contains(html.UnescapeString(body), `data-back="http://[::1]:7300/a/x"`) {
		t.Errorf("the page does not carry the way back")
	}
	if _, body := get(t, srv.URL+"/?back=http://example.com/"); !strings.Contains(body, `data-back=""`) {
		t.Errorf("the page carries a way back that is not local")
	}
}
