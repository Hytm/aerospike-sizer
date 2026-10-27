// Command sizer serves the Aerospike cluster sizer on a local web UI.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/Hytm/aerospike-sizer/internal/buildinfo"
)

type command struct {
	summary string
	run     func(args []string) error
}

var commands = map[string]command{
	"serve":     {"run the web interface", runServe},
	"deploy":    {"install this binary as the launchd service (zero-downtime handover)", runDeploy},
	"status":    {"show the launchd service state", runStatus},
	"uninstall": {"remove the launchd service", runUninstall},
	"version": {"print the version", func([]string) error {
		fmt.Println(buildinfo.Version)
		return nil
	}},
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		usage()
		os.Exit(2)
	}
	if err := cmd.run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "sizer:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sizer <command> [flags]")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", name, commands[name].summary)
	}
}
