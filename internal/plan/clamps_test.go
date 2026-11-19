package plan

import (
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

// Every input the estimate bounds comes back with what was used and a note; the model's own bounds
// say what they change.
func TestClampsAreNoted(t *testing.T) {
	req := DefaultRequest()
	req.Auto = nil
	if resp := Compute(req, referenceQuoter{}); resp.Clamps == nil || len(resp.Clamps) != 0 {
		t.Errorf("default form: clamps %+v", resp.Clamps)
	}
	req.Workload.RF = 10
	req.Workload.ObjectSize = 98500039
	req.Workload.SindexEntries = 2e9
	req.Node.VCPU = -2
	req.Hypotheses.Growth = 200
	req.Hypotheses.StorageFill = 0.01
	req.Network.AZs = 20
	req.Network.Rates = map[string]float64{"aws": -1}
	resp := Compute(req, referenceQuoter{})
	want := map[string]string{
		"workload.object_size":    "Average record size 93.94 MiB is above 8.00 MiB, the largest record Aerospike stores (max-record-size): the records are sized at 8.00 MiB, so their storage, memory and device IO are 11.7 times below what records of 93.94 MiB would need.",
		"workload.rf":             "Replication factor 10 is above 8, the most copies the sizer plans: sized with 8 copies, so with less storage, index memory and write IO than 10 copies need, and a floor of 9 nodes instead of 11.",
		"workload.sindex_entries": "Secondary index entries: 2,000,000,000 per record is above 1,000,000,000, the most the sizer accepts: the secondary indexes are sized with 1,000,000,000 entries per record, 2 times fewer than given.",
		"node.vcpu":               "Generic node vCPU -2 is below 0, the least the sizer accepts: sized with 0.",
		"hypotheses.growth":       "Growth 20000% is above 10000%, the most the sizer accepts: sized with 10000%.",
		"hypotheses.storage_fill": "Storage fill 1% is below 5%, the least the sizer accepts: sized with 5%.",
		"network.azs":             "Availability zones 20 is above 12, the most the sizer accepts: sized with 12.",
		"network.rates.aws":       "AWS cross-AZ rate -1 $/GB is below 0 $/GB, the least the sizer accepts: sized with 0 $/GB.",
	}
	if len(resp.Clamps) != len(want) {
		t.Errorf("%d clamps: %+v", len(resp.Clamps), resp.Clamps)
	}
	for _, c := range resp.Clamps {
		if c.Note != want[c.Field] {
			t.Errorf("%s: %q", c.Field, c.Note)
		}
	}
	// The figures are those of the values used, the explanation too.
	if resp.Workload.RF != sizing.MaxRF || resp.Workload.ObjectSize != sizing.MaxObjectSize || resp.Settings.AZs != 12 {
		t.Errorf("workload %+v, settings %+v, explanation %q", resp.Workload, resp.Settings, resp.Generic.Explain)
	}
	// Just above the largest record, the gap still shows: 8,700,000 B is 1.037 times 8 MiB.
	req = DefaultRequest()
	req.Auto = nil
	req.Workload.ObjectSize = 8700000
	resp = Compute(req, referenceQuoter{})
	const near = "Average record size 8.30 MiB is above 8.00 MiB, the largest record Aerospike stores (max-record-size): the records are sized at 8.00 MiB, so their storage, memory and device IO are 1.04 times below what records of 8.30 MiB would need."
	if len(resp.Clamps) != 1 || resp.Clamps[0].Note != near {
		t.Errorf("8,700,000 B: clamps %+v", resp.Clamps)
	}
}

// A value given and its bound never read alike, nor a ratio just above 1 as 1, whatever the format
// of the field rounds.
func TestClampNotesTellCloseValuesApart(t *testing.T) {
	h := sizing.DefaultHypotheses()
	for _, c := range []struct {
		clamp sizing.Clamp
		want  string
	}{
		{sizing.Clamp{Field: "workload.object_size", Given: 8390000, Used: sizing.MaxObjectSize},
			"Average record size 8,390,000 B is above 8,388,608 B, the largest record Aerospike stores (max-record-size): the records are sized at 8,388,608 B, so their storage, memory and device IO are 1.0002 times below what records of 8,390,000 B would need."},
		{sizing.Clamp{Field: "workload.object_size", Given: 8388608.3, Used: sizing.MaxObjectSize},
			"Average record size 8,388,608.3 B is above 8,388,608.0 B, the largest record Aerospike stores (max-record-size): the records are sized at 8,388,608.0 B, so their storage, memory and device IO are 1.00000004 times below what records of 8,388,608.3 B would need."},
		{sizing.Clamp{Field: "workload.sindex_entries", Given: 1.03e9, Used: sizing.MaxSindexEntries},
			"Secondary index entries: 1,030,000,000 per record is above 1,000,000,000, the most the sizer accepts: the secondary indexes are sized with 1,000,000,000 entries per record, 1.03 times fewer than given."},
		{sizing.Clamp{Field: "hypotheses.storage_fill", Given: 1.0004, Used: 1},
			"Storage fill 100.04% is above 100.00%, the most the sizer accepts: sized with 100.00%."},
		{sizing.Clamp{Field: "hypotheses.storage_fill", Given: 0.0499, Used: sizing.MinCeiling},
			"Storage fill 4.99% is below 5.00%, the least the sizer accepts: sized with 5.00%."},
		{sizing.Clamp{Field: "hypotheses.growth", Given: 100.0001, Used: sizing.MaxGrowth},
			"Growth 10,000.01% is above 10,000.00%, the most the sizer accepts: sized with 10,000.00%."},
		{sizing.Clamp{Field: "workload.objects", Given: 1.0001e15, Used: sizing.MaxObjects},
			"Records 1,000,100,000,000,000 is above 1,000,000,000,000,000, the most the sizer accepts: sized with 1,000,000,000,000,000."},
		{sizing.Clamp{Field: "hypotheses.index_bytes", Given: 1048576.5, Used: sizing.MaxBytesPerEntry},
			"Primary index: 1,048,576.5 B per record copy asked, 1,048,576 B planned, the most an estimate accepts."},
	} {
		if got := clampNote(c.clamp, h); got != c.want {
			t.Errorf("%s %v:\n got %q\nwant %q", c.clamp.Field, c.clamp.Given, got, c.want)
		}
	}
	for v, want := range map[float64]string{1.0371: "1.04", 1.00017: "1.0002", 1.05: "1.05", 1.0999: "1.1", 1.96: "2", 11.73: "11.7", 20.04: "20", 99.97: "100", 150.4: "150"} {
		if got := timesText(v); got != want {
			t.Errorf("timesText(%v) = %q, want %q", v, got, want)
		}
	}
}
