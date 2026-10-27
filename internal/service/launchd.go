package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Slots are the two launchd instances; one is in service at a time.
var Slots = []string{"a", "b"}

// Label returns the launchd label of a slot.
func Label(slot string) string { return "fr.sizer.app." + slot }

// DrainSeconds is how long a stopping instance finishes its requests; launchd's ExitTimeOut adds a
// margin to it.
const DrainSeconds = 20

// Health is what /health answers.
type Health struct {
	Status  string `json:"status"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
	Slot    string `json:"slot"`
}

// Launchctl runs launchctl; tests replace it.
type Launchctl interface {
	Run(args ...string) (string, error)
}

type systemLaunchctl struct{}

func (systemLaunchctl) Run(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return string(out), err
}

// Manager installs and hands over the launchd instances.
type Manager struct {
	StateDir  string            // binaries per slot
	AgentsDir string            // ~/Library/LaunchAgents
	LogDir    string            // ~/Library/Logs/sizer
	Addr      string            // listen address of the service
	Env       map[string]string // extra environment for the service
	Version   string
	Launchctl Launchctl
	HTTP      *http.Client
	Out       io.Writer
	Wait      time.Duration // how long to wait for the new instance
}

// NewManager returns a manager for the current user.
func NewManager(stateDir, addr, version string, env map[string]string) *Manager {
	home, _ := os.UserHomeDir()
	return &Manager{
		StateDir: stateDir, AgentsDir: filepath.Join(home, "Library", "LaunchAgents"), LogDir: filepath.Join(home, "Library", "Logs", "sizer"),
		Addr: addr, Env: env, Version: version, Launchctl: systemLaunchctl{},
		// A kept-alive connection would keep talking to the old instance: every check opens a new
		// connection, which macOS hands to the newest listener.
		HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}},
		Out:  os.Stdout, Wait: 30 * time.Second,
	}
}

func (m *Manager) domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func (m *Manager) plistPath(slot string) string {
	return filepath.Join(m.AgentsDir, Label(slot)+".plist")
}

func (m *Manager) binary(slot string) string { return filepath.Join(m.StateDir, slot, "sizer") }

// Health asks the running service who it is.
func (m *Manager) Health() (*Health, error) {
	resp, err := m.HTTP.Get("http://" + m.Addr + "/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Deploy installs the binary at src in the free slot, starts it next to the running instance on the
// same port, and retires the old instance once the new one answers /health. If the new instance
// does not answer, it is removed and the old one stays in service.
func (m *Manager) Deploy(src string) error {
	old, _ := m.Health()
	active := ""
	if old != nil {
		active = old.Slot
		if active == "" {
			return fmt.Errorf("a sizer server started by hand answers on %s (pid %d): stop it first", m.Addr, old.PID)
		}
	}
	free := "a"
	if active == "a" {
		free = "b"
	}
	fmt.Fprintf(m.Out, "deploying %s into slot %s (in service: %s)\n", m.Version, free, orNone(active))
	if err := m.install(src, free); err != nil {
		return err
	}
	_, _ = m.Launchctl.Run("bootout", m.domain()+"/"+Label(free))
	if out, err := m.Launchctl.Run("bootstrap", m.domain(), m.plistPath(free)); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(out))
	}
	h, err := m.waitFor(free, old)
	if err != nil {
		fmt.Fprintf(m.Out, "new instance not healthy (%v): rolling back, slot %s stays in service\n", err, orNone(active))
		_, _ = m.Launchctl.Run("bootout", m.domain()+"/"+Label(free))
		os.Remove(m.plistPath(free))
		return err
	}
	fmt.Fprintf(m.Out, "slot %s answers (pid %d, %s)\n", free, h.PID, h.Version)
	if active != "" {
		// bootout sends SIGTERM: the old instance drains its requests and exits; removing its plist
		// keeps launchd from starting it again at the next login.
		_, _ = m.Launchctl.Run("bootout", m.domain()+"/"+Label(active))
		os.Remove(m.plistPath(active))
		fmt.Fprintf(m.Out, "slot %s retired\n", active)
	}
	fmt.Fprintf(m.Out, "service: http://%s\n", m.Addr)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// install copies the binary into the slot and writes its plist.
func (m *Manager) install(src, slot string) error {
	dst := m.binary(slot)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := copyFile(src, dst+".new", 0o755); err != nil {
		return err
	}
	if err := os.Rename(dst+".new", dst); err != nil {
		return err
	}
	if err := os.MkdirAll(m.AgentsDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(m.LogDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.plistPath(slot), []byte(m.Plist(slot)), 0o644)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// waitFor polls /health until the new slot answers with a new pid.
func (m *Manager) waitFor(slot string, old *Health) (*Health, error) {
	deadline := time.Now().Add(m.Wait)
	for time.Now().Before(deadline) {
		if h, err := m.Health(); err == nil && h.Slot == slot && (old == nil || h.PID != old.PID) {
			return h, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil, errors.New("timeout waiting for /health")
}

// Plist renders the launchd agent of a slot.
func (m *Manager) Plist(slot string) string {
	env := map[string]string{
		"SIZER_SHARED_PORTS": "1",
		"SIZER_ADDR":         m.Addr,
		"SIZER_STATE":        m.StateDir,
		"PATH":               "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
	}
	for k, v := range m.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	esc := html.EscapeString
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", Label(slot))
	fmt.Fprintf(&b, "\t<key>ProgramArguments</key>\n\t<array>\n\t\t<string>%s</string>\n\t\t<string>serve</string>\n\t\t<string>--slot</string>\n\t\t<string>%s</string>\n\t</array>\n", esc(m.binary(slot)), slot)
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", esc(k), esc(env[k]))
	}
	b.WriteString("\t</dict>\n")
	fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n", esc(m.StateDir))
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	fmt.Fprintf(&b, "\t<key>ExitTimeOut</key>\n\t<integer>%d</integer>\n", DrainSeconds+10)
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Background</string>\n")
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", esc(filepath.Join(m.LogDir, slot+".log")))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", esc(filepath.Join(m.LogDir, slot+".log")))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// Status describes the installed slots and the instance in service.
func (m *Manager) Status() string {
	var b strings.Builder
	if h, err := m.Health(); err == nil {
		fmt.Fprintf(&b, "in service: slot %s, pid %d, version %s, http://%s\n", orNone(h.Slot), h.PID, h.Version, m.Addr)
	} else {
		fmt.Fprintf(&b, "not answering on %s\n", m.Addr)
	}
	for _, slot := range Slots {
		state := "not installed"
		if _, err := os.Stat(m.plistPath(slot)); err == nil {
			state = "installed"
			if out, err := m.Launchctl.Run("print", m.domain()+"/"+Label(slot)); err == nil {
				state = "loaded"
				for _, l := range strings.Split(out, "\n") {
					if t := strings.TrimSpace(l); strings.HasPrefix(t, "pid = ") || strings.HasPrefix(t, "state = ") {
						state += ", " + t
					}
				}
			}
		}
		fmt.Fprintf(&b, "slot %s (%s): %s\n", slot, Label(slot), state)
	}
	fmt.Fprintf(&b, "logs: %s\n", m.LogDir)
	return b.String()
}

// Uninstall stops both slots and removes their agents; the slot binaries are kept.
func (m *Manager) Uninstall() {
	for _, slot := range Slots {
		_, _ = m.Launchctl.Run("bootout", m.domain()+"/"+Label(slot))
		os.Remove(m.plistPath(slot))
	}
}
