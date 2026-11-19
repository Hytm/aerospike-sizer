package catalog

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestCatalogMatchesWellness compares this catalog with aerospike-wellness's when its checkout sits
// next to this one, or where WELLNESS_CATALOG names its catalog.json: the same types, shapes,
// architecture and reference prices, on demand and with a one-year commitment (T21), and planning
// IOPS equal to half its published write IOPS, or to its own figure without one (decision T19).
// Without it, the test is skipped.
func TestCatalogMatchesWellness(t *testing.T) {
	path := cmp.Or(os.Getenv("WELLNESS_CATALOG"), filepath.Join("..", "..", "..", "aerospike-wellness", "internal", "sizing", "catalog.json"))
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no wellness catalog at %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	var theirs []struct {
		Provider  string  `json:"provider"`
		Family    string  `json:"family"`
		Name      string  `json:"name"`
		VCPU      int     `json:"vcpu"`
		RAMGiB    float64 `json:"ram_gib"`
		Drives    int     `json:"drives"`
		DriveGB   float64 `json:"drive_gb"`
		IOPS      float64 `json:"iops"`
		WriteIOPS float64 `json:"write_iops"`
		Arch      string  `json:"arch"`
		PriceHour float64 `json:"price_hour"`
		Reserved  float64 `json:"reserved_1y_hour"`
		Via       string  `json:"reserved_via"`
	}
	if err := json.Unmarshal(data, &theirs); err != nil {
		t.Fatal(err)
	}
	ours := 0
	for _, p := range Providers {
		ours += len(p.Instances)
	}
	if len(theirs) != ours {
		t.Errorf("wellness lists %d types, this catalog %d", len(theirs), ours)
	}
	for _, w := range theirs {
		p, ok := Get(w.Provider)
		if !ok {
			t.Errorf("%s: provider %q", w.Name, w.Provider)
			continue
		}
		i, ok := p.Instance(w.Name)
		if !ok {
			t.Errorf("%s: in wellness's catalog, not in this one", w.Name)
			continue
		}
		iops := w.IOPS
		if w.WriteIOPS > 0 {
			iops = w.WriteIOPS / 2
		}
		if i.Family != w.Family || i.Arch != w.Arch || i.VCPU != w.VCPU || i.RAMGiB != w.RAMGiB || i.Drives != w.Drives ||
			i.DriveGB != w.DriveGB || i.IOPS != iops || i.PriceHour != w.PriceHour ||
			i.Reserved1YHour != w.Reserved || i.ReservedVia != w.Via {
			t.Errorf("%s: here %+v, wellness %+v (planning IOPS %v)", w.Name, i, w, iops)
		}
	}
}
