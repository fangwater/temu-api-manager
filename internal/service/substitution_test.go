package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
)

func TestSingleUnitSubstitutionMatchUsesOriginalOrderQuantity(t *testing.T) {
	combination := inventory.SKUCombination{
		SubstituteForSKU: "SKU-20",
		Items:            []inventory.SKUCombinationItem{{WarehouseSKU: "SKU-10", Quantity: 2}},
	}
	catalog := map[string]inventory.SKUCombination{"SKU-20": combination}

	match, ok := singleUnitSubstitutionMatch(model.Order{Lines: []model.OrderLine{{ExtCode: " SKU-20 ", Quantity: 1}}}, catalog)
	if !ok || match.WarehouseSKU != "SKU-20" || match.Quantity != 1 || len(match.Combination.Items) != 1 || match.Combination.Items[0].Quantity != 2 {
		t.Fatalf("unexpected single-unit match: ok=%v match=%#v", ok, match)
	}

	for name, order := range map[string]model.Order{
		"multiple units": {Lines: []model.OrderLine{{ExtCode: "SKU-20", Quantity: 2}}},
		"multiple lines": {Lines: []model.OrderLine{{ExtCode: "SKU-20", Quantity: 1}, {ExtCode: "OTHER", Quantity: 1}}},
		"unknown SKU":    {Lines: []model.OrderLine{{ExtCode: "OTHER", Quantity: 1}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, matched := singleUnitSubstitutionMatch(order, catalog); matched {
				t.Fatal("order should remain in the manual queue")
			}
		})
	}
}

func TestExpandSubstitutionQuantitiesMultipliesRecipe(t *testing.T) {
	combination := inventory.SKUCombination{Items: []inventory.SKUCombinationItem{
		{WarehouseSKU: "SKU-10", Quantity: 2},
		{WarehouseSKU: "CLIP", Quantity: 1},
	}}
	got := expandSubstitutionQuantities(combination, 3)
	if got["SKU-10"] != 6 || got["CLIP"] != 3 {
		t.Fatalf("unexpected quantities: %#v", got)
	}
}

func TestValidateSubstitutionCombinationRequiresDimensionsWeightAndMembers(t *testing.T) {
	valid := inventory.SKUCombination{
		Enabled: true, SubstituteForSKU: "SKU-20", LengthCM: 20, WidthCM: 10, HeightCM: 5, WeightKG: 1,
		Items: []inventory.SKUCombinationItem{{WarehouseSKU: "SKU-10", Quantity: 2}},
	}
	if err := validateSubstitutionCombination(valid); err != nil {
		t.Fatal(err)
	}
	missingWeight := valid
	missingWeight.WeightKG = 0
	if err := validateSubstitutionCombination(missingWeight); err == nil || !strings.Contains(err.Error(), "重量") {
		t.Fatalf("expected weight error, got %v", err)
	}
	missingItems := valid
	missingItems.Items = nil
	if err := validateSubstitutionCombination(missingItems); err == nil || !strings.Contains(err.Error(), "组合成员") {
		t.Fatalf("expected member error, got %v", err)
	}
}

func TestSubstitutionOMSPairingRequestUsesCombinationRecipe(t *testing.T) {
	combination := inventory.SKUCombination{
		SubstituteForSKU: "SKU-20",
		Items: []inventory.SKUCombinationItem{
			{WarehouseSKU: "SKU-10", Quantity: 1},
			{WarehouseSKU: "CLIP", Quantity: 1},
			{WarehouseSKU: "SKU-10", Quantity: 1},
		},
	}
	request := substitutionOMSPairingRequest("arp", combination)
	if request.Account != "arp" || request.PlatformSKU != "SKU-20" || len(request.Items) != 2 ||
		request.Items[0].SystemSKU != "CLIP" || request.Items[0].Quantity != 1 ||
		request.Items[1].SystemSKU != "SKU-10" || request.Items[1].Quantity != 2 {
		t.Fatalf("unexpected pairing request: %#v", request)
	}
}

func TestSubstitutionProblemMessagesPreservesClearUniqueReasons(t *testing.T) {
	got := substitutionProblemMessages([]error{errors.New("DPS002: 库存不足"), errors.New("DPS002: 库存不足"), errors.New("ARP_EAST: 产品配对未审核")})
	if len(got) != 2 || got[0] != "DPS002: 库存不足" || got[1] != "ARP_EAST: 产品配对未审核" {
		t.Fatalf("unexpected reasons: %#v", got)
	}
}

func TestSelectSubstitutionPriceQuotesAppliesPriorityBeforeReducingWarehouse(t *testing.T) {
	candidates := []substitutionPriceCandidate{
		substitutionCandidate("DPS002", "SpeedX", 3.03, 3),
		substitutionCandidate("DPS002", "GOFO", 3.24, 1),
		substitutionCandidate("DPS004", "GOFO", 4.34, 1),
	}

	quotes := selectSubstitutionPriceQuotes(candidates)
	if len(quotes) != 2 {
		t.Fatalf("quote count = %d, want 2: %#v", len(quotes), quotes)
	}
	if quotes[0].WarehouseKey != "DPS002" || quotes[0].ShippingCompany != "GOFO" || quotes[0].Amount != 3.24 {
		t.Fatalf("expected DPS002 GOFO recommendation, got %#v", quotes[0])
	}
}

func TestSelectSubstitutionPriceQuotesUsesCheapestWhenPriorityExceedsPriceWindow(t *testing.T) {
	candidates := []substitutionPriceCandidate{
		substitutionCandidate("DPS002", "SpeedX", 3.03, 3),
		substitutionCandidate("DPS002", "GOFO", 3.54, 1),
	}

	quotes := selectSubstitutionPriceQuotes(candidates)
	if len(quotes) != 1 || quotes[0].ShippingCompany != "SpeedX" {
		t.Fatalf("expected SpeedX outside the $0.50 priority window, got %#v", quotes)
	}
}

func substitutionCandidate(warehouseKey, carrier string, amount float64, priority int) substitutionPriceCandidate {
	channelID := int64(priority)
	channel := temu.ShippingChannel{
		ChannelID: channelID, ShipCompanyID: channelID + 100,
		ShippingCompanyName: carrier, EstimatedAmount: fmt.Sprintf("%.2f", amount), EstimatedCurrencyCode: "USD",
	}
	return substitutionPriceCandidate{
		candidate: autoChannelCandidate{warehouseKey: warehouseKey, temuWarehouseID: "temu-" + warehouseKey, channel: channel, amount: amount, priority: priority},
		quote:     SubstitutionPriceQuote{WarehouseKey: warehouseKey, TemuWarehouseID: "temu-" + warehouseKey, ShippingCompany: carrier, ChannelID: channelID, ShipCompanyID: channelID + 100, Amount: amount, Currency: "USD"},
	}
}

func TestSelectWarehouseForPriceComparisonAllowsLowTotalManualDecision(t *testing.T) {
	decision := inventory.DecisionResponse{Complete: true, Records: []inventory.SKUDecision{{
		SKU: "SKU-20", RequiresManual: true, DecisionCode: "MANUAL_LOW_TOTAL_STOCK",
		Regions: []inventory.Region{{Region: "west", Warehouses: []inventory.Warehouse{
			{Key: "DPS004", Selectable: false},
			{Key: "ARP_WEST", Name: "ARP West", Selectable: true, Available: 31},
		}}},
	}}}
	selected, err := selectWarehouseForPriceComparison(decision, map[string]int{"SKU-20": 1}, "ARP_WEST")
	if err != nil {
		t.Fatal(err)
	}
	if selected.WarehouseKey != "ARP_WEST" {
		t.Fatalf("unexpected selection: %#v", selected)
	}
}

func TestSelectWarehouseForPriceComparisonRejectsInsufficientStock(t *testing.T) {
	decision := inventory.DecisionResponse{Complete: true, Records: []inventory.SKUDecision{{
		SKU: "SKU-10", Regions: []inventory.Region{{Region: "east", Warehouses: []inventory.Warehouse{
			{Key: "DPS002", Selectable: true, Available: 1},
		}}},
	}}}
	if _, err := selectWarehouseForPriceComparison(decision, map[string]int{"SKU-10": 2}, "DPS002"); err == nil {
		t.Fatal("expected insufficient stock error")
	}
}

func TestSelectWarehouseForPriceComparisonRequiresEveryRequestedSKURecord(t *testing.T) {
	decision := inventory.DecisionResponse{Complete: true, Records: []inventory.SKUDecision{{
		SKU: "SKU-10", Regions: []inventory.Region{{Region: "east", Warehouses: []inventory.Warehouse{{Key: "DPS002", Selectable: true, Available: 5}}}},
	}}}
	_, err := selectWarehouseForPriceComparison(decision, map[string]int{"SKU-10": 1, "CLIP": 1}, "DPS002")
	if err == nil || !strings.Contains(err.Error(), "CLIP") {
		t.Fatalf("expected missing SKU record error, got %v", err)
	}
}
