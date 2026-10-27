// Package buildinfo exposes the version stamped into the binary at build time.
package buildinfo

// Version is set by the Makefile through -ldflags; "dev" for plain `go build`.
var Version = "dev"
