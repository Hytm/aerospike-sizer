package catalog

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

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
				t.Errorf("%s %s: instances must go from the cheapest to the dearest", p.Key, i.Name)
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

// Google publishes its local SSDs in GiB: Z3 Titanium SSDs are 3,000 GiB (decision T14), C4A
// Titanium SSDs 375 GiB (T13); the catalog stores the decimal conversion. Their planning IOPS are
// half the published random-write IOPS: 250,000 per Z3 drive (T15), 37,500 per C4A drive (T13).
func TestGoogleDrivesAreConvertedFromGiB(t *testing.T) {
	p, _ := Get("gcp")
	for _, i := range p.Instances {
		gib, iops := 375.0, 37500.0
		if strings.HasPrefix(i.Name, "z3-") {
			gib, iops = 3000, 250000
		}
		if got := i.DriveGB * 1e9 / GiB; math.Abs(got-gib) > 0.1 {
			t.Errorf("%s: %v GiB per drive, want %v", i.Name, got, gib)
		}
		if i.IOPS != iops*float64(i.Drives) {
			t.Errorf("%s: %v IOPS, want %v per drive", i.Name, i.IOPS, iops)
		}
	}
}

// TestGCPAxion checks every C4A type against the published shape ladder and the us-east1 price
// decomposition of decision T13 (per vCPU, per GiB and per Titanium SSD); a Google price change
// updates the three rates here and in T13 together.
func TestGCPAxion(t *testing.T) {
	const perVCPU, perGiB, perDisk = 0.03085, 0.0035125, 0.0616
	disks := map[int]int{4: 1, 8: 2, 16: 4, 32: 6, 48: 10, 64: 14, 72: 16}
	p, _ := Get("gcp")
	seen := 0
	for _, i := range p.Instances {
		if !strings.HasPrefix(i.Name, "c4a-") {
			continue
		}
		seen++
		parts := strings.Split(i.Name, "-") // c4a-<family>-<vcpu>-lssd
		if len(parts) != 4 || parts[3] != "lssd" {
			t.Errorf("%s: unexpected name", i.Name)
			continue
		}
		vcpu, _ := strconv.Atoi(parts[2])
		gibPerVCPU := 4.0
		if parts[1] == "highmem" {
			gibPerVCPU = 8
		}
		if i.VCPU != vcpu || i.RAMGiB != float64(vcpu)*gibPerVCPU || i.Drives != disks[vcpu] {
			t.Errorf("%s: %+v, want %d vCPU, %v GiB, %d drives", i.Name, i, vcpu, float64(vcpu)*gibPerVCPU, disks[vcpu])
		}
		want := float64(vcpu)*perVCPU + i.RAMGiB*perGiB + float64(i.Drives)*perDisk
		if math.Abs(i.PriceHour-want) > 0.0001 {
			t.Errorf("%s: %v $/h, want %.4f", i.Name, i.PriceHour, want)
		}
		if i.IOPS != 37500*float64(i.Drives) {
			t.Errorf("%s: %v IOPS, want 37,500 per drive", i.Name, i.IOPS)
		}
	}
	if seen != 14 {
		t.Errorf("%d C4A types, want 14 (7 sizes × 2 families)", seen)
	}
}
