package service

import (
	"strconv"
	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
	"testing"
)

func TestSubPoundWeightUsesTwoDecimalPounds(t *testing.T) {
	for _, tc := range []struct {
		kg   float64
		want string
	}{{.36, "0.80"}, {.18, "0.40"}, {.001, "0.01"}, {.4, "0.89"}} {
		spec, err := packageSpecFromResolution(inventory.PackageResolution{Complete: true, Package: &inventory.PackageSpec{Weight: tc.kg, WeightUnit: "kg", Length: 25, Width: 24, Height: 7, DimensionUnit: "cm"}})
		if err != nil {
			t.Fatal(err)
		}
		if spec.Weight != tc.want || spec.WeightUnit != "lb" || spec.ExtendWeight != "" || spec.ExtendWeightUnit != "" {
			t.Fatalf("kg=%v invalid weight: %+v", tc.kg, spec)
		}
		pounds, _ := strconv.ParseFloat(spec.Weight, 64)
		if pounds*.45359237 < tc.kg {
			t.Fatal("package weight understated")
		}
		request := shippingServicesRequest(model.Order{}, "test-warehouse", spec, false)
		if request["weight"] != tc.want || request["weightUnit"] != "lb" {
			t.Fatal("quote request changed package weight")
		}
		if _, ok := request["extendWeight"]; ok {
			t.Fatal("extra ounces must not be added to decimal pounds")
		}
		shipment, err := shipmentCreateRequest(model.Order{}, model.Quote{ChannelID: 1, ShipCompanyID: 2}, spec, temu.ShippingChannel{ChannelID: 1, ShipCompanyID: 2}, false)
		if err != nil {
			t.Fatal(err)
		}
		pack := shipment["sendRequestList"].([]any)[0].(map[string]any)
		if pack["weight"] != tc.want || pack["weightUnit"] != "lb" {
			t.Fatal("shipment and quote weights differ")
		}
	}
}
