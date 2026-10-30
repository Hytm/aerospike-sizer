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

- **Nodes** from the workload (record size and count, reads and writes, RF, storage engine with or
  without persistence, primary index in RAM or on flash, secondary and set indexes) on a generic node,
  with a planning growth (30 % by default) applied to the records and to the throughput alike (CPU,
  device IOPS, replication traffic), and the ceilings of the Aerospike capacity planning guide: device
  usage 50 %, RAM 70 % after 10 % for the OS (5 GiB at least), CPU 60 %, headroom for the loss of a node
  or a rack, at least RF+1 nodes, the same number in every rack. Aerospike Wellness sizes with the same
  conventions. Device IO is counted in 4 KiB blocks, the unit of the instances' sustained IOPS: a read
  fetches the blocks its record spans, a write the bytes of every copy (records fill 8 MiB write blocks).
  All-flash sizing follows the guide's sprig rules (partition-tree-sprigs, 4 KiB blocks,
  10 B of RAM per sprig); secondary indexes count 14 B per entry at 1.5 sparseness.
- **Replication traffic** across AZs (rack-awareness, Active Rack, Local read, client placement) or
  across regions with XDR, and its monthly cost per provider.
- **Cost comparison**: each provider's cheapest comfortable configuration, plus the ones you add, with
  live on-demand prices for AWS and Azure in the chosen region and reference prices otherwise.

The form can be prefilled from the URL, one parameter per field named as in the API
(`/?workload.objects=4e9&workload.index_on=flash&hypotheses.growth=0.2`); Aerospike Wellness links here
that way with the parameters of an analysed cluster.

Every result is an estimate: the assumptions behind it are shown and editable in the interface.
Verify prices and network rates against your provider's official calculator before any budget decision.
