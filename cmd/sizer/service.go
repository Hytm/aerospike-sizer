package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hytm/aerospike-sizer/internal/buildinfo"
	"github.com/Hytm/aerospike-sizer/internal/config"
	"github.com/Hytm/aerospike-sizer/internal/service"
)

func manager() *service.Manager {
	cfg := config.Load()
	return service.NewManager(cfg.StateDir, cfg.Addr, buildinfo.Version, nil)
}

// runDeploy installs this binary as the launchd service, handing over without downtime.
func runDeploy([]string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	return manager().Deploy(exe)
}

func runStatus([]string) error {
	fmt.Print(manager().Status())
	return nil
}

func runUninstall([]string) error {
	manager().Uninstall()
	fmt.Println("launchd agents removed")
	return nil
}
