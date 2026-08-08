package store

import (
	"strings"
	"testing"

	"temu-api-manager/internal/model"
)

func TestLogicalWarehouseKey(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "DPSNY002美东", want: "DPS002"},
		{name: "DPSCA004美西", want: "DPS004"},
		{name: "ARP 美仓-美东仓", want: "ARP_EAST"},
		{name: "ARP WEST", want: "ARP_WEST"},
		{name: "PG1955", want: "PG1955"},
		{name: "PA30", want: "PA30"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := logicalWarehouseKey(model.Warehouse{Name: test.name, ID: "WH-test"}); got != test.want {
				t.Fatalf("logicalWarehouseKey(%q) = %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func TestAutoFulfillmentClaimOrderPrioritizesInFlightWork(t *testing.T) {
	ordered := []string{
		"WHEN 'confirming' THEN 0",
		"WHEN 'waiting_label' THEN 1",
		"WHEN 'running' THEN 2",
		"WHEN 'failed' THEN 3",
		"WHEN 'queued' THEN 4",
		"WHEN 'waiting_oms' THEN 5",
	}
	previous := -1
	for _, fragment := range ordered {
		position := strings.Index(autoFulfillmentClaimOrder, fragment)
		if position < 0 {
			t.Fatalf("claim order is missing %q", fragment)
		}
		if position <= previous {
			t.Fatalf("claim priority is out of order at %q", fragment)
		}
		previous = position
	}
	if !strings.Contains(autoFulfillmentClaimOrder, "THEN j.updated_at END ASC NULLS LAST") {
		t.Fatal("in-flight jobs must be ordered oldest first")
	}
}

func TestLogicalWarehouseKeyFallback(t *testing.T) {
	if got := logicalWarehouseKey(model.Warehouse{ID: "wh-123"}); got != "TEMU_WH-123" {
		t.Fatalf("logicalWarehouseKey fallback = %q", got)
	}
}
