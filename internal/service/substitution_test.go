package service

import (
	"testing"

	"temu-api-manager/internal/inventory"
)

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
