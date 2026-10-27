// Package config reads the settings shared by every command from the environment.
package config

import (
	"os"
	"path/filepath"
)

// DefaultAddr is where the launchd service listens.
const DefaultAddr = "127.0.0.1:7310"

// Config holds the environment-driven settings.
type Config struct {
	Addr        string // listen address (SIZER_ADDR)
	StateDir    string // service state: binaries per slot (SIZER_STATE, default ~/Library/Application Support/sizer)
	SharedPorts bool   // SO_REUSEPORT for zero-downtime handover (SIZER_SHARED_PORTS=1, set by launchd only)
}

// Load reads the configuration from the environment.
func Load() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Addr:        env("SIZER_ADDR", DefaultAddr),
		StateDir:    env("SIZER_STATE", filepath.Join(home, "Library", "Application Support", "sizer")),
		SharedPorts: os.Getenv("SIZER_SHARED_PORTS") == "1",
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
