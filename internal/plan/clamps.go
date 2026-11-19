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
// leaves out; any other bound names the field as the page labels it. The value given and the value
// used always read differently (apart).
func clampNote(c sizing.Clamp, h sizing.Hypotheses) string {
	if c.Used < c.Given {
		switch c.Field {
		case "workload.object_size":
			given, used := apart(sizes, c.Given, c.Used)
			// What the records weigh as stored, overhead included, scales with their size.
			times := (c.Given + h.RecordOverhead) / (c.Used + h.RecordOverhead)
			return fmt.Sprintf("Average record size %s is above %s, the largest record Aerospike stores (max-record-size): the records are sized at %s, so their storage, memory and device IO are %s times below what records of %s would need.",
				given, used, used, timesText(times), given)
		case "workload.rf":
			return fmt.Sprintf("Replication factor %s is above %s, the most copies the sizer plans: sized with %s copies, so with less storage, index memory and write IO than %s copies need, and a floor of %s nodes instead of %s.",
				number(c.Given), number(c.Used), number(c.Used), number(c.Given), number(c.Used+1), number(c.Given+1))
		case "workload.sindex_entries":
			given, used := apart(plain, c.Given, c.Used)
			return fmt.Sprintf("Secondary index entries: %s per record is above %s, the most the sizer accepts: the secondary indexes are sized with %s entries per record, %s times fewer than given.",
				given, used, used, timesText(c.Given/c.Used))
		case "hypotheses.index_bytes":
			given, used := apart(plain, c.Given, c.Used)
			// 1 MiB per record copy is 16,384 entries of 64 B for each record, tombstones that wellness
			// folds into its effective figure included (T18): far beyond any cluster measured.
			return fmt.Sprintf("Primary index: %s B per record copy asked, %s B planned, the most an estimate accepts.", given, used)
		}
	}
	in := inputOf(c.Field)
	given, used := apart(in.kind, c.Given, c.Used)
	side, bound := "above", "the most"
	if c.Used > c.Given {
		side, bound = "below", "the least"
	}
	return fmt.Sprintf("%s %s is %s %s, %s the sizer accepts: sized with %s.", in.label, given, side, used, bound, used)
}

// input is how a note names a field of the form and writes its values, as the page labels it.
type input struct {
	label string
	kind  kind
}

// kind writes the values of a field: format as the page does, exact to a number of decimals of the
// same unit, for a value and its bound that format writes alike.
type kind struct {
	format func(float64) string
	exact  func(v float64, decimals int) string
}

var (
	plain  = kind{number, units.Grouped}
	sizes  = kind{bytesText, func(v float64, d int) string { return units.Grouped(v, d) + " B" }}
	shares = kind{shareText, func(v float64, d int) string { return units.Grouped(v*100, d) + "%" }}
)

func unit(u string) kind {
	return kind{func(v float64) string { return number(v) + u }, func(v float64, d int) string { return units.Grouped(v, d) + u }}
}

// maxDecimals is the most decimals apart writes: far below the gap of any bound a value can cross.
const maxDecimals = 20

// apart writes a value given and the value used in the format of their kind or, when it writes
// them alike (8,390,000 B and 8 MiB both read 8.00 MiB), exactly, to as many decimals as tell them
// apart: "8,390,000 B is above 8,388,608 B", "100.04% is above 100.00%".
func apart(k kind, given, used float64) (string, string) {
	g, u := k.format(given), k.format(used)
	for d := 0; g == u && d <= maxDecimals; d++ {
		g, u = k.exact(given, d), k.exact(used, d)
	}
	return g, u
}

var inputs = map[string]input{
	"workload.object_size":          {"Average record size", sizes},
	"workload.objects":              {"Records", plain},
	"workload.reads_per_sec":        {"Reads/s", plain},
	"workload.reads_found":          {"Reads that find a record", shares},
	"workload.writes_per_sec":       {"Writes/s", plain},
	"workload.rf":                   {"Replication factor", plain},
	"workload.sindex_entries":       {"Secondary index entries per record", plain},
	"node.vcpu":                     {"Generic node vCPU", plain},
	"node.ram_gib":                  {"Generic node RAM", unit(" GiB")},
	"node.storage_tb":               {"Generic node NVMe", unit(" TB")},
	"node.iops":                     {"Generic node sustained IOPS", plain},
	"hypotheses.growth":             {"Growth", shares},
	"hypotheses.index_bytes":        {"Primary index per record copy", sizes},
	"hypotheses.record_overhead":    {"Record overhead", sizes},
	"hypotheses.write_overhead":     {"Write-block overhead", shares},
	"hypotheses.compression":        {"Compression", shares},
	"hypotheses.defrag_mult":        {"Defrag", unit("×")},
	"hypotheses.cache_hit":          {"Reads served by a cache", shares},
	"hypotheses.storage_fill":       {"Storage fill", shares},
	"hypotheses.ram_fill":           {"RAM fill", shares},
	"hypotheses.io_target":          {"IO ceiling", shares},
	"hypotheses.ops_per_vcpu":       {"Transactions/s per vCPU", plain},
	"hypotheses.cpu_target":         {"CPU ceiling", shares},
	"hypotheses.os_reserve_gib":     {"OS reserve", unit(" GiB")},
	"hypotheses.os_reserve_pct":     {"OS reserve share of RAM", shares},
	"hypotheses.sprig_fill":         {"Sprig fill", shares},
	"hypotheses.sprig_ram_bytes":    {"RAM per sprig", sizes},
	"hypotheses.sindex_entry_bytes": {"Secondary index entry", sizes},
	"hypotheses.sindex_sparseness":  {"Secondary index sparseness", unit("×")},
	"network.azs":                   {"Availability zones", plain},
	"network.regions":               {"Regions", plain},
	"network.residual_read":         {"Residual cross-AZ reads", shares},
	"network.compression_gain":      {"XDR compression gain", shares},
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
	return input{field, plain}
}

// number writes a value with thousands separators, to the decimals it is given with; far beyond any
// bound, in scientific notation.
func number(v float64) string {
	if math.Abs(v) >= 1e15 {
		return fmt.Sprintf("%.3g", v)
	}
	_, decimals, _ := strings.Cut(strconv.FormatFloat(v, 'f', -1, 64), ".")
	return units.Grouped(v, len(decimals))
}

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

// timesText writes how many times a value is above its bound: to the tenth, to the unit from 100,
// and under 1.1 to as many decimals as show the gap, two at least (1.04, 1.0002).
func timesText(v float64) string {
	if v >= 100 {
		return number(math.Round(v))
	}
	decimals := 1
	if gap := v - 1; gap > 0 && gap < 0.1 {
		decimals = max(2, int(math.Ceil(-math.Log10(gap))))
	}
	return strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(v, 'f', decimals, 64), "0"), ".")
}
