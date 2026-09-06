package service

import (
	"encoding/json"
	"temu-api-manager/internal/inventory"
	"testing"
)

func bindingWarehouse(key, account string) inventory.Warehouse {
	return inventory.Warehouse{Key: key, Active: true, QueryStatus: "succeeded", APIBinding: &inventory.WarehouseAPIBinding{CredentialKey: "api-test", OMSAccountKey: account}}
}
func bindingDecision(warehouses ...inventory.Warehouse) inventory.DecisionResponse {
	return inventory.DecisionResponse{Complete: true, Records: []inventory.SKUDecision{{SKU: "SKU-1", Regions: []inventory.Region{{Warehouses: warehouses}}}}}
}

func TestFulfillmentAccountUsesSelectedWarehouseAPIBinding(t *testing.T) {
	decision := bindingDecision(bindingWarehouse("ARP_WEST", "fhzarp-laundry"), bindingWarehouse("DPS004", "dps"))
	for key, want := range map[string]string{"ARP_WEST": "fhzarp-laundry", "DPS004": "dps"} {
		got, err := fulfillmentAccountForWarehouse(decision, key)
		if err != nil || got != want {
			t.Fatalf("%s got %q %v, want %q", key, got, err, want)
		}
	}
}

func TestFulfillmentAccountRejectsMissingOrConflictingBindings(t *testing.T) {
	missing := bindingWarehouse("ARP_WEST", "arp")
	missing.APIBinding = nil
	failed := bindingWarehouse("ARP_WEST", "arp")
	failed.QueryStatus = "failed"
	noCredential := bindingWarehouse("ARP_WEST", "arp")
	noCredential.APIBinding.CredentialKey = ""
	mixed := bindingDecision(bindingWarehouse("ARP_WEST", "arp"))
	mixed.Records = append(mixed.Records, bindingDecision(bindingWarehouse("ARP_WEST", "other")).Records[0])
	for name, decision := range map[string]inventory.DecisionResponse{
		"missing": bindingDecision(missing), "failed": bindingDecision(failed), "no credential": bindingDecision(noCredential),
		"invalid account":  bindingDecision(bindingWarehouse("ARP_WEST", "bad/account")),
		"absent warehouse": bindingDecision(bindingWarehouse("DPS004", "dps")),
		"duplicate":        bindingDecision(bindingWarehouse("ARP_WEST", "arp"), bindingWarehouse("ARP_WEST", "arp")),
		"mixed accounts":   mixed, "empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fulfillmentAccountForWarehouse(decision, "ARP_WEST"); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestLegacyAccountDecisionDoesNotEnableQuoting(t *testing.T) {
	var decision inventory.DecisionResponse
	if err := json.Unmarshal([]byte(`{"complete":true,"account_decision":{"configured":true,"account_key":"arp"},"records":[{"sku":"SKU-1","regions":[{"warehouses":[{"warehouse_key":"ARP_WEST","active":true,"query_status":"succeeded"}]}]}]}`), &decision); err != nil {
		t.Fatal(err)
	}
	if _, err := fulfillmentAccountForWarehouse(decision, "ARP_WEST"); err == nil {
		t.Fatal("legacy account decision must not be used")
	}
}
