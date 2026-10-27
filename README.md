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

What it computes:

- **Nodes** from the workload (record size and count, reads and writes, RF, storage engine) on a generic
  node, with Aerospike-minded ceilings: storage 70 % (stop-writes), RAM 70 % after the OS reserve, CPU 60 %,
  one spare node (N+1), at least RF+1 nodes, the same number in every rack.
- **Replication traffic** across AZs (rack-awareness, Active Rack, Local read, client placement) or
  across regions with XDR, and its monthly cost per provider.
- **Cost comparison**: each provider's cheapest comfortable configuration, plus the ones you add, with
  live on-demand prices for AWS and Azure in the chosen region and reference prices otherwise.

Every result is an estimate: the assumptions behind it are shown and editable in the interface.
Verify prices and network rates against your provider's official calculator before any budget decision.
