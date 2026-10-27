# Aerospike Cluster Sizer

Local tool to size an Aerospike cluster from a workload profile: node count, replication network
traffic (multi-AZ or cross-region XDR) and a cost comparison across AWS, Google Cloud and Azure.

A single Go binary serves the web interface; on macOS it runs in the background as a launchd service
on <http://127.0.0.1:7310>.

## Usage

```bash
make run       # foreground server on http://127.0.0.1:7311
make deploy    # install or upgrade the launchd service, without downtime
make status    # state of the service
```

Requires Go 1.27. `make ci` runs the local checks (gofmt, vet, staticcheck, tests, build).

Every result is an estimate: the assumptions behind it are shown and editable in the interface.
Verify prices and network rates against your provider's official calculator before any budget decision.
