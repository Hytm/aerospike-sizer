package service

import (
	"encoding/json"
	"errors"
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
	mu            sync.Mutex
	h             Health
	down          int             // /health answers garbage that many times, then normally
	loaded        map[string]bool // slots launchctl print knows
	printOut      string          // what launchctl print lists for a loaded slot
	calls         []string        // the bootstrap and bootout calls
	flip          *Health         // health after a bootstrap, nil to simulate a broken release
	failBootstrap bool
}

func (f *fakeService) Run(args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	slot := strings.TrimPrefix(filepath.Base(args[len(args)-1]), "fr.sizer.app.")
	slot = strings.TrimSuffix(slot, ".plist")
	switch args[0] {
	case "print":
		if !f.loaded[slot] {
			return "Could not find service", errors.New("exit status 113")
		}
		return f.printOut, nil
	case "bootstrap":
		f.calls = append(f.calls, "bootstrap "+filepath.Base(args[len(args)-1]))
		if f.failBootstrap {
			return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
		}
		if f.flip != nil {
			f.h = *f.flip
		}
		f.loaded[slot] = true
	case "bootout":
		f.calls = append(f.calls, "bootout "+filepath.Base(args[len(args)-1]))
		delete(f.loaded, slot)
	}
	return "", nil
}

func (f *fakeService) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down > 0 {
		f.down--
		io.WriteString(w, "starting")
		return
	}
	json.NewEncoder(w).Encode(f.h)
}

func manager(t *testing.T, f *fakeService) (*Manager, string) {
	t.Helper()
	if f.loaded == nil {
		f.loaded = map[string]bool{}
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	src := filepath.Join(dir, "sizer-src")
	os.WriteFile(src, []byte("binary"), 0o755)
	m := &Manager{
		StateDir: filepath.Join(dir, "state"), AgentsDir: filepath.Join(dir, "agents"), LogDir: filepath.Join(dir, "logs"),
		Addr: strings.TrimPrefix(srv.URL, "http://"), Env: map[string]string{"SIZER_NOTE": "/tmp/p & co"}, Version: "v2",
		Launchctl: f, HTTP: srv.Client(), Out: io.Discard, Wait: 2 * time.Second, ProbeInterval: time.Millisecond,
	}
	os.MkdirAll(m.AgentsDir, 0o755)
	return m, src
}

func calls(f *fakeService) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "|")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestDeployHandsOverToTheFreeSlot(t *testing.T) {
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "v1", Slot: "a"}, flip: &Health{Status: "ok", PID: 200, Version: "v2", Slot: "b"}, loaded: map[string]bool{"a": true}}
	f.down = 1 // one probe misses: the service is still in service
	m, src := manager(t, f)
	os.WriteFile(m.plistPath("a"), []byte("old"), 0o644)

	if err := m.Deploy(src); err != nil {
		t.Fatal(err)
	}
	if want := "bootout fr.sizer.app.b|bootstrap fr.sizer.app.b.plist|bootout fr.sizer.app.a"; calls(f) != want {
		t.Errorf("launchctl calls = %v, want %v", calls(f), want)
	}
	if b, err := os.ReadFile(m.binary("b")); err != nil || string(b) != "binary" {
		t.Errorf("binary not installed in slot b: %v", err)
	}
	if exists(m.plistPath("a")) {
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
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "v1", Slot: "b"}, loaded: map[string]bool{"b": true}}
	m, src := manager(t, f)
	m.Wait = 500 * time.Millisecond

	if err := m.Deploy(src); err == nil {
		t.Fatal("deploy must fail when the new instance never answers")
	}
	if want := "bootout fr.sizer.app.a|bootstrap fr.sizer.app.a.plist|bootout fr.sizer.app.a"; calls(f) != want {
		t.Errorf("launchctl calls = %v, want %v", calls(f), want)
	}
	if exists(m.plistPath("a")) {
		t.Error("the failed slot's plist must be removed")
	}
}

func TestDeployDiscardsASlotThatFailsToBootstrap(t *testing.T) {
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "v1", Slot: "b"}, loaded: map[string]bool{"b": true}, failBootstrap: true}
	m, src := manager(t, f)
	if err := m.Deploy(src); err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("err = %v", err)
	}
	if want := "bootout fr.sizer.app.a|bootstrap fr.sizer.app.a.plist|bootout fr.sizer.app.a"; calls(f) != want {
		t.Errorf("launchctl calls = %v, want %v", calls(f), want)
	}
	if exists(m.plistPath("a")) {
		t.Error("the plist of the slot that failed to bootstrap must be removed")
	}
}

func TestDeployRefusesAManualServerOnThePort(t *testing.T) {
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "dev"}}
	m, src := manager(t, f)
	if err := m.Deploy(src); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("err = %v", err)
	}
}

// A slot launchd runs that does not answer is not free: overwriting it would kill the service
// and leave nothing to roll back to.
func TestDeployRefusesALoadedSlotThatDoesNotAnswer(t *testing.T) {
	f := &fakeService{down: 100, loaded: map[string]bool{"a": true}}
	m, src := manager(t, f)
	os.WriteFile(m.plistPath("a"), []byte("old"), 0o644)
	if err := m.Deploy(src); err == nil || !strings.Contains(err.Error(), "slot a is loaded but does not answer") {
		t.Errorf("err = %v", err)
	}
	if calls(f) != "" || !exists(m.plistPath("a")) || exists(m.binary("a")) {
		t.Errorf("calls %q, plist a kept %v, binary a installed %v", calls(f), exists(m.plistPath("a")), exists(m.binary("a")))
	}
}

// Nothing runs and nothing answers: slot a is installed, and the plists an interrupted deploy left
// are removed so that only the new instance starts at the next login.
func TestDeployInstallsWhenNothingRuns(t *testing.T) {
	f := &fakeService{down: 100, flip: &Health{Status: "ok", PID: 300, Version: "v2", Slot: "a"}}
	m, src := manager(t, f)
	os.WriteFile(m.plistPath("b"), []byte("stale"), 0o644)
	f.mu.Lock()
	f.down = probes // the probes miss, then the bootstrapped instance answers
	f.mu.Unlock()
	if err := m.Deploy(src); err != nil {
		t.Fatal(err)
	}
	if want := "bootout fr.sizer.app.a|bootstrap fr.sizer.app.a.plist"; calls(f) != want {
		t.Errorf("launchctl calls = %v, want %v", calls(f), want)
	}
	if exists(m.plistPath("b")) || !exists(m.plistPath("a")) {
		t.Errorf("plists: a %v, b %v", exists(m.plistPath("a")), exists(m.plistPath("b")))
	}
}

// A launchctl print listing: the slot's state and pid sit at the top level; the coalition blocks
// carry states of their own that must not be picked.
const launchctlPrint = "fr.sizer.app.a = {\n\tactive count = 1\n\tpath = /Users/me/Library/LaunchAgents/fr.sizer.app.a.plist\n\ttype = LaunchAgent\n\tstate = running\n\n\tprogram = /Users/me/Library/Application Support/sizer/a/sizer\n\tpid = 100\n\n\tresource coalition = {\n\t\tstate = active\n\t}\n\tjetsam coalition = {\n\t\tstate = active\n\t}\n}\n"

func TestStatusAndUninstall(t *testing.T) {
	f := &fakeService{h: Health{Status: "ok", PID: 100, Version: "v1", Slot: "a"}, loaded: map[string]bool{"a": true}, printOut: launchctlPrint}
	m, _ := manager(t, f)
	os.WriteFile(m.plistPath("a"), []byte("plist"), 0o644)
	status := m.Status()
	for _, want := range []string{"in service: slot a, pid 100, version v1, http://" + m.Addr + "\n", "slot a (fr.sizer.app.a): loaded, state = running, pid = 100\n", "slot b (fr.sizer.app.b): not installed\n", "logs: " + m.LogDir} {
		if !strings.Contains(status, want) {
			t.Errorf("status lacks %q:\n%s", want, status)
		}
	}
	m.Uninstall()
	if want := "bootout fr.sizer.app.a|bootout fr.sizer.app.b"; calls(f) != want || exists(m.plistPath("a")) {
		t.Errorf("uninstall: calls %v, plist a kept %v", calls(f), exists(m.plistPath("a")))
	}
	if status := m.Status(); !strings.Contains(status, "slot a (fr.sizer.app.a): not installed") {
		t.Errorf("status after uninstall:\n%s", status)
	}
}
