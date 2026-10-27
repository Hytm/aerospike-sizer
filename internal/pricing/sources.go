package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// EC2Shop reads AWS on-demand Linux prices from ec2.shop.
type EC2Shop struct {
	Base string
	HTTP *http.Client
}

// Fetch returns the price of exactly this instance type: ec2.shop filters by prefix, and another
// size of the family must never pass for it.
func (s EC2Shop) Fetch(ctx context.Context, region, instance string) (float64, error) {
	body, err := get(ctx, s.HTTP, s.Base+"/?"+url.Values{"region": {region}, "filter": {instance}}.Encode())
	if err != nil {
		return 0, err
	}
	var data struct {
		Prices []struct {
			InstanceType string
			Cost         float64
		}
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	for _, p := range data.Prices {
		if strings.EqualFold(p.InstanceType, instance) && p.Cost > 0 {
			return p.Cost, nil
		}
	}
	return 0, fmt.Errorf("ec2.shop: no price for %s in %s", instance, region)
}

// AzureRetail reads Azure pay-as-you-go prices from the public Retail Prices API.
type AzureRetail struct {
	Base string
	HTTP *http.Client
}

// Fetch returns the Linux pay-as-you-go price: the API also lists Windows, Spot and Low Priority
// meters of the same SKU, all cheaper or dearer, which must not be taken for it.
func (a AzureRetail) Fetch(ctx context.Context, region, instance string) (float64, error) {
	filter := fmt.Sprintf("armRegionName eq '%s' and armSkuName eq 'Standard_%s' and priceType eq 'Consumption'", region, instance)
	body, err := get(ctx, a.HTTP, a.Base+"/api/retail/prices?"+url.Values{"$filter": {filter}}.Encode())
	if err != nil {
		return 0, err
	}
	var data struct {
		Items []struct {
			RetailPrice   float64 `json:"retailPrice"`
			CurrencyCode  string  `json:"currencyCode"`
			UnitOfMeasure string  `json:"unitOfMeasure"`
			ProductName   string  `json:"productName"`
			SkuName       string  `json:"skuName"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	price := 0.0
	for _, it := range data.Items {
		if it.CurrencyCode != "USD" || it.UnitOfMeasure != "1 Hour" || it.RetailPrice <= 0 ||
			strings.Contains(it.ProductName, "Windows") || strings.Contains(it.SkuName, "Spot") || strings.Contains(it.SkuName, "Low Priority") {
			continue
		}
		if price == 0 || it.RetailPrice < price {
			price = it.RetailPrice
		}
	}
	if price == 0 {
		return 0, fmt.Errorf("azure: no Linux pay-as-you-go price for %s in %s", instance, region)
	}
	return price, nil
}
