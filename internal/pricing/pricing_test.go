package pricing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	gate  chan struct{} // when set, Fetch waits for it to be closed
}

func (f *fakeFetcher) Fetch(context.Context, string, string) (float64, error) {
	f.mu.Lock()
	f.calls++
	gate, price, err := f.gate, f.price, f.err
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return price, err
}

func (f *fakeFetcher) set(price float64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.price, f.err = price, err
}

func (f *fakeFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
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
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.25, Live}) || f.count() != 2 {
		t.Errorf("refreshed quote = %+v after %d calls", q, f.count())
	}
}

func TestFailedLookupFallsBackOnTheReferenceAndRetries(t *testing.T) {
	clock := time.Unix(1e9, 0)
	f := &fakeFetcher{err: errors.New("down")}
	b := book(f, &clock)
	b.Quote("aws", "eu-west-3", large)
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.172, Reference}) || f.count() != 1 {
		t.Errorf("quote = %+v after %d calls", q, f.count())
	}
	f.set(0.199, nil)
	clock = clock.Add(2 * time.Minute)
	// The retry is pending like a first lookup: the interface keeps asking until the price lands.
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.172, Pending}) {
		t.Errorf("quote starting the retry = %+v", q)
	}
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.199, Live}) {
		t.Errorf("retried quote = %+v", q)
	}
}

func TestOneLookupRunsAtATime(t *testing.T) {
	clock := time.Unix(1e9, 0)
	f := &fakeFetcher{price: 0.199, gate: make(chan struct{})}
	b := book(f, &clock)
	b.Quote("aws", "eu-west-3", large)
	if q := b.Quote("aws", "eu-west-3", large); q.Source != Pending {
		t.Errorf("second quote = %+v", q)
	}
	close(f.gate)
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.199, Live}) || f.count() != 1 {
		t.Errorf("quote = %+v after %d calls", q, f.count())
	}
}

func TestNonPositivePriceIsNotLive(t *testing.T) {
	clock := time.Unix(1e9, 0)
	b := book(&fakeFetcher{price: 0}, &clock)
	b.Quote("aws", "eu-west-3", large)
	b.Wait()
	if q := b.Quote("aws", "eu-west-3", large); q != (Quote{0.172, Reference}) {
		t.Errorf("quote = %+v", q)
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
	if q := b.Quote("gcp", "europe-west1", z3); q != (Quote{1.15, Reference}) {
		t.Errorf("quote = %+v", q)
	}
	if !b.Live("aws") || b.Live("gcp") || (&Book{}).Live("aws") {
		t.Error("Live must follow the fetchers")
	}
}

func serve(t *testing.T, status int, body string, check func(*http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check(r)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestLookupErrorsAreReported(t *testing.T) {
	none := func(*http.Request) {}
	down := AWSPriceList{Base: serve(t, http.StatusBadGateway, "oops", none), HTTP: http.DefaultClient}
	if _, err := down.Fetch(context.Background(), "eu-west-3", "i4i.large"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("502: err = %v", err)
	}
	garbled := AzureRetail{Base: serve(t, http.StatusOK, `{not json`, none), HTTP: http.DefaultClient}
	if _, err := garbled.Fetch(context.Background(), "francecentral", "L8s_v3"); err == nil {
		t.Error("malformed JSON must fail")
	}
}

// The Retail Prices API answers the armSkuName filter with six meters: Linux and Windows, each
// pay-as-you-go, Spot and Low Priority; the Linux pay-as-you-go one is the price.
func TestAzureRetailSkipsWindowsSpotAndLowPriority(t *testing.T) {
	body := `{"Items":[
		{"retailPrice":0.43465,"currencyCode":"USD","unitOfMeasure":"1 Hour","armSkuName":"Standard_L16s_v3","skuName":"Standard_L16s_v3 Spot","meterName":"L16s v3 Spot","productName":"Virtual Machines Lsv3 Series Windows","type":"Consumption"},
		{"retailPrice":0.323,"currencyCode":"USD","unitOfMeasure":"1 Hour","armSkuName":"Standard_L16s_v3","skuName":"Standard_L16s_v3 Low Priority","meterName":"L16s v3 Low Priority","productName":"Virtual Machines Lsv3 Series","type":"Consumption"},
		{"retailPrice":1.616,"currencyCode":"USD","unitOfMeasure":"1 Hour","armSkuName":"Standard_L16s_v3","skuName":"Standard_L16s_v3","meterName":"L16s v3","productName":"Virtual Machines Lsv3 Series","type":"Consumption"},
		{"retailPrice":2.352,"currencyCode":"USD","unitOfMeasure":"1 Hour","armSkuName":"Standard_L16s_v3","skuName":"Standard_L16s_v3","meterName":"L16s v3","productName":"Virtual Machines Lsv3 Series Windows","type":"Consumption"},
		{"retailPrice":0.941,"currencyCode":"USD","unitOfMeasure":"1 Hour","armSkuName":"Standard_L16s_v3","skuName":"Standard_L16s_v3 Low Priority","meterName":"L16s v3 Low Priority","productName":"Virtual Machines Lsv3 Series Windows","type":"Consumption"},
		{"retailPrice":0.298637,"currencyCode":"USD","unitOfMeasure":"1 Hour","armSkuName":"Standard_L16s_v3","skuName":"Standard_L16s_v3 Spot","meterName":"L16s v3 Spot","productName":"Virtual Machines Lsv3 Series","type":"Consumption"}]}`
	base := serve(t, http.StatusOK, body, func(r *http.Request) {
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

// The price file of a region lists every instance type offered there, one entry each: the price is
// that of exactly this type, and a type the region does not offer has none.
func TestAWSPriceListReadsThisInstanceOnly(t *testing.T) {
	body := `{"manifest":{"source":"ec2-ondemand-without-sec-sel"},"regions":{"EU (Paris)":{
		"i8g xlarge EU Paris Linux":{"price":"0.3982000000","Instance Type":"i8g.xlarge","Operating System":"Linux"},
		"i8g 2xlarge EU Paris Linux":{"price":"0.7964000000","Instance Type":"i8g.2xlarge","Operating System":"Linux"}}}}`
	base := serve(t, http.StatusOK, body, func(r *http.Request) {
		if r.URL.Path != "/pricing/2.0/meteredUnitMaps/ec2/USD/current/ec2-ondemand-without-sec-sel/EU (Paris)/Linux/index.json" {
			t.Errorf("request %s", r.URL.Path)
		}
	})
	a := AWSPriceList{Base: base, HTTP: http.DefaultClient}
	if p, err := a.Fetch(context.Background(), "eu-west-3", "i8g.xlarge"); err != nil || p != 0.3982 {
		t.Errorf("price %v, %v", p, err)
	}
	if _, err := a.Fetch(context.Background(), "eu-west-3", "r6id.xlarge"); err == nil {
		t.Error("a type the region does not offer has no price")
	}
	if _, err := a.Fetch(context.Background(), "mars-1", "i8g.xlarge"); err == nil {
		t.Error("a region without a price file has no price")
	}
}

// Every AWS region of the catalog has its price file.
func TestEveryAWSRegionHasAPriceFile(t *testing.T) {
	p, _ := catalog.Get("aws")
	for _, r := range p.Regions {
		if AWSLocations[r.ID] == "" {
			t.Errorf("%s: no price file location", r.ID)
		}
	}
	if len(AWSLocations) != len(p.Regions) {
		t.Errorf("%d locations for %d regions", len(AWSLocations), len(p.Regions))
	}
}
