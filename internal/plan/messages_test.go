package plan

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/sizing"
)

// messageScenarios are requests that, together, make the answer use every code of the closed list
// (T22).
var messageScenarios = map[string]func(*Request){
	"default (cpu, node loss, comfortable rows)": func(*Request) {},
	"rack loss over 3 racks (rounded)":           func(r *Request) { r.Hypotheses.Loss = sizing.RackLoss },
	"rack loss without racks":                    func(r *Request) { r.Hypotheses.Loss, r.Network.AZs = sizing.RackLoss, 1 },
	"storage, flash index beyond the support threshold, flash sindex": func(r *Request) {
		r.Workload.Objects, r.Workload.IndexOn, r.Workload.SindexEntries, r.Workload.SindexOn = 2e11, sizing.OnFlash, 2, sizing.OnFlash
		r.Workload.ReadsPerSec, r.Workload.WritesPerSec = 1000, 100
	},
	"storage, flash index": func(r *Request) {
		r.Workload.ObjectSize, r.Workload.IndexOn = 1e5, sizing.OnFlash
		r.Workload.ReadsPerSec, r.Workload.WritesPerSec = 1000, 100
	},
	"memory engine, flash index asked, set and secondary indexes": func(r *Request) {
		r.Workload.Engine, r.Workload.Persist, r.Workload.SetIndex, r.Workload.SindexEntries = sizing.Memory, true, true, 1
		r.Workload.ReadsPerSec, r.Workload.WritesPerSec, r.Workload.IndexOn = 1000, 100, sizing.OnFlash
	},
	"ram, sprigs and secondary indexes": func(r *Request) {
		r.Workload.IndexOn, r.Workload.SindexEntries = sizing.OnFlash, 50
		r.Workload.ReadsPerSec, r.Workload.WritesPerSec = 1000, 100
	},
	"iops of one block": func(r *Request) { r.Node.IOPS = 20000 },
	"iops of one block, some reads found, flash index": func(r *Request) {
		found := 0.5
		r.Workload.ReadsFound, r.Workload.IndexOn, r.Node.IOPS = &found, sizing.OnFlash, 20000
	},
	"iops of blocks": func(r *Request) { r.Workload.ObjectSize, r.Node.IOPS, r.Node.VCPU = 12000, 50000, 64 },
	"iops of blocks, some reads found": func(r *Request) {
		found := 0.37
		r.Workload.ObjectSize, r.Workload.ReadsFound, r.Node.IOPS, r.Node.VCPU = 12000, &found, 50000, 64
	},
	"topology floor": func(r *Request) {
		r.Workload.Objects, r.Workload.ReadsPerSec, r.Workload.WritesPerSec = 1e3, 10, 1
	},
	"no usable RAM, no instance type": func(r *Request) {
		r.Hypotheses.OSReserveGiB = MaxNodeRAMGiB
		r.Rows = []RowRequest{{ID: "r", Provider: "aws", Region: "us-east-1", Instance: "i4i.large", Nodes: 3}}
	},
	"too little storage": func(r *Request) { r.Node.StorageTB = 0.0001 },
	"rows short, tight, too few, uneven": func(r *Request) {
		r.Auto = nil
		r.Rows = []RowRequest{
			{ID: "short", Provider: "aws", Region: "us-east-1", Instance: "i4i.large", Nodes: 3},
			{ID: "tight", Provider: "aws", Region: "us-east-1", Instance: "i4i.2xlarge", Nodes: 6},
			{ID: "one", Provider: "aws", Region: "us-east-1", Instance: "i4i.4xlarge", Nodes: 1},
			{ID: "two", Provider: "aws", Region: "us-east-1", Instance: "i4i.4xlarge", Nodes: 2},
			{ID: "uneven", Provider: "aws", Region: "us-east-1", Instance: "i4i.4xlarge", Nodes: 7},
		}
	},
	"rows unknown or not offered": func(r *Request) {
		r.Auto = nil
		r.Rows = []RowRequest{
			{ID: "a", Provider: "oracle", Region: "x", Instance: "y", Nodes: 3},
			{ID: "b", Provider: "aws", Region: "mars-1", Instance: "i4i.large", Nodes: 3},
			{ID: "c", Provider: "aws", Region: "eu-west-3", Instance: "r6id.xlarge", Nodes: 3},
		}
	},
	"unknown pricing basis and region": func(r *Request) {
		r.Auto = []string{"aws"}
		r.Pricing = Pricing{Basis: "reserved", Regions: map[string]string{"aws": "mars-1"}}
	},
}

// Every text of the answer comes with its message, one for one: a text emitted without a code
// fails here. Together the scenarios use every code of the closed list, each with its arguments.
func TestEveryTextHasACode(t *testing.T) {
	used := map[string]bool{}
	var check func(where string, ms []sizing.Message)
	check = func(where string, ms []sizing.Message) {
		for _, m := range ms {
			if err := m.Validate(); err != nil {
				t.Errorf("%s: %v", where, err)
			}
			used[m.Code] = true
			check(where, m.Parts)
		}
	}
	aligned := func(where string, texts []string, ms []sizing.Message) {
		if !slices.Equal(texts, sizing.Texts(ms)) {
			t.Errorf("%s: texts %q, messages %q", where, texts, sizing.Texts(ms))
		}
		check(where, ms)
	}
	for name, change := range messageScenarios {
		req := DefaultRequest()
		change(&req)
		resp := Compute(req, referenceQuoter{})
		g := resp.Generic
		if g.Explain != strings.Join(sizing.Texts(g.ExplainMessages), " ") || len(g.ExplainMessages) == 0 {
			t.Errorf("%s: explanation %q, messages %q", name, g.Explain, sizing.Texts(g.ExplainMessages))
		}
		check(name+": explanation", g.ExplainMessages)
		aligned(name+": generic checks", g.Checks, g.CheckMessages)
		aligned(name+": notes", resp.Notes, resp.NoteMessages)
		for _, r := range resp.Rows {
			for _, row := range []*Row{&r, r.Alternative} {
				if row != nil {
					aligned(name+": "+row.ID, row.Fit.Checks, row.Fit.Messages)
				}
			}
		}
	}
	for _, c := range slices.Sorted(maps.Keys(sizing.Codes())) {
		if !used[c] {
			t.Errorf("no scenario uses %s", c)
		}
	}
}

// A region is quoted by its id, which aerospike-wellness shows, and by the label the English uses.
func TestNotesQuoteTheRegionIDAndLabel(t *testing.T) {
	req := DefaultRequest()
	req.Workload.Objects = 1e14
	resp := Compute(req, referenceQuoter{})
	p, _ := catalog.Get("aws")
	region, _ := p.Region(p.DefaultRegion)
	m := resp.NoteMessages[0]
	if m.Code != "note.no_instance" || m.Args["provider"] != "AWS" || m.Args["region"] != region.ID || m.Args["region_label"] != region.Label {
		t.Errorf("note %+v", m)
	}
}
