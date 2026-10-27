// Package pricing looks up live on-demand prices from free public sources, server side so that no
// browser CORS policy gets in the way, and falls back on the catalog's reference prices.
package pricing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

// Source tells where a price comes from.
type Source string

// Sources.
const (
	Live      Source = "live"      // read from the provider's prices for the region
	Reference Source = "reference" // catalog price (US East), when no live price is available
	Pending   Source = "pending"   // reference price while the live lookup runs
)

// Quote is the hourly on-demand price of one node.
type Quote struct {
	Hourly float64 `json:"hourly"`
	Source Source  `json:"source"`
}

// Fetcher reads the hourly on-demand Linux price of an instance type in a region.
type Fetcher interface {
	Fetch(ctx context.Context, region, instance string) (float64, error)
}

// Book quotes prices: live ones from a cache filled in the background, else reference ones.
type Book struct {
	Fetchers   map[string]Fetcher // per provider; a provider without one stays on reference prices
	TTL        time.Duration      // how long a live price is kept
	RetryAfter time.Duration      // how long a failed lookup stays on the reference price
	Timeout    time.Duration      // of one lookup
	Log        *slog.Logger
	Now        func() time.Time // tests replace it

	mu      sync.Mutex
	entries map[key]*entry
	wg      sync.WaitGroup
}

type key struct{ provider, region, instance string }

type entry struct {
	price   float64
	ok      bool      // price is a live one
	at      time.Time // of the last lookup, zero while the first one runs
	loading bool
}

// NewBook returns a book reading AWS prices from ec2.shop and Azure prices from the Azure Retail
// Prices API; Google Cloud has no free public equivalent and stays on reference prices.
func NewBook(log *slog.Logger) *Book {
	client := &http.Client{Timeout: 15 * time.Second}
	return &Book{
		Fetchers: map[string]Fetcher{
			"aws":   EC2Shop{Base: "https://ec2.shop", HTTP: client},
			"azure": AzureRetail{Base: "https://prices.azure.com", HTTP: client},
		},
		TTL: 12 * time.Hour, RetryAfter: 10 * time.Minute, Timeout: 10 * time.Second, Log: log,
	}
}

func (b *Book) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Quote returns the price of one node, starting a live lookup when none is cached or the cached
// one is stale; the first lookup of a price answers Pending with the reference price.
func (b *Book) Quote(provider, region string, inst catalog.Instance) Quote {
	ref := Quote{Hourly: inst.PriceHour, Source: Reference}
	f := b.Fetchers[provider]
	if f == nil {
		return ref
	}
	k := key{provider, region, inst.Name}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = map[key]*entry{}
	}
	e := b.entries[k]
	if e == nil {
		e = &entry{}
		b.entries[k] = e
		b.start(k, e, f)
	}
	if e.at.IsZero() {
		return Quote{Hourly: inst.PriceHour, Source: Pending}
	}
	keep := b.RetryAfter
	if e.ok {
		keep = b.TTL
	}
	if !e.loading && b.now().Sub(e.at) > keep {
		b.start(k, e, f)
	}
	if e.ok {
		return Quote{Hourly: e.price, Source: Live}
	}
	return ref
}

// start looks the price up in the background; b.mu is held.
func (b *Book) start(k key, e *entry, f Fetcher) {
	e.loading = true
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
		defer cancel()
		price, err := f.Fetch(ctx, k.region, k.instance)
		if err == nil && !(price > 0) {
			err = errors.New("no positive price")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		e.loading, e.at, e.ok = false, b.now(), err == nil
		if err != nil {
			if b.Log != nil {
				b.Log.Warn("live price unavailable, reference price used", "provider", k.provider, "region", k.region, "instance", k.instance, "err", err)
			}
			return
		}
		e.price = price
	}()
}

// Forget drops a cached price so that the next quote looks it up again.
func (b *Book) Forget(provider, region, instance string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key{provider, region, instance})
}

// Wait blocks until the lookups in flight are over.
func (b *Book) Wait() { b.wg.Wait() }

// maxBody bounds a price response.
const maxBody = 4 << 20

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "aerospike-sizer")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}
