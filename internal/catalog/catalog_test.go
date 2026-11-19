package catalog

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestEveryProviderIsComplete(t *testing.T) {
	if len(Providers) != 3 {
		t.Fatalf("%d providers", len(Providers))
	}
	names := map[string]bool{}
	for _, p := range Providers {
		if _, ok := p.Region(p.DefaultRegion); !ok {
			t.Errorf("%s: default region %q is not listed", p.Key, p.DefaultRegion)
		}
		if PricesRegion[p.Key] == "" {
			t.Errorf("%s: no reference price region", p.Key)
		}
		for _, r := range p.Regions {
			if len(p.Offered(r.ID)) == 0 {
				t.Errorf("%s: region %s offers no instance type", p.Key, r.ID)
			}
		}
		for k, i := range p.Instances {
			if names[i.Name] {
				t.Errorf("%s is listed twice", i.Name)
			}
			names[i.Name] = true
			if i.VCPU <= 0 || i.RAMGiB <= 0 || i.Drives <= 0 || i.DriveGB <= 0 || i.IOPS <= 0 || i.PriceHour <= 0 || i.Family == "" {
				t.Errorf("%s %s: incomplete %+v", p.Key, i.Name, i)
			}
			if i.Arch != "x86_64" && i.Arch != "arm64" {
				t.Errorf("%s: arch %q", i.Name, i.Arch)
			}
			// The reference price is read in PricesRegion: the type is offered there.
			if !i.OfferedIn(PricesRegion[p.Key]) {
				t.Errorf("%s: not offered in %s, where its reference price is read", i.Name, PricesRegion[p.Key])
			}
			for _, r := range i.Regions {
				if _, ok := p.Region(r); !ok {
					t.Errorf("%s: region %q is not one of %s's", i.Name, r, p.Key)
				}
			}
			if k > 0 {
				prev := p.Instances[k-1]
				if cmp.Or(cmp.Compare(prev.PriceHour, i.PriceHour), strings.Compare(prev.Name, i.Name)) >= 0 {
					t.Errorf("%s %s after %s: instances go from the cheapest to the dearest, then by name", p.Key, i.Name, prev.Name)
				}
			}
		}
	}
}

// Offered keeps the types a region offers, in the catalog's price order, and only them.
func TestOfferedKeepsThePriceOrder(t *testing.T) {
	for _, p := range Providers {
		for _, r := range p.Regions {
			want := slices.DeleteFunc(slices.Clone(p.Instances), func(i Instance) bool { return !slices.Contains(i.Regions, r.ID) })
			if got := p.Offered(r.ID); !slices.EqualFunc(got, want, func(a, b Instance) bool { return a.Name == b.Name }) {
				t.Errorf("%s %s: offered %d types, want %d", p.Key, r.ID, len(got), len(want))
			}
		}
	}
	// Google offers no Z3 in Jakarta (gcloud-compute.com region tables, read 2026-11-19).
	p, _ := Get("gcp")
	jakarta := p.Offered("asia-southeast2")
	if len(jakarta) == 0 || len(jakarta) >= len(p.Instances) {
		t.Fatalf("%d of %d types offered in asia-southeast2", len(jakarta), len(p.Instances))
	}
	for _, i := range jakarta {
		if i.Family == "z3" {
			t.Errorf("%s is not offered in asia-southeast2", i.Name)
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

// AWS publishes the 100 % random-write IOPS of each i4i size (instance store table of
// docs.aws.amazon.com/ec2/latest/instancetypes/so.html, read 2026-11-15): 27,500 per 468 GB of
// drive, so 55,000 for the 937 GB drive and 220,000 for each 3,750 GB drive. Planning IOPS are half
// of it, the T13 rule (decision T16), as in wellness's catalog.
func TestAWSi4iPlanHalfThePublishedWriteIOPS(t *testing.T) {
	published := map[string]float64{
		"i4i.large": 27500, "i4i.xlarge": 55000, "i4i.2xlarge": 110000,
		"i4i.4xlarge": 220000, "i4i.8xlarge": 440000, "i4i.16xlarge": 880000,
	}
	p, _ := Get("aws")
	for name, write := range published {
		i, ok := p.Instance(name)
		if !ok {
			t.Errorf("%s missing", name)
			continue
		}
		if i.IOPS != write/2 {
			t.Errorf("%s: %v IOPS, want %v (half of %v published)", name, i.IOPS, write/2, write)
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
