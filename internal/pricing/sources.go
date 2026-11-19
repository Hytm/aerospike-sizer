package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// AWSPriceList reads AWS on-demand Linux prices from the price files of the AWS pricing pages: one
// file per region lists every instance type offered there, which ec2.shop did not for the newest
// families in several regions (decision T19).
type AWSPriceList struct {
	Base string
	HTTP *http.Client
}

// AWSLocations names the sizer's AWS regions as the price files do.
var AWSLocations = map[string]string{
	"us-east-1": "US East (N. Virginia)", "us-west-2": "US West (Oregon)", "eu-west-1": "EU (Ireland)",
	"eu-west-3": "EU (Paris)", "eu-central-1": "EU (Frankfurt)", "ap-southeast-1": "Asia Pacific (Singapore)",
	"ap-southeast-3": "Asia Pacific (Jakarta)", "ap-northeast-1": "Asia Pacific (Tokyo)", "sa-east-1": "South America (Sao Paulo)",
}

// Fetch returns the price of exactly this instance type in the region's file.
func (a AWSPriceList) Fetch(ctx context.Context, region, instance string) (float64, error) {
	loc, ok := AWSLocations[region]
	if !ok {
		return 0, fmt.Errorf("aws: no price file for region %s", region)
	}
	body, err := get(ctx, a.HTTP, a.Base+"/pricing/2.0/meteredUnitMaps/ec2/USD/current/ec2-ondemand-without-sec-sel/"+url.PathEscape(loc)+"/Linux/index.json")
	if err != nil {
		return 0, err
	}
	var data struct {
		Regions map[string]map[string]struct {
			InstanceType string `json:"Instance Type"`
			Price        string `json:"price"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	for _, p := range data.Regions[loc] {
		if p.InstanceType == instance {
			if price, err := strconv.ParseFloat(p.Price, 64); err == nil && price > 0 {
				return price, nil
			}
		}
	}
	return 0, fmt.Errorf("aws: no price for %s in %s", instance, region)
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
