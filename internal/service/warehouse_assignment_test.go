package service

import (
	"errors"
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
