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
		"m.note ILIKE $2",
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

	where, args = manualReviewWhere("resolved", "")
	if len(args) != 1 || args[0] != "resolved" || !strings.Contains(where, "NOT m.active") || !strings.Contains(where, "m.outcome<>''") {
		t.Fatalf("unexpected resolved filter: where=%q args=%#v", where, args)
	}
}

func TestOMSPlatformOrderStatusText(t *testing.T) {
	tests := map[int]string{
		omsPlatformOrderTerminalStatus: "人工终态",
		omsPlatformOrderMissingStatus:  "领星无匹配订单",
		0:                              "待处理",
		1:                              "待获取平台面单",
		2:                              "处理中",
		3:                              "已发货",
		4:                              "",
	}
	for status, want := range tests {
		if got := omsPlatformOrderStatusText(status); got != want {
			t.Fatalf("omsPlatformOrderStatusText(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestOMSPlatformOrderMissingRecordsRequireSuccessfulEmptyLookups(t *testing.T) {
	for _, fragment := range []string{
		"s.status='shipped'",
		"s.confirmed_at IS NOT NULL",
		"d.status IN ('waiting_sync','manual_required')",
		"d.response_summary->>'source'='oms_platform_order'",
		"jsonb_array_length(d.response_summary->'expected'->'orders')=0",
		"jsonb_array_length(d.response_summary->'opposite'->'orders')=0",
		"e.event_type='label_ready'",
	} {
		if !strings.Contains(omsPlatformOrderMissingRecords, fragment) {
			t.Fatalf("missing-order query does not require %q", fragment)
		}
	}
}

func TestOMSPlatformOrderAutomationRequiresStartedJobBoundBeforeShipment(t *testing.T) {
	for _, fragment := range []string{
		"j.shipment_id=s.id",
		"j.started_at IS NOT NULL",
		"j.started_at <= s.created_at",
	} {
		if !strings.Contains(omsAutomaticFulfillmentSQL, fragment) {
			t.Fatalf("OMS status query does not require %q for automatic fulfillment", fragment)
		}
	}
}

func TestShipmentQueueWhereExcludesShippedFromLedger(t *testing.T) {
	where, err := shipmentQueueWhere("ledger")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "s.status <> 'shipped'") {
		t.Fatalf("ledger filter does not exclude shipped records: %q", where)
	}
	all, err := shipmentQueueWhere("all")
	if err != nil || all != "" {
		t.Fatalf("all shipment filter = %q, %v; want no filter", all, err)
	}
	if _, err := shipmentQueueWhere("unknown"); err == nil {
		t.Fatal("unsupported shipment queue was accepted")
	}
}

func TestShipmentQueueWhereClassifiesStalledConfirmationAsException(t *testing.T) {
	processing, err := shipmentQueueWhere("processing")
	if err != nil {
		t.Fatal(err)
	}
	exceptions, err := shipmentQueueWhere("exceptions")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"s.status='label_ready'",
		"s.confirmation_attempts=0",
		"cardinality(s.package_sn_list)>0",
		"btrim(s.tracking_number)<>''",
		"interval '5 minutes'",
	} {
		if !strings.Contains(shipmentConfirmationStalledSQL, fragment) {
			t.Errorf("stalled confirmation predicate is missing %q", fragment)
		}
	}
	if !strings.Contains(processing, "AND NOT "+shipmentConfirmationStalledSQL) {
		t.Fatalf("processing queue does not exclude stalled confirmations: %s", processing)
	}
	if !strings.Contains(exceptions, "OR "+shipmentConfirmationStalledSQL) {
		t.Fatalf("exception queue does not include stalled confirmations: %s", exceptions)
	}
}

func TestShipmentCompletionJobBindingPreservesOnlyActiveSameShipment(t *testing.T) {
	for _, fragment := range []string{
		"SELECT so.parent_order_sn,$1,$2,''",
		"shipment_id=EXCLUDED.shipment_id",
		"temu_auto_fulfillment_jobs.shipment_id=EXCLUDED.shipment_id",
		"status IN ('running','confirming')",
		"ELSE EXCLUDED.status",
		"EXCLUDED.status='waiting_oms'",
		"AND temu_auto_fulfillment_jobs.status='completed'",
		"ELSE ''",
	} {
		if !strings.Contains(ensureShipmentCompletionJobSQL, fragment) {
			t.Errorf("shipment completion binding is missing %q", fragment)
		}
	}
	tests := map[string]string{
		"submitting":         "waiting_label",
		"label_pending":      "waiting_label",
		"submission_unknown": "waiting_label",
		"label_ready":        "confirming",
		"confirm_failed":     "confirming",
		"label_failed":       "failed",
		"shipped":            "waiting_oms",
	}
	for shipmentStatus, want := range tests {
		got, ok := completionJobStatusForShipment(shipmentStatus)
		if !ok || got != want {
			t.Errorf("completionJobStatusForShipment(%q)=(%q,%v), want (%q,true)", shipmentStatus, got, ok, want)
		}
	}
	if got, ok := completionJobStatusForShipment("unknown"); ok || got != "" {
		t.Fatalf("unknown shipment state must not alter the completion job: (%q,%v)", got, ok)
	}
}

func TestShipmentCompletionRepairIsLimitedToUntouchedCompleteLabels(t *testing.T) {
	for _, fragment := range []string{
		"s.status='label_ready'",
		"s.confirmation_attempts=0",
		"cardinality(s.package_sn_list)>0",
		"btrim(s.tracking_number)<>''",
		"j.shipment_id IS DISTINCT FROM s.id",
		"j.status NOT IN ('running','waiting_label','confirming')",
		"FOR UPDATE OF s SKIP LOCKED",
		"temu_auto_fulfillment_jobs.shipment_id IS DISTINCT FROM EXCLUDED.shipment_id",
		"SELECT count(*) FROM events",
		"'confirmation_job_repaired'",
	} {
		if !strings.Contains(repairShipmentCompletionJobsSQL, fragment) {
			t.Errorf("shipment completion repair is missing %q", fragment)
		}
	}
}
