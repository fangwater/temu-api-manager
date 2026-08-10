package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"temu-api-manager/internal/model"
)

func TestAutoFulfillmentRateLimitBackoff(t *testing.T) {
	if autoFulfillmentRateLimitMarker != "%code=4000004%" {
		t.Fatalf("unexpected rate-limit marker %q", autoFulfillmentRateLimitMarker)
	}
	if autoFulfillmentRateLimitBackoff != time.Minute {
		t.Fatalf("rate-limit backoff = %s", autoFulfillmentRateLimitBackoff)
	}
}

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

func TestRecordLabelPurchaseChoiceAllowsLegacyQuoteWithoutSnapshot(t *testing.T) {
	if err := recordLabelPurchaseChoice(context.Background(), nil, "legacy-quote", "shipment", "order", model.LabelPurchaseChoice{}); err != nil {
		t.Fatalf("legacy quote without analysis must be skipped: %v", err)
	}
}

func TestValidateLabelPurchaseAmountRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "-0.01", "NaN", "Inf"} {
		if err := validateLabelPurchaseAmount(value); err == nil {
			t.Fatalf("validateLabelPurchaseAmount(%q) unexpectedly succeeded", value)
		}
	}
	if err := validateLabelPurchaseAmount("10.2500"); err != nil {
		t.Fatalf("valid amount rejected: %v", err)
	}
}

func TestManualReviewWhereSupportsStatusAndSearch(t *testing.T) {
	where, args := manualReviewWhere(" manual_pending ", " PO-123 ")
	if len(args) != 2 || args[0] != "manual_pending" || args[1] != "%PO-123%" {
		t.Fatalf("unexpected filter args: %#v", args)
	}
	for _, fragment := range []string{
		"m.status=$1",
		"m.parent_order_sn ILIKE $2",
		"l.order_sn ILIKE $2",
		"l.ext_code ILIKE $2",
		"l.goods_name ILIKE $2",
		"l.spec ILIKE $2",
		"merged.merge_order_sn ILIKE $2",
	} {
		if !strings.Contains(where, fragment) {
			t.Errorf("manual review filter is missing %q: %s", fragment, where)
		}
	}

	where, args = manualReviewWhere("", " SKU-A ")
	if len(args) != 1 || args[0] != "%SKU-A%" || !strings.Contains(where, "m.parent_order_sn ILIKE $1") {
		t.Fatalf("unexpected query-only filter: where=%q args=%#v", where, args)
	}

	where, args = manualReviewWhere("", "  ")
	if where != "WHERE m.active" || len(args) != 0 {
		t.Fatalf("unexpected empty filter: where=%q args=%#v", where, args)
	}
}
