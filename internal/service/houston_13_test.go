package service

import (
	"reflect"
	"testing"

	"temu-api-manager/internal/inventory"
)

func TestHouston13SelectionAndReservationRetainPhysicalAccount(t *testing.T) {
	decision := inventory.DecisionResponse{Complete: true, Records: []inventory.SKUDecision{{SKU: "test-sku", Warehouses: []inventory.Warehouse{{Key: "ARP_HOUSTON_13", Code: "ARP03", Name: "ARP-休斯顿13号仓", Region: "central", Active: true, QueryStatus: "succeeded", SKUFound: true, Selectable: true, Available: 100, APIBinding: &inventory.WarehouseAPIBinding{CredentialKey: "test-api", OMSAccountKey: "test-account"}}}}}}
	for _, region := range []string{"auto", "central"} {
		selection, err := inventory.SelectWarehouse(decision, region, map[string]int{"test-sku": 2})
		if err != nil || selection.WarehouseKey != "ARP_HOUSTON_13" {
			t.Fatalf("%s selection failed: %+v %v", region, selection, err)
		}
		account, err := fulfillmentAccountForWarehouse(decision, selection.WarehouseKey)
		if err != nil || account != "test-account" {
			t.Fatalf("wrong account: %s %v", account, err)
		}
		reservation, err := fulfillmentInventoryReservationRequest("temu", "test-shop", "test-order", selection, map[string]int{"test-sku": 2})
		if err != nil || reservation.WarehouseCode != "ARP03" || reservation.Items[0].ObservedAvailable != 100 {
			t.Fatalf("wrong reservation: %+v %v", reservation, err)
		}
	}
	if _, err := inventory.SelectWarehouse(decision, "auto", map[string]int{"test-sku": 101}, "ARP_HOUSTON_13"); err == nil {
		t.Fatal("warehouse 13 bypassed quantity check")
	}
	keys, err := quoteWarehouseKeys("central", "")
	if err != nil || !reflect.DeepEqual(keys, []string{"ARP_HOUSTON", "ARP_HOUSTON_13"}) {
		t.Fatalf("central quote warehouses: %v %v", keys, err)
	}
}
