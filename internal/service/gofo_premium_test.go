package service

import (
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
	"testing"
)

func TestLaundryGOFOPremium(t *testing.T) {
	for _, tc := range []struct {
		name   string
		codes  []string
		prices []float64
		want   string
	}{
		{"exact premium", []string{"GOFO", "SWIFTX", "SPEEDX"}, []float64{5.30, 5, 5.1}, "GOFO"},
		{"over premium cheapest wins", []string{"GOFO", "SWIFTX", "SPEEDX", "UPS"}, []float64{5.31, 5, 5.1, 4.9}, "UPS"},
		{"speedx cheaper beyond limit", []string{"GOFO", "SWIFTX", "SPEEDX"}, []float64{5.3, 5.2, 4.9}, "SPEEDX"},
		{"no gofo", []string{"SWIFTX", "SPEEDX"}, []float64{5.1, 5}, "SPEEDX"},
		{"no comparison carriers", []string{"GOFO", "UPS"}, []float64{5.1, 5}, "UPS"},
		{"within premium", []string{"GOFO", "SPEEDX"}, []float64{5.2, 5}, "GOFO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidates := []autoChannelCandidate{}
			for i, code := range tc.codes {
				candidates = append(candidates, autoChannelCandidate{amount: tc.prices[i], priority: i + 1, channel: temu.ShippingChannel{ShippingCompanyName: code, EstimatedCurrencyCode: "USD"}, rules: model.WarehouseCarrierRules{SelectionMode: "gofo_over_swiftx_speedx", MaxPriceDelta: .3}})
			}
			got, _, err := selectAutomaticChannel(candidates, 0)
			if err != nil || carrierCode(got.channel) != tc.want {
				t.Fatalf("got %s err %v, want %s", carrierCode(got.channel), err, tc.want)
			}
		})
	}
}
