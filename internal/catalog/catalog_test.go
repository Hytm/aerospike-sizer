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
		if PricesRegion[p.Key] == "" || PricesTerms[p.Key] == "" {
			t.Errorf("%s: no reference price region or one-year terms", p.Key)
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
			checkCommitment(t, p.Key, i)
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

// checkCommitment checks the one-year commitment rate of a type: a plausible discount on the
// on-demand price (20 to 50 %, as aerospike-wellness checks it), a Savings Plan only for the AWS
// types without a reserved instance (i7ie, i8g), and the rate each pricing basis takes (T21).
func checkCommitment(t *testing.T, provider string, i Instance) {
	t.Helper()
	if discount := 1 - i.Reserved1YHour/i.PriceHour; discount < 0.2 || discount > 0.5 {
		t.Errorf("%s: one-year rate %v for %v on demand, a %.1f %% discount", i.Name, i.Reserved1YHour, i.PriceHour, discount*100)
	}
	want := ""
	if provider == "aws" && (i.Family == "i7ie" || i.Family == "i8g") {
		want = "savings-plan"
	}
	if i.ReservedVia != want {
		t.Errorf("%s: reserved via %q, want %q", i.Name, i.ReservedVia, want)
	}
	if i.Rate(OnDemand) != i.PriceHour || i.Rate(Commitment1Y) != i.Reserved1YHour || i.Rate("") != i.PriceHour {
		t.Errorf("%s: rates %v on demand, %v for one year", i.Name, i.Rate(OnDemand), i.Rate(Commitment1Y))
	}
}

// A type without a one-year rate is priced on demand on either basis.
func TestRateFallsBackOnDemand(t *testing.T) {
	i := Instance{PriceHour: 1}
	if i.Rate(Commitment1Y) != 1 || i.Rate(OnDemand) != 1 {
		t.Errorf("rates %v, %v", i.Rate(Commitment1Y), i.Rate(OnDemand))
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
	// AWS offers no r6id in Paris (its price files), Google no Z3 or C3 in Jakarta
	// (gcloud-compute.com region tables), both read 2026-11-19.
	for _, c := range []struct{ provider, region, family string }{
		{"aws", "eu-west-3", "r6id"}, {"gcp", "asia-southeast2", "z3"}, {"gcp", "asia-southeast2", "c3"},
	} {
		p, _ := Get(c.provider)
		offered := p.Offered(c.region)
		if len(offered) == 0 || len(offered) >= len(p.Instances) {
			t.Errorf("%d of %d types offered in %s", len(offered), len(p.Instances), c.region)
		}
		for _, i := range offered {
			if i.Family == c.family {
				t.Errorf("%s is not offered in %s", i.Name, c.region)
			}
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
// Titanium SSDs and C3 Local SSDs 375 GiB (T13, T19); the catalog stores the decimal conversion.
func TestGoogleDrivesAreConvertedFromGiB(t *testing.T) {
	p, _ := Get("gcp")
	for _, i := range p.Instances {
		gib := 375.0
		if i.Family == "z3" {
			gib = 3000
		}
		if got := i.DriveGB * 1e9 / GiB; math.Abs(got-gib) > 0.1 {
			t.Errorf("%s: %v GiB per drive, want %v", i.Name, got, gib)
		}
	}
}

// publishedWriteIOPS are the 100 % random-write IOPS, 4 KiB, over all the local drives, that AWS and
// Google publish for the types of the catalog: AWS instance store tables (storage optimized for i4i,
// i7ie, i8g, i3en and im4gn, memory optimized for r6id); Google "Local SSD" tables, Titanium SSD for
// C4A and Z3, NVMe by number of disks for C3; all read again 2026-11-19. The same table as
// aerospike-wellness's: both tools plan half of it (decisions T13, T16, T19).
var publishedWriteIOPS = map[string]float64{
	"i4i.large": 27500, "i4i.xlarge": 55000, "i4i.2xlarge": 110000, "i4i.4xlarge": 220000, "i4i.8xlarge": 440000,
	"i4i.12xlarge": 660000, "i4i.16xlarge": 880000, "i4i.24xlarge": 1320000, "i4i.32xlarge": 1760000,
	"i7ie.large": 43333, "i7ie.xlarge": 86666, "i7ie.2xlarge": 173332, "i7ie.3xlarge": 260000, "i7ie.6xlarge": 520000,
	"i7ie.12xlarge": 1040000, "i7ie.18xlarge": 1560000, "i7ie.24xlarge": 2080000, "i7ie.48xlarge": 4160000,
	"i8g.large": 41250, "i8g.xlarge": 82500, "i8g.2xlarge": 165000, "i8g.4xlarge": 330000, "i8g.8xlarge": 660000,
	"i8g.12xlarge": 990000, "i8g.16xlarge": 1320000, "i8g.24xlarge": 1980000, "i8g.48xlarge": 3960000,
	"i3en.large": 32500, "i3en.xlarge": 65000, "i3en.2xlarge": 130000, "i3en.3xlarge": 200000, "i3en.6xlarge": 400000,
	"i3en.12xlarge": 800000, "i3en.24xlarge": 1600000,
	"r6id.large": 16771, "r6id.xlarge": 33542, "r6id.2xlarge": 67084, "r6id.4xlarge": 134167, "r6id.8xlarge": 268334,
	"r6id.12xlarge": 402500, "r6id.16xlarge": 536668, "r6id.24xlarge": 805000, "r6id.32xlarge": 1073336,
	"im4gn.large": 25000, "im4gn.xlarge": 50000, "im4gn.2xlarge": 100000, "im4gn.4xlarge": 200000,
	"im4gn.8xlarge": 400000, "im4gn.16xlarge": 800000,
	"z3-highmem-8-highlssd": 500000, "z3-highmem-16-highlssd": 1000000, "z3-highmem-22-highlssd": 1500000,
	"z3-highmem-32-highlssd": 2000000, "z3-highmem-44-highlssd": 3000000, "z3-highmem-88-highlssd": 6000000,
	"c3-standard-4-lssd": 90000, "c3-standard-8-lssd": 180000, "c3-standard-22-lssd": 360000, "c3-standard-44-lssd": 360000,
	"c3-standard-88-lssd": 800000, "c3-standard-176-lssd": 1600000,
	"c4a-standard-4-lssd": 75000, "c4a-highmem-4-lssd": 75000, "c4a-standard-8-lssd": 150000, "c4a-highmem-8-lssd": 150000,
	"c4a-standard-16-lssd": 300000, "c4a-highmem-16-lssd": 300000, "c4a-standard-32-lssd": 450000, "c4a-highmem-32-lssd": 450000,
	"c4a-standard-48-lssd": 750000, "c4a-highmem-48-lssd": 750000, "c4a-standard-64-lssd": 1050000, "c4a-highmem-64-lssd": 1050000,
	"c4a-standard-72-lssd": 1200000, "c4a-highmem-72-lssd": 1200000,
}

// Every AWS and Google type plans half its published write IOPS (decision T13, T16 for AWS);
// Azure publishes no write figure, and its Lsv3 and Lasv3 plan 60,000 per disk (T16, T19).
func TestPlanningIOPSAreHalfThePublishedWriteIOPS(t *testing.T) {
	seen := 0
	for _, p := range Providers {
		for _, i := range p.Instances {
			if p.Key == "azure" {
				if i.IOPS != 60000*float64(i.Drives) {
					t.Errorf("%s: %v IOPS, want 60,000 per disk", i.Name, i.IOPS)
				}
				continue
			}
			write, ok := publishedWriteIOPS[i.Name]
			if !ok {
				t.Errorf("%s: its published write IOPS are not in the table", i.Name)
				continue
			}
			seen++
			if i.IOPS != write/2 {
				t.Errorf("%s: %v IOPS, want %v (half of %v published)", i.Name, i.IOPS, write/2, write)
			}
		}
	}
	if seen != len(publishedWriteIOPS) {
		t.Errorf("%d of the %d types of the table are in the catalog", seen, len(publishedWriteIOPS))
	}
}

// TestGCPAxion checks every C4A type against the published shape ladder and the us-east1 price
// decomposition of decision T13 (per vCPU, per GiB and per Titanium SSD); a Google price change
// updates the three rates here, in T13 and in aerospike-wellness together.
func TestGCPAxion(t *testing.T) {
	const perVCPU, perGiB, perDisk = 0.03085, 0.0035125, 0.0616
	disks := map[int]int{4: 1, 8: 2, 16: 4, 32: 6, 48: 10, 64: 14, 72: 16}
	p, _ := Get("gcp")
	seen := 0
	for _, i := range p.Instances {
		if i.Family != "c4a" {
			continue
		}
		seen++
		parts := strings.Split(i.Name, "-") // c4a-<family>-<vcpu>-lssd
		if len(parts) != 4 || parts[3] != "lssd" || i.Arch != "arm64" {
			t.Errorf("%s: unexpected name or arch %q", i.Name, i.Arch)
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
	}
	if seen != 14 {
		t.Errorf("%d C4A types, want 14 (7 sizes × 2 families)", seen)
	}
}
