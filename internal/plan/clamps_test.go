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
}
