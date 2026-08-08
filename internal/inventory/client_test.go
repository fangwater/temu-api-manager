package inventory

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSelectWarehousePrefersDPSWhenAllSKUsFit(t *testing.T) {
	decision := DecisionResponse{Complete: true, Records: []SKUDecision{
		{SKU: "A", Regions: []Region{{Region: "east", Warehouses: []Warehouse{{Key: "DPS002", Name: "DPS", Selectable: true, Available: 3}, {Key: "ARP_EAST", Selectable: true, Available: 20}}}}},
		{SKU: "B", Regions: []Region{{Region: "east", Warehouses: []Warehouse{{Key: "DPS002", Name: "DPS", Selectable: true, Available: 5}, {Key: "ARP_EAST", Selectable: true, Available: 20}}}}},
	}}
	selected, err := SelectWarehouse(decision, "east", map[string]int{"A": 2, "B": 1})
	if err != nil {
		t.Fatal(err)
	}
	if selected.WarehouseKey != "DPS002" {
		t.Fatalf("unexpected selection: %#v", selected)
	}
}

func TestSelectWarehouseRequiresOneWarehouseToCoverEverySKU(t *testing.T) {
	decision := DecisionResponse{Complete: true, Records: []SKUDecision{
		{SKU: "A", Regions: []Region{{Region: "west", Warehouses: []Warehouse{{Key: "DPS004", Selectable: true, Available: 5}, {Key: "ARP_WEST", Selectable: false}}}}},
		{SKU: "B", Regions: []Region{{Region: "west", Warehouses: []Warehouse{{Key: "DPS004", Selectable: false}, {Key: "ARP_WEST", Selectable: true, Available: 5}}}}},
	}}
	if _, err := SelectWarehouse(decision, "west", map[string]int{"A": 1, "B": 1}); err == nil {
		t.Fatal("expected manual review error")
	}
}

func TestSelectWarehouseRejectsHealthyRegionWhenOtherRegionRequiresManual(t *testing.T) {
	decision := DecisionResponse{Complete: true, Records: []SKUDecision{{
		SKU: "A", RequiresManual: true,
		Regions: []Region{
			{Region: "east", Warehouses: []Warehouse{{Key: "DPS002", Selectable: true, Available: 100}, {Key: "ARP_EAST", Selectable: true, Available: 100}}},
			{Region: "west", RequiresManual: true, Reason: "美西两仓库存小于等于50"},
		},
	}}}
	if _, err := SelectWarehouse(decision, "east", map[string]int{"A": 1}); err == nil {
		t.Fatal("expected global manual-review decision to block the healthy region")
	}
}

func TestSelectWarehouseAllowsARPOverrideWhenAllSKUsFit(t *testing.T) {
	decision := DecisionResponse{Complete: true, Records: []SKUDecision{
		{SKU: "A", Regions: []Region{{Region: "east", Warehouses: []Warehouse{{Key: "DPS002", Name: "DPS", Selectable: true, Available: 3}, {Key: "ARP_EAST", Name: "ARP", Selectable: true, Available: 20}}}}},
		{SKU: "B", Regions: []Region{{Region: "east", Warehouses: []Warehouse{{Key: "DPS002", Name: "DPS", Selectable: true, Available: 5}, {Key: "ARP_EAST", Name: "ARP", Selectable: true, Available: 20}}}}},
	}}
	selected, err := SelectWarehouse(decision, "east", map[string]int{"A": 2, "B": 1}, "ARP_EAST")
	if err != nil {
		t.Fatal(err)
	}
	if selected.WarehouseKey != "ARP_EAST" {
		t.Fatalf("unexpected override selection: %#v", selected)
	}
}

func TestSelectWarehouseRejectsOverrideThatCannotCoverEverySKU(t *testing.T) {
	decision := DecisionResponse{Complete: true, Records: []SKUDecision{
		{SKU: "A", Regions: []Region{{Region: "east", Warehouses: []Warehouse{{Key: "DPS002", Selectable: true, Available: 5}, {Key: "ARP_EAST", Selectable: true, Available: 5}}}}},
		{SKU: "B", Regions: []Region{{Region: "east", Warehouses: []Warehouse{{Key: "DPS002", Selectable: true, Available: 5}, {Key: "ARP_EAST", Selectable: false}}}}},
	}}
	if _, err := SelectWarehouse(decision, "east", map[string]int{"A": 1, "B": 1}, "ARP_EAST"); err == nil {
		t.Fatal("expected unavailable ARP override to fail")
	}
}

func TestUpdatePackageSpecForwardsExactWarehouseSKU(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch {
			t.Errorf("got method %s, want PATCH", request.Method)
		}
		if request.URL.Path != "/v1/warehouse-sku-specs/PH+H-12Pcs-Black-42cm/package" {
			t.Errorf("unexpected path: %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"success":true,"data":{"warehouse_sku":"PH+H-12Pcs-Black-42cm","length_cm":41,"width_cm":32.5,"height_cm":7,"weight_kg":1.27,"enabled":true,"complete":true}}`)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1/temu/warehouse-availability/query", time.Second)
	item, err := client.UpdatePackageSpec(context.Background(), "PH+H-12Pcs-Black-42cm", PackageSpecUpdate{LengthCM: 41, WidthCM: 32.5, HeightCM: 7, WeightKG: 1.27})
	if err != nil {
		t.Fatal(err)
	}
	if item.WarehouseSKU != "PH+H-12Pcs-Black-42cm" || !item.Complete {
		t.Fatalf("unexpected updated spec: %#v", item)
	}
}
