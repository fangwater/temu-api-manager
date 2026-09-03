package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/internal/oms"
)

func TestValidateWarehouseAssignmentSource(t *testing.T) {
	confirmedAt := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	validShipment := model.Shipment{
		Status: "shipped", ConfirmedAt: &confirmedAt, ParentOrderSN: "PO-1",
		WarehouseID: "TW-1", TrackingNumber: "TRACK-1", PackageSNList: []string{"PKG-1"},
	}
	validMapping := model.WarehouseMapping{TemuWarehouseID: "TW-1", OMSWarehouseCode: "DPSNY002"}
	validLookup := oms.PlatformOrderLookup{
		MatchCount: 1,
		Orders:     []oms.PlatformOrder{{PlatformOrderNo: "PO-1", Status: 0}},
	}
	if err := validateWarehouseAssignmentSource(validShipment, validMapping, validLookup); err != nil {
		t.Fatalf("valid source rejected: %v", err)
	}

	tests := []struct {
		name     string
		shipment model.Shipment
		mapping  model.WarehouseMapping
		lookup   oms.PlatformOrderLookup
		message  string
	}{
		{"label not confirmed", func() model.Shipment { v := validShipment; v.Status = "label_ready"; return v }(), validMapping, validLookup, "面单尚未购买并确认"},
		{"mapping changed", func() model.Shipment { v := validShipment; v.WarehouseID = "TW-OLD"; return v }(), validMapping, validLookup, "仓库映射已变化"},
		{"duplicate OMS order", validShipment, validMapping, oms.PlatformOrderLookup{MatchCount: 2, Orders: []oms.PlatformOrder{{Status: 0}, {Status: 0}}}, "唯一的同号平台订单"},
		{"not pending", validShipment, validMapping, oms.PlatformOrderLookup{MatchCount: 1, Orders: []oms.PlatformOrder{{PlatformOrderNo: "PO-1", Status: 1}}}, "不在待处理状态"},
		{"different assigned warehouse", validShipment, validMapping, oms.PlatformOrderLookup{MatchCount: 1, Orders: []oms.PlatformOrder{{PlatformOrderNo: "PO-1", Status: 0, SendWarehouseCode: "HYTX30"}}}, "不同的发货仓库"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateWarehouseAssignmentSource(test.shipment, test.mapping, test.lookup)
			if !errors.Is(err, ErrWarehouseAssignmentUnavailable) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateWarehouseAssignmentPreview(t *testing.T) {
	target := warehouseAssignmentTarget{
		shipment: model.Shipment{ParentOrderSN: "PO-1"},
		mapping:  model.WarehouseMapping{OMSWarehouseCode: "DPSNY002"},
	}
	valid := oms.WarehouseAssignmentPreview{
		Ready:      true,
		Routes:     []oms.WarehouseAssignmentRoute{{PlatformOrderNo: "PO-1", WarehouseCode: "DPSNY002"}},
		Unresolved: []oms.WarehouseAssignmentUnresolved{},
	}
	if err := validateWarehouseAssignmentPreview(target, valid); err != nil {
		t.Fatalf("valid preview rejected: %v", err)
	}

	unresolved := oms.WarehouseAssignmentPreview{
		Ready:      false,
		Routes:     []oms.WarehouseAssignmentRoute{},
		Unresolved: []oms.WarehouseAssignmentUnresolved{{PlatformOrderNo: "PO-1", Reason: "订单已不在待处理状态"}},
	}
	if err := validateWarehouseAssignmentPreview(target, unresolved); !errors.Is(err, ErrWarehouseAssignmentUnavailable) || !strings.Contains(err.Error(), "订单已不在待处理状态") {
		t.Fatalf("unexpected unresolved error: %v", err)
	}

	mismatch := valid
	mismatch.Routes = []oms.WarehouseAssignmentRoute{{PlatformOrderNo: "PO-1", WarehouseCode: "HYTX30"}}
	if err := validateWarehouseAssignmentPreview(target, mismatch); !errors.Is(err, ErrWarehouseAssignmentUnavailable) || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("unexpected mismatch error: %v", err)
	}
}

func TestAssignPendingOMSPlatformOrderUsesAutomaticCarrierAndSkipsCompletedAssignment(t *testing.T) {
	var previewCalls, assignmentCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-OMS-Account") != "dps" {
			t.Errorf("OMS account = %q", r.Header.Get("X-OMS-Account"))
		}
		switch r.URL.Path {
		case "/platform-orders/routing-preview":
			previewCalls++
			_, _ = io.WriteString(w, `{"success":true,"data":{"ready":true,"routes":[{"platform_order_no":"PO-1","warehouse_code":"DPSNY002"}],"unresolved":[],"carriers":[{"value":"_AUTO_MATCH_","label":"自动匹配"}]}}`)
		case "/platform-orders/warehouse-assignments":
			assignmentCalls++
			var body struct {
				LogisticsCarrier string `json:"logistics_carrier"`
				Confirmation     string `json:"confirmation"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode assignment request: %v", err)
			}
			if body.LogisticsCarrier != oms.AutoMatchCarrier || body.Confirmation != "CONFIRM_AND_APPROVE" {
				t.Errorf("assignment request = %#v", body)
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{"account":"dps","total":1,"success":1,"failed":0,"failures":[],"routes":[{"platform_order_no":"PO-1","warehouse_code":"DPSNY002"}],"warehouse_code":"DPSNY002","logistics_carrier":"_AUTO_MATCH_","completed_at":"2026-08-14T02:30:00Z"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	confirmedAt := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	shipment := model.Shipment{
		Status: "shipped", ConfirmedAt: &confirmedAt, ParentOrderSN: "PO-1",
		WarehouseID: "TW-1", TrackingNumber: "TRACK-1", PackageSNList: []string{"PKG-1"}, OMSAccount: "dps",
	}
	mapping := model.WarehouseMapping{TemuWarehouseID: "TW-1", OMSWarehouseCode: "DPSNY002"}
	lookup := oms.PlatformOrderLookup{
		Account: "dps", MatchCount: 1,
		Orders: []oms.PlatformOrder{{OMSOrderNo: "SO-1", PlatformOrderNo: "PO-1", Status: 0}},
	}
	manager := &Service{oms: oms.NewClient(upstream.URL+"/outbound", time.Second)}

	audit, assigned, err := manager.assignPendingOMSPlatformOrder(context.Background(), shipment, mapping, lookup, nil)
	if err != nil || !assigned {
		t.Fatalf("automatic assignment = (%#v, %t, %v)", audit, assigned, err)
	}
	if audit.Status != "succeeded" || audit.LogisticsCarrier != oms.AutoMatchCarrier || audit.CompletedAt == nil {
		t.Fatalf("assignment audit = %#v", audit)
	}
	if previewCalls != 1 || assignmentCalls != 1 {
		t.Fatalf("calls = preview %d, assignment %d", previewCalls, assignmentCalls)
	}

	reused, assignedAgain, err := manager.assignPendingOMSPlatformOrder(context.Background(), shipment, mapping, lookup, audit)
	if err != nil || assignedAgain || reused != audit {
		t.Fatalf("completed assignment was not reused: (%#v, %t, %v)", reused, assignedAgain, err)
	}
	if previewCalls != 1 || assignmentCalls != 1 {
		t.Fatalf("completed assignment called upstream again: preview %d, assignment %d", previewCalls, assignmentCalls)
	}
}

func TestAssignPendingOMSPlatformOrderDoesNotSubmitUnresolvedPreview(t *testing.T) {
	assignmentCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform-orders/warehouse-assignments" {
			assignmentCalled = true
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"ready":false,"routes":[],"unresolved":[{"platform_order_no":"PO-1","reason":"仓库无法匹配"}],"carriers":[]}}`)
	}))
	defer upstream.Close()

	confirmedAt := time.Now()
	shipment := model.Shipment{
		Status: "shipped", ConfirmedAt: &confirmedAt, ParentOrderSN: "PO-1",
		WarehouseID: "TW-1", TrackingNumber: "TRACK-1", PackageSNList: []string{"PKG-1"}, OMSAccount: "dps",
	}
	mapping := model.WarehouseMapping{TemuWarehouseID: "TW-1", OMSWarehouseCode: "DPSNY002"}
	lookup := oms.PlatformOrderLookup{
		Account: "dps", MatchCount: 1,
		Orders: []oms.PlatformOrder{{OMSOrderNo: "SO-1", PlatformOrderNo: "PO-1", Status: 0}},
	}
	manager := &Service{oms: oms.NewClient(upstream.URL+"/outbound", time.Second)}

	audit, assigned, err := manager.assignPendingOMSPlatformOrder(context.Background(), shipment, mapping, lookup, nil)
	if !errors.Is(err, ErrWarehouseAssignmentUnavailable) || assigned || audit == nil || audit.Status != "retrying" {
		t.Fatalf("unresolved assignment = (%#v, %t, %v)", audit, assigned, err)
	}
	if assignmentCalled {
		t.Fatal("warehouse assignment submitted after unresolved preview")
	}
}
