package plan

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
	"github.com/Hytm/aerospike-sizer/internal/units"
)

// clampNotes says, for each input moved into its bounds, what the estimate used instead and, for
// the bounds of the model, what that changes; h are the hypotheses the estimate uses.
func clampNotes(clamps []sizing.Clamp, h sizing.Hypotheses) []ClampNote {
	out := make([]ClampNote, 0, len(clamps))
	for _, c := range clamps {
		out = append(out, ClampNote{Clamp: c, Note: clampNote(c, h)})
	}
	return out
}

// clampNote words one clamp. The bounds of the model (the largest record, the most copies, the
// most secondary index entries, the most primary index bytes per record copy) say what the estimate
// leaves out; any other bound names the field as the page labels it.
func clampNote(c sizing.Clamp, h sizing.Hypotheses) string {
	if c.Used < c.Given {
		switch c.Field {
		case "workload.object_size":
			// What the records weigh as stored, overhead included, scales with their size.
			times := (c.Given + h.RecordOverhead) / (c.Used + h.RecordOverhead)
			return fmt.Sprintf("Average record size %s is above %s, the largest record Aerospike stores (max-record-size): the records are sized at %s, so their storage, memory and device IO are %s times below what records of %s would need.",
				bytesText(c.Given), bytesText(c.Used), bytesText(c.Used), timesText(times), bytesText(c.Given))
		case "workload.rf":
			return fmt.Sprintf("Replication factor %s is above %s, the most copies the sizer plans: sized with %s copies, so with less storage, index memory and write IO than %s copies need, and a floor of %s nodes instead of %s.",
				number(c.Given), number(c.Used), number(c.Used), number(c.Given), number(c.Used+1), number(c.Given+1))
		case "workload.sindex_entries":
			return fmt.Sprintf("Secondary index entries: %s per record is above %s, the most the sizer accepts: the secondary indexes are sized with %s entries per record, %s times fewer than given.",
				number(c.Given), number(c.Used), number(c.Used), timesText(c.Given/c.Used))
		case "hypotheses.index_bytes":
			// 1 MiB per record copy is 16,384 entries of 64 B for each record, tombstones that wellness
			// folds into its effective figure included (T18): far beyond any cluster measured.
			return fmt.Sprintf("Primary index: %s B per record copy asked, %s B planned, the most an estimate accepts.", number(c.Given), number(c.Used))
		}
	}
	in := inputOf(c.Field)
	side, bound := "above", "the most"
	if c.Used > c.Given {
		side, bound = "below", "the least"
	}
	return fmt.Sprintf("%s %s is %s %s, %s the sizer accepts: sized with %s.", in.label, in.format(c.Given), side, in.format(c.Used), bound, in.format(c.Used))
}

// input is how a note names a field of the form and writes its values, as the page labels it.
type input struct {
	label  string
	format func(float64) string
}

var inputs = map[string]input{
	"workload.object_size":          {"Average record size", bytesText},
	"workload.objects":              {"Records", number},
	"workload.reads_per_sec":        {"Reads/s", number},
	"workload.reads_found":          {"Reads that find a record", shareText},
	"workload.writes_per_sec":       {"Writes/s", number},
	"workload.rf":                   {"Replication factor", number},
	"workload.sindex_entries":       {"Secondary index entries per record", number},
	"node.vcpu":                     {"Generic node vCPU", number},
	"node.ram_gib":                  {"Generic node RAM", unit(" GiB")},
	"node.storage_tb":               {"Generic node NVMe", unit(" TB")},
	"node.iops":                     {"Generic node sustained IOPS", number},
	"hypotheses.growth":             {"Growth", shareText},
	"hypotheses.index_bytes":        {"Primary index per record copy", bytesText},
	"hypotheses.record_overhead":    {"Record overhead", bytesText},
	"hypotheses.write_overhead":     {"Write-block overhead", shareText},
	"hypotheses.compression":        {"Compression", shareText},
	"hypotheses.defrag_mult":        {"Defrag", unit("×")},
	"hypotheses.cache_hit":          {"Reads served by a cache", shareText},
	"hypotheses.storage_fill":       {"Storage fill", shareText},
	"hypotheses.ram_fill":           {"RAM fill", shareText},
	"hypotheses.io_target":          {"IO ceiling", shareText},
	"hypotheses.ops_per_vcpu":       {"Transactions/s per vCPU", number},
	"hypotheses.cpu_target":         {"CPU ceiling", shareText},
	"hypotheses.os_reserve_gib":     {"OS reserve", unit(" GiB")},
	"hypotheses.os_reserve_pct":     {"OS reserve share of RAM", shareText},
	"hypotheses.sprig_fill":         {"Sprig fill", shareText},
	"hypotheses.sprig_ram_bytes":    {"RAM per sprig", bytesText},
	"hypotheses.sindex_entry_bytes": {"Secondary index entry", bytesText},
	"hypotheses.sindex_sparseness":  {"Secondary index sparseness", unit("×")},
	"network.azs":                   {"Availability zones", number},
	"network.regions":               {"Regions", number},
	"network.residual_read":         {"Residual cross-AZ reads", shareText},
	"network.compression_gain":      {"XDR compression gain", shareText},
}

// inputOf returns how a note names a field: a rate by its provider, a field without a label by
// its path.
func inputOf(field string) input {
	if in, ok := inputs[field]; ok {
		return in
	}
	for prefix, what := range map[string]string{"network.rates.": "cross-AZ rate", "network.region_rates.": "cross-region rate"} {
		if key, ok := strings.CutPrefix(field, prefix); ok {
			if p, ok := catalog.Get(key); ok {
				return input{p.Label + " " + what, unit(" $/GB")}
			}
		}
	}
	return input{field, number}
}

// number writes a value: whole with thousands separators, else as given; far beyond any bound, in
// scientific notation.
func number(v float64) string {
	switch {
	case math.Abs(v) >= 1e15:
		return fmt.Sprintf("%.3g", v)
	case v == math.Trunc(v):
		return units.Count(v)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func unit(u string) func(float64) string { return func(v float64) string { return number(v) + u } }

func bytesText(v float64) string {
	if math.Abs(v) >= 1e18 || v < 0 {
		return number(v) + " B"
	}
	return units.IEC(v)
}

func shareText(v float64) string {
	if math.Abs(v) >= 1e13 {
		return fmt.Sprintf("%.3g%%", v*100)
	}
	return units.PctTenth(v)
}

func timesText(v float64) string {
	if v >= 100 {
		return number(math.Round(v))
	}
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}
