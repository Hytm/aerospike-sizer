package sizing

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

// sample returns a message of the code with a value of the kind of each of its arguments and,
// when it lists items, one item.
func sample(c string) Message {
	a := Args{}
	for _, name := range codes[c].args {
		switch argKinds[name] {
		case KindResource:
			a[name] = string(ByRAM)
		case KindName:
			a[name] = "x"
		default:
			a[name] = 0.5
		}
	}
	var parts []Message
	if codes[c].lists {
		parts = []Message{message("loss.node", nil)}
	}
	return message(c, a, parts...)
}

func TestEveryCodeHasEnglish(t *testing.T) {
	for c := range codes {
		m := sample(c)
		if err := m.Validate(); err != nil {
			t.Errorf("%s: %v", c, err)
		}
		if m.Text == "" || m.Text == c || strings.Contains(m.Text, "%!") {
			t.Errorf("%s: English %q", c, m.Text)
		}
	}
}

// One kind per argument name, in every code: a caller writes an argument from its name.
func TestEveryArgumentHasAKind(t *testing.T) {
	used := map[string]bool{}
	for c, spec := range codes {
		for _, name := range spec.args {
			if argKinds[name] == "" {
				t.Errorf("%s: argument %s has no kind", c, name)
			}
			used[name] = true
		}
	}
	for name := range argKinds {
		if !used[name] {
			t.Errorf("argument %s is in no code", name)
		}
	}
}

func TestValidateRejectsAMessageOutsideTheList(t *testing.T) {
	good := message("check.tight", Args{"resource": ByRAM, "load": 0.9})
	if err := good.Validate(); err != nil || good.Text != "RAM tight: 90% of the usable capacity." {
		t.Fatalf("%q: %v", good.Text, err)
	}
	bad := map[string]Message{
		"unknown code":     {Code: "check.bogus", Text: "check.bogus"},
		"missing argument": message("check.tight", Args{"resource": ByRAM}),
		"extra argument":   message("check.tight", Args{"resource": ByRAM, "load": 0.9, "nodes": 3}),
		"not a resource":   message("check.tight", Args{"resource": "topology", "load": 0.9}),
		"not a number":     message("check.tight", Args{"resource": ByRAM, "load": "90%"}),
		"no item":          message("explain.loss", nil),
		"unexpected item":  message("check.comfortable", nil, message("loss.node", nil)),
		"invalid item":     message("explain.loss", nil, Message{Code: "loss.bogus"}),
		"other text":       {Code: "check.comfortable", Text: "Fine."},
	}
	for name, m := range bad {
		if m.Validate() == nil {
			t.Errorf("%s: %+v is valid", name, m)
		}
	}
}

// The explanation is the English of its messages, every one of the closed list, and the messages
// come back from JSON as they were sent.
func TestMessagesAreTheExplanation(t *testing.T) {
	w := workload()
	w.IndexOn, w.SindexEntries, w.ObjectSize = OnFlash, 2, 1e5
	w.ReadsPerSec, w.WritesPerSec = 1000, 100
	h := DefaultHypotheses()
	h.Loss = RackLoss
	d := Detailed(w, h)
	l := Layout{RF: 2, AZs: 3, Racks: 3}
	node := Amounts{Storage: 3.75e12, RAM: 128 * catalog.GiB, CPU: 16, IOPS: 2e5}
	s := Size(d.Amounts, node, h, l)
	ms := s.Explained(w, d, node, h, l)
	if s.Binding != ByStorage || s.Explain(w, d, node, h, l) != strings.Join(Texts(ms), " ") {
		t.Fatalf("sizing %+v, messages %q", s, Texts(ms))
	}
	var codes []string
	for _, m := range ms {
		if err := m.Validate(); err != nil {
			t.Error(err)
		}
		codes = append(codes, m.Code)
		for _, p := range m.Parts {
			codes = append(codes, p.Code)
		}
	}
	want := []string{"explain.storage", "part.records", "part.flash_index", "explain.loss", "loss.rack"}
	if !slices.Equal(codes, want) {
		t.Errorf("codes %v, want %v", codes, want)
	}
	b, err := json.Marshal(ms)
	if err != nil {
		t.Fatal(err)
	}
	var back []Message
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for i, m := range back {
		if err := m.Validate(); err != nil || m.Text != ms[i].Text {
			t.Errorf("%s back from JSON: %q, %v", m.Code, m.Text, err)
		}
	}
}
