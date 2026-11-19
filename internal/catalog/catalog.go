// Package catalog holds the cloud providers, regions and NVMe instance types the sizer compares.
package catalog

import (
	_ "embed"
	"encoding/json"
	"slices"
)

// GiB is a gibibyte: memory is counted in binary units, as providers publish it.
const GiB = 1 << 30

// HoursPerMonth turns hourly prices into monthly ones, as the providers' calculators do; the
// network traffic is counted over the same month.
const HoursPerMonth = 730

// Instance is a machine type with local NVMe suitable for Aerospike.
type Instance struct {
	Name    string  `json:"name"`
	Family  string  `json:"family"` // i4i, i8g, c4a, z3, Lsv3… as aerospike-wellness names them
	Arch    string  `json:"arch"`   // x86_64 or arm64
	VCPU    int     `json:"vcpu"`
	RAMGiB  float64 `json:"ram_gib"`
	Drives  int     `json:"drives"`
	DriveGB float64 `json:"drive_gb"` // decimal GB (a drive published in GiB is converted)
	// IOPS is the planning figure per node, in 4 KiB blocks: half the 100 % random-write IOPS the
	// provider publishes for the local drives of every AWS and Google type (decision T13, extended
	// to AWS by T16), 60,000 per disk for Azure, which publishes no write figure (T16).
	IOPS      float64  `json:"iops"`
	PriceHour float64  `json:"price_hour"` // reference on-demand Linux price in PricesRegion
	Regions   []string `json:"regions"`    // the provider's regions that offer the type (T19)
}

// OfferedIn tells whether the type is offered in a region of its provider.
func (i Instance) OfferedIn(region string) bool { return slices.Contains(i.Regions, region) }

// RAMBytes returns the memory in bytes.
func (i Instance) RAMBytes() float64 { return i.RAMGiB * GiB }

// StorageBytes returns the raw local NVMe capacity in bytes.
func (i Instance) StorageBytes() float64 { return float64(i.Drives) * i.DriveGB * 1e9 }

// Region is a provider region.
type Region struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Provider is a cloud provider with its regions and instance types (cheapest first).
type Provider struct {
	Key           string     `json:"key"`
	Label         string     `json:"label"`
	Color         string     `json:"color"`
	DefaultRegion string     `json:"default_region"`
	Regions       []Region   `json:"regions"`
	Instances     []Instance `json:"instances"`
}

// Region returns a region of the provider.
func (p *Provider) Region(id string) (Region, bool) {
	for _, r := range p.Regions {
		if r.ID == id {
			return r, true
		}
	}
	return Region{}, false
}

// Offered returns the provider's instance types offered in a region, cheapest first.
func (p *Provider) Offered(region string) []Instance {
	var out []Instance
	for _, i := range p.Instances {
		if i.OfferedIn(region) {
			out = append(out, i)
		}
	}
	return out
}

// Instance returns an instance type of the provider.
func (p *Provider) Instance(name string) (Instance, bool) {
	for _, i := range p.Instances {
		if i.Name == name {
			return i, true
		}
	}
	return Instance{}, false
}

//go:embed catalog.json
var catalogJSON []byte

// Providers is the embedded catalog, in display order.
var Providers = mustProviders()

// PricesAsOf is the date the reference prices were read: every one of them was checked against the
// providers' published prices then (decisions T6, T13 and T19).
const PricesAsOf = "2026-11-19"

// PricesRegion is the region whose public prices the reference holds, per provider (US East).
var PricesRegion = map[string]string{"aws": "us-east-1", "azure": "eastus", "gcp": "us-east1"}

// Get returns a provider by key.
func Get(key string) (*Provider, bool) {
	for i := range Providers {
		if Providers[i].Key == key {
			return &Providers[i], true
		}
	}
	return nil, false
}

func mustProviders() []Provider {
	var out []Provider
	if err := json.Unmarshal(catalogJSON, &out); err != nil {
		panic(err)
	}
	return out
}
