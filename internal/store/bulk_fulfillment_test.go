package store

import (
	"testing"

	"temu-api-manager/internal/model"
)

func TestBulkFulfillmentBatchStatusDoesNotStopOnFailedOrder(t *testing.T) {
	if got := bulkFulfillmentBatchStatus(3); got != "running" {
		t.Fatalf("batch status = %q, want running", got)
	}
	if got := bulkFulfillmentBatchStatus(0); got != "completed" {
		t.Fatalf("batch status = %q, want completed", got)
	}
}

func TestBulkInventoryCapacityLimitsTwoPieceSubstitutionsToNinetyOrders(t *testing.T) {
	reserved := 0
	accepted := 0
	for range 200 {
		if !bulkInventoryCapacityAllows(180, reserved, 2) {
			continue
		}
		reserved += 2
		accepted++
	}
	if accepted != 90 || reserved != 180 || bulkInventoryCapacityAllows(180, reserved, 2) {
		t.Fatalf("accepted=%d reserved=%d, want 90 orders and 180 units", accepted, reserved)
	}
}

func TestNormalizeBulkInventoryReservationItemsAggregatesRecipeAndSorts(t *testing.T) {
	items, err := normalizeBulkInventoryReservationItems([]model.BulkInventoryReservationItem{
		{WarehouseSKU: " SKU-B ", Quantity: 1, AvailableStock: 20},
		{WarehouseSKU: "SKU-A", Quantity: 1, AvailableStock: 180},
		{WarehouseSKU: "SKU-A", Quantity: 1, AvailableStock: 180},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].WarehouseSKU != "SKU-A" || items[0].Quantity != 2 || items[0].AvailableStock != 180 ||
		items[1].WarehouseSKU != "SKU-B" || items[1].Quantity != 1 {
		t.Fatalf("unexpected normalized reservation: %#v", items)
	}
}
