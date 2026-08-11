package service

import (
	"strings"
	"testing"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
)

func TestBuildSplitPackagesBalancesAndCoversOrder(t *testing.T) {
	weightA, lengthA, widthA, heightA := 0.8, 20.0, 10.0, 5.0
	weightB, lengthB, widthB, heightB := 1.2, 30.0, 12.0, 8.0
	order := model.Order{ParentOrderSN: "PO-1", Lines: []model.OrderLine{
		{OrderSN: "O-1", ExtCode: "SKU-A", GoodsName: "A", Quantity: 4},
		{OrderSN: "O-2", ExtCode: "SKU-B", GoodsName: "B", Quantity: 3},
	}}
	resolution := inventory.PackageResolution{Complete: true, Items: []inventory.PackageResolutionItem{
		{WarehouseSKU: "SKU-A", Complete: true, WeightKG: &weightA, LengthCM: &lengthA, WidthCM: &widthA, HeightCM: &heightA},
		{WarehouseSKU: "SKU-B", Complete: true, WeightKG: &weightB, LengthCM: &lengthB, WidthCM: &widthB, HeightCM: &heightB},
	}}

	packages, warnings, err := buildSplitPackages(order, resolution, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 3 || len(warnings) == 0 {
		t.Fatalf("unexpected split result: packages=%d warnings=%#v", len(packages), warnings)
	}
	actual := map[string]int{}
	minUnits, maxUnits := 1000, 0
	for index, item := range packages {
		if item.Number != index+1 || item.NeedsMeasurement || item.WeightKG <= 0 || item.LengthCM <= 0 || item.WidthCM <= 0 || item.HeightCM <= 0 {
			t.Fatalf("invalid package %d: %#v", index+1, item)
		}
		units := 0
		for _, line := range item.Items {
			actual[line.OrderSN] += line.Quantity
			units += line.Quantity
		}
		if units < minUnits {
			minUnits = units
		}
		if units > maxUnits {
			maxUnits = units
		}
	}
	if actual["O-1"] != 4 || actual["O-2"] != 3 {
		t.Fatalf("order quantities were not preserved: %#v", actual)
	}
	if maxUnits-minUnits > 1 {
		t.Fatalf("unit counts are not balanced: min=%d max=%d", minUnits, maxUnits)
	}
}

func TestBuildSplitPackagesMarksMissingSpecsForMeasurement(t *testing.T) {
	order := model.Order{Lines: []model.OrderLine{{OrderSN: "O-1", ExtCode: "SKU-A", Quantity: 2}}}
	packages, _, err := buildSplitPackages(order, inventory.PackageResolution{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range packages {
		if !item.NeedsMeasurement || item.WeightKG != 0 || item.LengthCM != 0 {
			t.Fatalf("missing specs must require measurement: %#v", item)
		}
	}
}

func TestBuildSplitPackagesAllowsOnePackagePerUnit(t *testing.T) {
	order := model.Order{Lines: []model.OrderLine{{OrderSN: "O-1", ExtCode: "SKU-A", Quantity: 14}}}
	packages, _, err := buildSplitPackages(order, inventory.PackageResolution{}, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 14 {
		t.Fatalf("got %d packages, want 14", len(packages))
	}
	for _, item := range packages {
		if len(item.Items) != 1 || item.Items[0].Quantity != 1 {
			t.Fatalf("expected one unit per package: %#v", item)
		}
	}
}

func TestValidateSplitPackagesRejectsQuantityMismatch(t *testing.T) {
	order := model.Order{Lines: []model.OrderLine{{OrderSN: "O-1", Quantity: 3}}}
	packages := []SplitPackage{
		{WeightKG: 1, LengthCM: 10, WidthCM: 10, HeightCM: 10, Items: []SplitPackageItem{{OrderSN: "O-1", Quantity: 1}}},
		{WeightKG: 1, LengthCM: 10, WidthCM: 10, HeightCM: 10, Items: []SplitPackageItem{{OrderSN: "O-1", Quantity: 1}}},
	}
	_, err := validateSplitPackages(order, packages)
	if err == nil || !strings.Contains(err.Error(), "expected 3") {
		t.Fatalf("expected allocation mismatch, got %v", err)
	}
}

func TestSplitShippingServicesRequestIncludesParentAndSignature(t *testing.T) {
	request := splitShippingServicesRequest(
		"PO-1",
		"WH-1",
		model.PackageSpec{Weight: "2", WeightUnit: "lb", Length: "10", Width: "8", Height: "4", DimensionUnit: "in"},
		[]SplitPackageItem{{OrderSN: "O-1", Quantity: 2, GoodsID: 10, SKUID: 20}},
		true,
	)
	if request["warehouseId"] != "WH-1" || request["signatureOnDelivery"] != true {
		t.Fatalf("unexpected request header fields: %#v", request)
	}
	items, ok := request["shipOrderInfoList"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected order items: %#v", request["shipOrderInfoList"])
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["parentOrderSn"] != "PO-1" || item["orderSn"] != "O-1" || item["quantity"] != 2 {
		t.Fatalf("unexpected order item: %#v", items[0])
	}
}

func TestSelectSplitCarrierQuoteAppliesSignaturePolicy(t *testing.T) {
	channels := temu.ShippingServicesResult{Available: []temu.ShippingChannel{
		{ChannelID: 1, ShippingCompanyName: "GOFO", EstimatedAmount: "1.00", EstimatedCurrencyCode: "USD", InfoNeeded: []string{"signServiceId"}},
		{ChannelID: 2, ShippingCompanyName: "GOFO", EstimatedAmount: "2.25", EstimatedCurrencyCode: "USD"},
		{ChannelID: 3, ShippingCompanyName: "FedEx", EstimatedAmount: "8.50", EstimatedCurrencyCode: "USD", SignServiceID: 99},
	}}

	gofo := selectSplitCarrierQuote("GOFO", channels)
	if !gofo.Available || gofo.ChannelID != 2 || gofo.Amount != 2.25 || gofo.SignatureRequired || !gofo.ProofOfDeliveryIncluded {
		t.Fatalf("unexpected GOFO quote: %#v", gofo)
	}
	fedex := selectSplitCarrierQuote("FEDEX", channels)
	if !fedex.Available || fedex.ChannelID != 3 || !fedex.SignatureRequired || fedex.ProofOfDeliveryIncluded {
		t.Fatalf("unexpected FEDEX quote: %#v", fedex)
	}

	signedOnly := selectSplitCarrierQuote("GOFO", temu.ShippingServicesResult{Available: channels.Available[:1]})
	if signedOnly.Available {
		t.Fatalf("signature-required GOFO channel must be rejected: %#v", signedOnly)
	}
}

func TestSplitTotalsRequireEveryPackage(t *testing.T) {
	first, second := 1.20, 2.30
	packages := []SplitPackageQuote{
		{
			Package:           SplitPackage{Number: 1},
			RecommendedAmount: &first,
			Carriers: []SplitCarrierQuote{
				{CarrierCode: "GOFO", Available: true, Amount: first},
				{CarrierCode: "USPS", Available: true, Amount: 3.10},
			},
		},
		{
			Package:           SplitPackage{Number: 2},
			RecommendedAmount: &second,
			Carriers: []SplitCarrierQuote{
				{CarrierCode: "GOFO", Available: true, Amount: second},
				{CarrierCode: "USPS", Available: false},
			},
		},
	}
	totals := splitCarrierTotals(packages, []string{"GOFO", "USPS"})
	if len(totals) != 2 || !totals[0].Available || totals[0].Amount == nil || *totals[0].Amount != 3.50 {
		t.Fatalf("unexpected GOFO total: %#v", totals)
	}
	if totals[1].Available || len(totals[1].UnavailablePackages) != 1 || totals[1].UnavailablePackages[0] != 2 {
		t.Fatalf("unexpected USPS total: %#v", totals[1])
	}
	mixed := splitMixedTotal(packages)
	if mixed == nil || *mixed != 3.50 {
		t.Fatalf("unexpected mixed total: %v", mixed)
	}
}
