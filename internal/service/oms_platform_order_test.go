package service

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/internal/oms"
)

func TestDecideOMSPlatformOrderUserWorkflow(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	confirmedLongAgo := now.Add(-time.Hour)
	confirmedRecently := now.Add(-10 * time.Minute)
	lookup := func(account string, orders ...oms.PlatformOrder) oms.PlatformOrderLookup {
		return oms.PlatformOrderLookup{Account: account, Found: len(orders) > 0, MatchCount: len(orders), Orders: orders}
	}
	order := func(status int, key, text, warehouse string) oms.PlatformOrder {
		return oms.PlatformOrder{OMSOrderNo: "SO-1", PlatformOrderNo: "PO-1", Status: status, StatusKey: key, StatusText: text, SendWarehouseCode: warehouse}
	}

	tests := []struct {
		name        string
		expected    oms.PlatformOrderLookup
		opposite    oms.PlatformOrderLookup
		confirmedAt *time.Time
		state       string
		verified    bool
		manual      bool
	}{
		{"recent missing waits", lookup("dps"), lookup("arp"), &confirmedRecently, "missing", false, false},
		{"old missing needs supplement", lookup("dps"), lookup("arp"), &confirmedLongAgo, "manual_required", false, true},
		{"pending waits for match", lookup("dps", order(0, "pending", "待处理", "")), lookup("arp"), &confirmedLongAgo, "pending", false, false},
		{"awaiting label waits", lookup("dps", order(1, "awaiting_platform_label", "待获取平台面单", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "awaiting_platform_label", false, false},
		{"processing archives", lookup("dps", order(2, "processing", "处理中", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "processing", true, false},
		{"shipped archives", lookup("dps", order(3, "shipped", "已发货", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "shipped", true, false},
		{"canceled is manual", lookup("dps", order(4, "canceled", "已取消", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "manual_required", false, true},
		{"exception is manual", lookup("dps", order(5, "exception", "异常", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "manual_required", false, true},
		{"awaiting invoice is manual", lookup("dps", order(6, "awaiting_invoice", "待开票", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "manual_required", false, true},
		{"unknown status is manual", lookup("dps", order(9, "unknown", "", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "manual_required", false, true},
		{"wrong warehouse is manual", lookup("dps", order(2, "processing", "处理中", "DPSCA004")), lookup("arp"), &confirmedLongAgo, "manual_required", false, true},
		{"duplicate expected orders are manual", lookup("dps", order(2, "processing", "处理中", "DPSNY002"), order(2, "processing", "处理中", "DPSNY002")), lookup("arp"), &confirmedLongAgo, "manual_required", false, true},
		{"opposite processing prevents collision", lookup("dps", order(2, "processing", "处理中", "DPSNY002")), lookup("arp", order(2, "processing", "处理中", "HYTX30")), &confirmedLongAgo, "manual_required", false, true},
		{"both shipped prevents collision", lookup("dps", order(3, "shipped", "已发货", "DPSNY002")), lookup("arp", order(3, "shipped", "已发货", "HYTX30")), &confirmedLongAgo, "manual_required", false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := decideOMSPlatformOrder(test.expected, test.opposite, "DPSNY002", test.confirmedAt, now)
			if decision.State != test.state || decision.Verified != test.verified || decision.ManualRequired != test.manual {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestOMSAccountSelectorsForWarehouse(t *testing.T) {
	tests := []struct {
		mapping            model.WarehouseMapping
		expected, opposite string
		ok                 bool
	}{
		{model.WarehouseMapping{OMSKey: "DPS002", OMSWarehouseCode: "DPSNY002", OMSAccount: "dps"}, "dps", "arp", true},
		{model.WarehouseMapping{OMSKey: "DPS004", OMSWarehouseCode: "DPSCA004", OMSAccount: "DPS"}, "dps", "arp", true},
		{model.WarehouseMapping{OMSKey: "ARP_EAST", OMSWarehouseCode: "HYTX30", OMSAccount: "arp"}, "arp", "dps", true},
		{model.WarehouseMapping{OMSKey: "DPS002", OMSWarehouseCode: "DPSNY002"}, "", "", false},
		{model.WarehouseMapping{OMSKey: "DPS002", OMSWarehouseCode: "DPSNY002", OMSAccount: "unknown"}, "", "", false},
	}
	for _, test := range tests {
		expected, opposite, ok := omsAccountSelectorsForWarehouse(test.mapping)
		if expected != test.expected || opposite != test.opposite || ok != test.ok {
			t.Fatalf("mapping %#v = (%q, %q, %v)", test.mapping, expected, opposite, ok)
		}
	}
}

func TestAutoFulfillmentOMSFailureStatus(t *testing.T) {
	if got := autoFulfillmentOMSFailureStatus(errors.New("temporary")); got != "waiting_oms" {
		t.Fatalf("temporary error status = %q", got)
	}
	if got := autoFulfillmentOMSFailureStatus(errOMSManualRequired); got != "failed" {
		t.Fatalf("manual error status = %q", got)
	}
}

func TestOMSPlatformOrderSummaryPreservesWarehouseAssignmentAudit(t *testing.T) {
	completedAt := time.Date(2026, 8, 14, 2, 30, 0, 0, time.UTC)
	audit := &omsWarehouseAssignmentAudit{
		Status: "succeeded", OMSOrderNo: "SO-1", OMSAccount: "dps",
		WarehouseCode: "DPSNY002", LogisticsCarrier: oms.AutoMatchCarrier, CompletedAt: &completedAt,
	}
	expected := oms.PlatformOrderLookup{
		Account: "dps", Found: true, MatchCount: 1,
		Orders: []oms.PlatformOrder{{OMSOrderNo: "SO-1", PlatformOrderNo: "PO-1", Status: 0}},
	}
	summary := omsPlatformOrderSummary(
		model.WarehouseMapping{OMSKey: "DPS002", OMSWarehouseCode: "DPSNY002", OMSAccount: "dps"},
		expected,
		oms.PlatformOrderLookup{Account: "arp"},
		omsPlatformOrderDecision{State: "pending"},
		audit,
	)
	if !json.Valid(summary) {
		t.Fatalf("invalid summary: %s", summary)
	}
	shipment := model.Shipment{OMSSync: &model.OMSSync{Summary: summary}}
	restored := omsWarehouseAssignmentAuditFromShipment(shipment)
	if restored == nil || restored.Status != "succeeded" || restored.LogisticsCarrier != oms.AutoMatchCarrier || restored.CompletedAt == nil || !restored.CompletedAt.Equal(completedAt) {
		t.Fatalf("restored audit = %#v", restored)
	}
	failureSummary := omsPlatformOrderFailureSummary(shipment, model.WarehouseMapping{OMSKey: "DPS002", OMSWarehouseCode: "DPSNY002", OMSAccount: "dps"})
	failureShipment := model.Shipment{OMSSync: &model.OMSSync{Summary: failureSummary}}
	restoredAfterFailure := omsWarehouseAssignmentAuditFromShipment(failureShipment)
	if restoredAfterFailure == nil || restoredAfterFailure.Status != "succeeded" || restoredAfterFailure.CompletedAt == nil || !restoredAfterFailure.CompletedAt.Equal(completedAt) {
		t.Fatalf("assignment audit lost after query failure: %#v", restoredAfterFailure)
	}
}
