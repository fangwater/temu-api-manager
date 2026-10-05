package service

import (
	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
	"testing"
)

func TestHoustonFlatSelectionKeepsAccountAndReservation(t *testing.T) {
	decision := inventory.DecisionResponse{Complete: true, Records: []inventory.SKUDecision{{SKU: "hanger", Warehouses: []inventory.Warehouse{{Key: "ARP_HOUSTON", Code: "ARP06A", Name: "ARP-休斯顿", Region: "central", Active: true, QueryStatus: "succeeded", SKUFound: true, Selectable: true, Available: 100, APIBinding: &inventory.WarehouseAPIBinding{CredentialKey: "test-api", OMSAccountKey: "arp"}}}}}}
	selection, err := inventory.SelectWarehouse(decision, "auto", map[string]int{"hanger": 2})
	if err != nil || selection.WarehouseKey != "ARP_HOUSTON" {
		t.Fatalf("Houston only: %v %+v", err, selection)
	}
	account, err := fulfillmentAccountForWarehouse(decision, selection.WarehouseKey)
	if err != nil || account != "arp" {
		t.Fatalf("wrong account: %s %v", account, err)
	}
	reservation, err := fulfillmentInventoryReservationRequest("temu", "test-shop", "test-order", selection, map[string]int{"hanger": 2})
	if err != nil || reservation.WarehouseCode != "ARP06A" || reservation.Items[0].ObservedAvailable != 100 {
		t.Fatalf("wrong reservation: %+v %v", reservation, err)
	}
	if _, err = inventory.SelectWarehouse(decision, "auto", map[string]int{"hanger": 101}, "ARP_HOUSTON"); err == nil {
		t.Fatal("manual selection bypassed quantity")
	}
}

func TestHoustonCheaperForbiddenCarriersCannotEnterSelection(t *testing.T) {
	rules := model.WarehouseCarrierRules{WarehouseKey: "ARP_HOUSTON", AllowedCarrierCodes: []string{"USPS", "GOFO", "UPS", "FEDEX"}, AllowedCurrencyCodes: []string{"USD"}}
	channels := []temu.ShippingChannel{{ShippingCompanyName: "SPEEDX", EstimatedAmount: "1", EstimatedCurrencyCode: "USD"}, {ShippingCompanyName: "YANWEN", EstimatedAmount: "2", EstimatedCurrencyCode: "USD"}, {ShippingCompanyName: "unknown", EstimatedAmount: "3", EstimatedCurrencyCode: "USD"}, {ShippingCompanyName: "USPS", EstimatedAmount: "10", EstimatedCurrencyCode: "USD"}}
	allowed, rejected := filterAutomaticChannels(channels, rules)
	if len(allowed) != 1 || carrierCode(allowed[0]) != "USPS" || len(rejected) != 3 {
		t.Fatalf("invalid collection options: %+v", allowed)
	}
}
