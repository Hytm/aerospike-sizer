package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeService answers /health like the slot currently in service; fakeLaunchctl switches it.
type fakeService struct {
	mu    sync.Mutex
	h     Health
	calls []string
	flip  *Health // health after a bootstrap, nil to simulate a broken release
}

func (f *fakeService) Run(args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args[:1], " ")+" "+filepath.Base(args[len(args)-1]))
	if args[0] == "bootstrap" && f.flip != nil {
		f.h = *f.flip
	}
	return "", nil
}

func (f *fakeService) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	json.NewEncoder(w).Encode(f.h)
}

func manager(t *testing.T, f *fakeService) (*Manager, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	src := filepath.Join(dir, "sizer-src")
	os.WriteFile(src, []byte("binary"), 0o755)
	m := &Manager{
		StateDir: filepath.Join(dir, "state"), AgentsDir: filepath.Join(dir, "agents"), LogDir: filepath.Join(dir, "logs"),
		Addr: strings.TrimPrefix(srv.URL, "http://"), Env: map[string]string{"SIZER_NOTE": "/tmp/p & co"}, Version: "v2",
		Launchctl: f, HTTP: srv.Client(), Out: io.Discard, Wait: 2 * time.Second,
	}
	return m, src
}

func TestDeployHandsOverToTheFreeSlot(t *testing.T) {
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "v1", Slot: "a"}, flip: &Health{Status: "ok", PID: 200, Version: "v2", Slot: "b"}}
	m, src := manager(t, f)
	os.MkdirAll(m.AgentsDir, 0o755)
	os.WriteFile(m.plistPath("a"), []byte("old"), 0o644)

	if err := m.Deploy(src); err != nil {
		t.Fatal(err)
	}
	want := []string{"bootout fr.sizer.app.b", "bootstrap fr.sizer.app.b.plist", "bootout fr.sizer.app.a"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("launchctl calls = %v, want %v", f.calls, want)
	}
	if b, err := os.ReadFile(m.binary("b")); err != nil || string(b) != "binary" {
		t.Errorf("binary not installed in slot b: %v", err)
	}
	if _, err := os.Stat(m.plistPath("a")); !os.IsNotExist(err) {
		t.Error("the retired slot's plist must be removed")
	}
	plist, _ := os.ReadFile(m.plistPath("b"))
	for _, want := range []string{"<string>fr.sizer.app.b</string>", "<key>SIZER_SHARED_PORTS</key>\n\t\t<string>1</string>",
		"<key>SuccessfulExit</key>\n\t\t<false/>", "<integer>30</integer>", "/tmp/p &amp; co", "<string>--slot</string>\n\t\t<string>b</string>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
}

func TestDeployRollsBackWhenTheNewInstanceDoesNotAnswer(t *testing.T) {
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "v1", Slot: "b"}}
	m, src := manager(t, f)
	m.Wait = 500 * time.Millisecond

	if err := m.Deploy(src); err == nil {
		t.Fatal("deploy must fail when the new instance never answers")
	}
	want := []string{"bootout fr.sizer.app.a", "bootstrap fr.sizer.app.a.plist", "bootout fr.sizer.app.a"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("launchctl calls = %v, want %v", f.calls, want)
	}
	if _, err := os.Stat(m.plistPath("a")); !os.IsNotExist(err) {
		t.Error("the failed slot's plist must be removed")
	}
}

func TestDeployRefusesAManualServerOnThePort(t *testing.T) {
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "dev"}}
	m, src := manager(t, f)
	if err := m.Deploy(src); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("err = %v", err)
	}
}
