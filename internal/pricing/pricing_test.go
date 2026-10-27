package pricing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Hytm/aerospike-sizer/internal/catalog"
)

type fakeFetcher struct {
	mu    sync.Mutex
	price float64
	err   error
	calls int
}

func (f *fakeFetcher) Fetch(context.Context, string, string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.price, f.err
}

func (f *fakeFetcher) set(price float64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.price, f.err = price, err
}

var large = catalog.Instance{Name: "i4i.large", PriceHour: 0.172}

func book(f Fetcher, clock *time.Time) *Book {
	return &Book{Fetchers: map[string]Fetcher{"aws": f}, TTL: time.Hour, RetryAfter: time.Minute, Timeout: time.Second,
		Now: func() time.Time { return *clock }}
}

func TestQuoteIsPendingThenLiveThenRefreshed(t *testing.T) {
	clock := time.Unix(1e9, 0)
	f := &fakeFetcher{price: 0.199}
	b := book(f, &clock)

	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.172, Pending}) {
		t.Errorf("first quote = %+v", q)
	}
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.199, Live}) {
		t.Errorf("cached quote = %+v", q)
	}
	f.set(0.25, nil)
	clock = clock.Add(2 * time.Hour)
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.199, Live}) {
		t.Errorf("a stale price stays quoted while it refreshes: %+v", q)
	}
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.25, Live}) || f.calls != 2 {
		t.Errorf("refreshed quote = %+v after %d calls", q, f.calls)
	}
}

func TestFailedLookupFallsBackOnTheReferenceAndRetries(t *testing.T) {
	clock := time.Unix(1e9, 0)
	f := &fakeFetcher{err: errors.New("down")}
	b := book(f, &clock)
	b.Quote("aws", "eu-west-3", large)
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.172, Reference}) || f.calls != 1 {
		t.Errorf("quote = %+v after %d calls", q, f.calls)
	}
	f.set(0.199, nil)
	clock = clock.Add(2 * time.Minute)
	b.Quote("aws", "eu-west-3", large)
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.199, Live}) {
		t.Errorf("retried quote = %+v", q)
	}
}

func TestForgetLooksThePriceUpAgain(t *testing.T) {
	clock := time.Unix(1e9, 0)
	f := &fakeFetcher{price: 0.199}
	b := book(f, &clock)
	b.Quote("aws", "eu-west-3", large)
	b.Wait()
	b.Forget("aws", "eu-west-3", "i4i.large")
	if q := b.Quote("aws", "eu-west-3", large); q.Source != Pending {
		t.Errorf("quote after forget = %+v", q)
	}
	b.Wait()
}

func TestProviderWithoutSourceQuotesTheReference(t *testing.T) {
	clock := time.Unix(1e9, 0)
	b := book(&fakeFetcher{}, &clock)
	z3 := catalog.Instance{Name: "z3-highmem-8-highlssd", PriceHour: 1.15}
	if q := b.Quote("gcp", "europe-west9", z3); q != (Quote{1.15, Reference}) {
		t.Errorf("quote = %+v", q)
	}
}

func serve(t *testing.T, body string, check func(*http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check(r)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestEC2ShopReadsTheCostOfThisInstanceOnly(t *testing.T) {
	body := `{"Prices":[{"InstanceType":"i4i.large2","Cost":9},{"InstanceType":"i4i.large","Memory":"16 GiB","Cost":0.199,"Reserved1yPrice":"0.1300"}]}`
	base := serve(t, body, func(r *http.Request) {
		if r.URL.Query().Get("region") != "eu-west-3" || r.URL.Query().Get("filter") == "" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("request %s", r.URL)
		}
	})
	s := EC2Shop{Base: base, HTTP: http.DefaultClient}
	if p, err := s.Fetch(context.Background(), "eu-west-3", "i4i.large"); err != nil || p != 0.199 {
		t.Errorf("price %v, %v", p, err)
	}
	if _, err := s.Fetch(context.Background(), "eu-west-3", "i4i.xlarge"); err == nil {
		t.Error("another instance's price must not be taken")
	}
}

func TestAzureRetailSkipsWindowsSpotAndLowPriority(t *testing.T) {
	body := `{"Items":[
		{"retailPrice":0.43465,"currencyCode":"USD","unitOfMeasure":"1 Hour","skuName":"L16s v3 Spot","productName":"Virtual Machines Lsv3 Series Windows"},
		{"retailPrice":0.323,"currencyCode":"USD","unitOfMeasure":"1 Hour","skuName":"L16s v3 Low Priority","productName":"Virtual Machines Lsv3 Series"},
		{"retailPrice":1.616,"currencyCode":"USD","unitOfMeasure":"1 Hour","skuName":"L16s v3","productName":"Virtual Machines Lsv3 Series"},
		{"retailPrice":2.352,"currencyCode":"USD","unitOfMeasure":"1 Hour","skuName":"L16s v3","productName":"Virtual Machines Lsv3 Series Windows"},
		{"retailPrice":0.298637,"currencyCode":"USD","unitOfMeasure":"1 Hour","skuName":"L16s v3 Spot","productName":"Virtual Machines Lsv3 Series"}]}`
	base := serve(t, body, func(r *http.Request) {
		want := "armRegionName eq 'francecentral' and armSkuName eq 'Standard_L16s_v3' and priceType eq 'Consumption'"
		if r.URL.Path != "/api/retail/prices" || r.URL.Query().Get("$filter") != want {
			t.Errorf("request %s", r.URL)
		}
	})
	a := AzureRetail{Base: base, HTTP: http.DefaultClient}
	if p, err := a.Fetch(context.Background(), "francecentral", "L16s_v3"); err != nil || p != 1.616 {
		t.Errorf("price %v, %v", p, err)
	}
}
