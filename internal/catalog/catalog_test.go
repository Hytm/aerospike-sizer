package catalog

import "testing"

func TestEveryProviderIsComplete(t *testing.T) {
	if len(Providers) != 3 {
		t.Fatalf("%d providers", len(Providers))
	}
	for _, p := range Providers {
		if _, ok := p.Region(p.DefaultRegion); !ok {
			t.Errorf("%s: default region %q is not listed", p.Key, p.DefaultRegion)
		}
		if PricesRegion[p.Key] == "" {
			t.Errorf("%s: no reference price region", p.Key)
		}
		last := 0.0
		for _, i := range p.Instances {
			if i.VCPU <= 0 || i.RAMGiB <= 0 || i.Drives <= 0 || i.DriveGB <= 0 || i.IOPS <= 0 || i.PriceHour <= 0 {
				t.Errorf("%s %s: incomplete %+v", p.Key, i.Name, i)
			}
			if i.PriceHour < last {
				t.Errorf("%s %s: instances must go from the smallest to the largest", p.Key, i.Name)
			}
			last = i.PriceHour
		}
	}
}

func TestUnits(t *testing.T) {
	p, _ := Get("aws")
	i, ok := p.Instance("i4i.8xlarge")
	if !ok {
		t.Fatal("i4i.8xlarge missing")
	}
	if i.StorageBytes() != 7.5e12 || i.RAMBytes() != 256*GiB {
		t.Errorf("storage %v, RAM %v", i.StorageBytes(), i.RAMBytes())
	}
}

// TestGCPAxion checks the C4A types against the published shapes and us-east1 prices (decision T13).
func TestGCPAxion(t *testing.T) {
	p, _ := Get("gcp")
	for name, want := range map[string]Instance{
		"c4a-highmem-16-lssd": {VCPU: 16, RAMGiB: 128, Drives: 4, PriceHour: 1.1896},
		"c4a-highmem-32-lssd": {VCPU: 32, RAMGiB: 256, Drives: 6, PriceHour: 2.256},
		"c4a-highmem-72-lssd": {VCPU: 72, RAMGiB: 576, Drives: 16, PriceHour: 5.23},
	} {
		i, ok := p.Instance(name)
		if !ok {
			t.Errorf("%s missing", name)
			continue
		}
		if i.VCPU != want.VCPU || i.RAMGiB != want.RAMGiB || i.Drives != want.Drives || i.PriceHour != want.PriceHour {
			t.Errorf("%s: %+v, want %+v", name, i, want)
		}
		if gib := i.DriveGB * 1e9 / GiB; gib < 374.9 || gib > 375.1 {
			t.Errorf("%s: %v GiB per Titanium SSD, want 375", name, gib)
		}
	}
}
