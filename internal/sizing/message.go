package sizing

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Hytm/aerospike-sizer/internal/units"
)

// Message is one text of the API, a sentence or an item a sentence lists, as a stable code with
// the values it quotes and the items it lists, and its English (decision T22). The interface shows
// Text; aerospike-wellness words Code, Args and Parts in French and English, and falls back on Text
// for a code it does not know. A code never changes meaning: a new text gets a new code.
type Message struct {
	Code  string    `json:"code"`
	Text  string    `json:"text"`
	Args  Args      `json:"args,omitempty"`
	Parts []Message `json:"parts,omitempty"` // the items the sentence lists, joined with ", "
}

// Args are the values a message quotes, by name: numbers (float64, as JSON gives them back) and
// names (string). A name has the same Kind in every code.
type Args map[string]any

// Kind tells what an argument holds, and so how a caller writes it.
type Kind string

// Kinds of the arguments.
const (
	KindResource   Kind = "resource"    // storage, ram, cpu or iops
	KindAmount     Kind = "amount"      // an amount of the message's resource: bytes (storage, ram), vCPU or IOPS
	KindBytes      Kind = "bytes"       // bytes
	KindCount      Kind = "count"       // a number, written to the unit
	KindShare      Kind = "share"       // a share, 1 for 100 %, written to the percent
	KindShareTenth Kind = "share_tenth" // a measured share, 1 for 100 %, written to the tenth of a percent
	KindGiB        Kind = "gib"         // gibibytes
	KindName       Kind = "name"        // written as is: a provider, a region, an instance type, a value given
)

// argKinds gives the kind of every argument name, the same in every code.
var argKinds = map[string]Kind{
	"resource":             KindResource,
	"demand":               KindAmount,
	"needed":               KindAmount,
	"usable":               KindAmount,
	"bytes":                KindBytes,
	"count":                KindCount,
	"tps":                  KindCount,
	"ops_per_vcpu":         KindCount,
	"sprigs":               KindCount,
	"support_sprigs":       KindCount,
	"blocks":               KindCount,
	"rf":                   KindCount,
	"nodes":                KindCount,
	"min_nodes":            KindCount,
	"racks":                KindCount,
	"azs":                  KindCount,
	"growth":               KindShare,
	"fill":                 KindShare,
	"load":                 KindShare,
	"os_reserve_pct":       KindShare,
	"found":                KindShareTenth,
	"os_reserve_gib":       KindGiB,
	"provider":             KindName,
	"region":               KindName,
	"region_label":         KindName,
	"instance":             KindName,
	"basis":                KindName,
	"default_region":       KindName,
	"default_region_label": KindName,
}

// codeSpec is a code of the closed list: the arguments it quotes, whether it lists items (Parts),
// and its English, from the arguments and the items already joined.
type codeSpec struct {
	args    []string
	lists   bool
	english func(a Args, parts string) string
}

// codes is the closed list of the codes of the API (T22): every explanation, fit check and note,
// and every item they list. A new text needs a new code here, in T22 and in aerospike-wellness.
var codes = map[string]codeSpec{
	// The explanation of the generic sizing.
	"explain.unusable": {[]string{"resource"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("No node count works: the generic node has no usable %s once the ceilings and the OS reserve are applied.", a.label())
	}},
	"explain.too_little": {[]string{"resource"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("No sensible node count works: the generic node offers too little %s for this workload.", a.label())
	}},
	"explain.storage": {[]string{"resource", "demand", "usable", "fill", "rf", "growth"}, true, func(a Args, parts string) string {
		return fmt.Sprintf("Disk storage drives the sizing: %s to place (%s; %s), %s usable per node (%s fill).",
			units.SI(a.num("demand")), parts, a.scope(), units.SI(a.num("usable")), units.Pct(a.num("fill")))
	}},
	"explain.ram": {[]string{"resource", "demand", "usable", "fill", "rf", "growth", "os_reserve_gib", "os_reserve_pct"}, true, func(a Args, parts string) string {
		return fmt.Sprintf("Memory drives the sizing: %s (%s; %s), %s usable per node (%s).",
			units.IEC(a.num("demand")), parts, a.scope(), units.IEC(a.num("usable")), a.ramCeiling())
	}},
	"explain.records_in_memory": {[]string{"fill"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("That ceiling weighs the RAM fill of the indexes with the %s storage fill of the records in memory.", units.Pct(a.num("fill")))
	}},
	"explain.cpu": {[]string{"resource", "tps", "growth", "demand", "ops_per_vcpu", "usable", "fill"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("CPU drives the sizing: %s transactions/s at the horizon (%s growth) need %s at %s per vCPU, %s usable per node (%s CPU).",
			units.Count(a.num("tps")), units.Pct(a.num("growth")), a.amount("demand"), units.Count(a.num("ops_per_vcpu")), a.amount("usable"), units.Pct(a.num("fill")))
	}},
	"explain.iops": {[]string{"resource", "demand", "usable", "fill", "rf", "growth"}, true, func(a Args, parts string) string {
		return fmt.Sprintf("Device throughput drives the sizing: %s IOPS of 4 KiB (%s; %s), %s usable per node (%s of the sustained IOPS).",
			units.Count(a.num("demand")), parts, a.scope(), units.Count(a.num("usable")), units.Pct(a.num("fill")))
	}},
	"explain.floor": {[]string{"min_nodes"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("Storage, RAM, CPU and IOPS fit on fewer nodes: the topology floor (RF+1 = %d) sets the minimum for fault tolerance.", a.int("min_nodes"))
	}},
	"explain.loss": {nil, true, func(_ Args, parts string) string {
		return fmt.Sprintf("The ceilings still hold with %s.", parts)
	}},
	"explain.rounded": {[]string{"nodes", "racks"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("Rounded up to %d nodes so that each of the %d racks holds the same number.", a.int("nodes"), a.int("racks"))
	}},

	// What a demand is made of: items of the explanation.
	"part.records": {[]string{"bytes"}, false, func(a Args, _ string) string { return "records " + units.SI(a.num("bytes")) }},
	"part.flash_index": {[]string{"bytes", "sprigs"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("primary index on flash %s with %d sprigs per partition", units.SI(a.num("bytes")), a.int("sprigs"))
	}},
	"part.flash_index_support": {[]string{"bytes", "sprigs", "support_sprigs"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("primary index on flash %s with %d sprigs per partition (above %s: ask Aerospike Support)",
			units.SI(a.num("bytes")), a.int("sprigs"), units.Count(a.num("support_sprigs")))
	}},
	"part.flash_sindex":      {[]string{"bytes"}, false, func(a Args, _ string) string { return "secondary indexes on flash " + units.SI(a.num("bytes")) }},
	"part.index":             {[]string{"bytes"}, false, func(a Args, _ string) string { return "primary index " + units.IEC(a.num("bytes")) }},
	"part.sprigs":            {[]string{"bytes"}, false, func(a Args, _ string) string { return "sprigs of the flash index " + units.IEC(a.num("bytes")) }},
	"part.sindex":            {[]string{"bytes"}, false, func(a Args, _ string) string { return "secondary indexes " + units.IEC(a.num("bytes")) }},
	"part.set_index":         {[]string{"bytes"}, false, func(a Args, _ string) string { return "set indexes " + units.IEC(a.num("bytes")) }},
	"part.records_in_memory": {[]string{"bytes"}, false, func(a Args, _ string) string { return "records in memory " + units.IEC(a.num("bytes")) }},
	"part.reads":             {[]string{"count"}, false, func(a Args, _ string) string { return "record reads " + units.Count(a.num("count")) }},
	"part.reads_blocks": {[]string{"count", "blocks"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("record reads of %.0f blocks %s", a.num("blocks"), units.Count(a.num("count")))
	}},
	"part.reads_found": {[]string{"count", "found"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("record reads for the %s of the reads that find a record %s", units.PctTenth(a.num("found")), units.Count(a.num("count")))
	}},
	"part.reads_blocks_found": {[]string{"count", "blocks", "found"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("record reads of %.0f blocks for the %s of the reads that find a record %s", a.num("blocks"), units.PctTenth(a.num("found")), units.Count(a.num("count")))
	}},
	"part.writes": {[]string{"count"}, false, func(a Args, _ string) string {
		return "record writes in 4 KiB blocks with defrag " + units.Count(a.num("count"))
	}},
	"part.flash_index_io": {[]string{"count"}, false, func(a Args, _ string) string { return "flash index blocks " + units.Count(a.num("count")) }},

	// Ceilings and losses: items of a fit check or of the explanation.
	"ceiling.storage": {[]string{"fill"}, false, func(a Args, _ string) string { return units.Pct(a.num("fill")) + " fill" }},
	"ceiling.ram":     {[]string{"fill", "os_reserve_gib", "os_reserve_pct"}, false, func(a Args, _ string) string { return a.ramCeiling() }},
	"ceiling.cpu":     {[]string{"fill"}, false, func(a Args, _ string) string { return units.Pct(a.num("fill")) + " CPU" }},
	"ceiling.iops":    {[]string{"fill"}, false, func(a Args, _ string) string { return units.Pct(a.num("fill")) + " of the sustained IOPS" }},
	"loss.node":       {nil, false, func(Args, string) string { return "one node down" }},
	"loss.rack":       {nil, false, func(Args, string) string { return "one rack down" }},

	// Fit checks of a node count.
	"check.topology": {[]string{"nodes", "min_nodes"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("Topology: %d node(s) < RF+1 (%d min).", a.int("nodes"), a.int("min_nodes"))
	}},
	"check.fewer_azs": {[]string{"azs"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("Fewer nodes than configured AZs (%d): no real multi-AZ spread.", a.int("azs"))
	}},
	"check.uneven_racks": {[]string{"nodes", "racks"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("%d nodes do not split evenly over %d racks.", a.int("nodes"), a.int("racks"))
	}},
	"check.no_usable": {[]string{"resource"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("No usable %s on this node shape.", a.label())
	}},
	"check.short": {[]string{"resource", "needed", "usable"}, true, func(a Args, parts string) string {
		return fmt.Sprintf("Not enough %s: %s needed vs %s usable (%s).", a.label(), a.amount("needed"), a.amount("usable"), parts)
	}},
	"check.tight": {[]string{"resource", "load"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("%s tight: %s of the usable capacity.", capitalize(a.label()), units.Pct(a.num("load")))
	}},
	"check.comfortable": {nil, false, func(Args, string) string { return "Comfortable margin on storage, RAM, CPU and IOPS." }},

	// Notes of the plan.
	"note.unknown_pricing_basis": {[]string{"basis"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("Unknown pricing basis %q: on-demand prices used.", a.str("basis"))
	}},
	"note.unknown_pricing_region": {[]string{"provider", "region", "default_region", "default_region_label"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("%s: unknown region %q for the recommendation, %s used instead.", a.str("provider"), a.str("region"), a.str("default_region_label"))
	}},
	"note.flash_index_in_ram": {nil, false, func(Args, string) string {
		return "The primary index is planned in RAM: index-type flash does not work with the memory engine (configuration reference, 7.0)."
	}},
	"note.rack_loss_as_node": {nil, false, func(Args, string) string {
		return "The ceilings are kept after the loss of one node: losing a whole rack can only be planned for with rack-aware AZs in a single region."
	}},
	"note.no_instance": {[]string{"provider", "region", "region_label"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("%s: no instance type of the catalog offered in %s suits this workload.", a.str("provider"), a.str("region_label"))
	}},
	"note.unknown_provider": {[]string{"provider"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("Unknown provider %q ignored.", a.str("provider"))
	}},
	"note.unknown_row": {[]string{"provider", "region", "instance"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("%s: unknown region %q or instance type %q ignored.", a.str("provider"), a.str("region"), a.str("instance"))
	}},
	"note.not_offered": {[]string{"provider", "instance", "region", "region_label"}, false, func(a Args, _ string) string {
		return fmt.Sprintf("%s: instance type %q is not offered in %s, ignored.", a.str("provider"), a.str("instance"), a.str("region_label"))
	}},
}

// message builds a message of the closed list and words it in English. Whole numbers and
// constraints are stored as JSON gives them back: float64 and string.
func message(c string, args Args, parts ...Message) Message {
	for k, v := range args {
		switch v := v.(type) {
		case int:
			args[k] = float64(v)
		case Constraint:
			args[k] = string(v)
		}
	}
	m := Message{Code: c, Args: args, Parts: parts}
	m.Text = m.english()
	return m
}

// Note builds a note of the plan, a note.* code of the closed list.
func Note(code string, args Args) Message { return message(code, args) }

// Texts returns the English of the messages.
func Texts(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

// english words the message from its code; Validate rejects a code the closed list does not have.
func (m Message) english() string {
	c, ok := codes[m.Code]
	if !ok {
		return m.Code
	}
	return c.english(m.Args, strings.Join(Texts(m.Parts), ", "))
}

// Arg is an argument of a code: its name and its kind.
type Arg struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
}

// Codes returns the closed list of the codes (T22), each with the arguments it quotes.
func Codes() map[string][]Arg {
	out := make(map[string][]Arg, len(codes))
	for name, c := range codes {
		args := make([]Arg, len(c.args))
		for i, a := range c.args {
			args[i] = Arg{Name: a, Kind: argKinds[a]}
		}
		out[name] = args
	}
	return out
}

// Validate checks that the message is one of the closed list: a known code, with its arguments
// and no other, each of the type its kind calls for, items exactly when the code lists some, each
// valid in turn, and the English the code words.
func (m Message) Validate() error {
	c, ok := codes[m.Code]
	if !ok {
		return fmt.Errorf("unknown code %q", m.Code)
	}
	if got := slices.Sorted(maps.Keys(m.Args)); !slices.Equal(got, slices.Sorted(slices.Values(c.args))) {
		return fmt.Errorf("%s: arguments %v, want %v", m.Code, got, c.args)
	}
	for name, v := range m.Args {
		if err := argKinds[name].check(v); err != nil {
			return fmt.Errorf("%s: %s: %w", m.Code, name, err)
		}
	}
	if c.lists != (len(m.Parts) > 0) {
		return fmt.Errorf("%s: %d items", m.Code, len(m.Parts))
	}
	for _, p := range m.Parts {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("%s: %w", m.Code, err)
		}
	}
	if want := m.english(); m.Text != want {
		return fmt.Errorf("%s: text %q, want %q", m.Code, m.Text, want)
	}
	return nil
}

// check tells whether a value is one the kind holds.
func (k Kind) check(v any) error {
	switch k {
	case KindResource:
		if s, ok := v.(string); !ok || !slices.Contains(Resources, Constraint(s)) {
			return fmt.Errorf("%v is not a resource", v)
		}
	case KindName:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%v is not a name", v)
		}
	case "":
		return fmt.Errorf("no kind")
	default:
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%v is not a number", v)
		}
	}
	return nil
}

func (a Args) num(k string) float64 { v, _ := a[k].(float64); return v }
func (a Args) int(k string) int     { return int(a.num(k)) }
func (a Args) str(k string) string  { v, _ := a[k].(string); return v }

// label names the resource of the message.
func (a Args) label() string { return Constraint(a.str("resource")).label() }

// amount writes an amount of the resource of the message.
func (a Args) amount(k string) string { return Constraint(a.str("resource")).amount(a.num(k)) }

// scope tells what the demand of the explanation includes.
func (a Args) scope() string {
	return fmt.Sprintf("RF%d, %s growth included", a.int("rf"), units.Pct(a.num("growth")))
}

// ramCeiling writes the RAM ceiling of the explanation and of a fit check.
func (a Args) ramCeiling() string {
	return fmt.Sprintf("%s of RAM after the OS reserve of %.0f GiB or %s", units.Pct(a.num("fill")), a.num("os_reserve_gib"), units.Pct(a.num("os_reserve_pct")))
}
