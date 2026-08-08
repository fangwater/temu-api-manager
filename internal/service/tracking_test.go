package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
)

func TestOrderTrackingForShipmentReturnsEveryUniquePackage(t *testing.T) {
	var requests []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, request)
		packageSN, _ := request["packageSn"].(string)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"result": map[string]any{
				"packageSn":   packageSN,
				"trackingNum": "TRACK-" + packageSN,
				"trackingInfo": []map[string]string{{
					"logisticsUpdatedAt": "1754409600",
					"logisticsStatus":    "PICKED_UP",
					"statusText":         "Package picked up",
				}},
			},
		})
	}))
	defer upstream.Close()

	manager := &Service{temu: temu.NewClient(upstream.URL, "app", "secret", "token", time.Second)}
	result, err := manager.orderTrackingForShipment(context.Background(), model.Shipment{
		ParentOrderSN: " PO-1 ",
		PackageSNList: []string{" PK-1 ", "", "PK-2", "PK-1"},
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if result.ParentOrderSN != "PO-1" || len(result.Packages) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Packages[0].PackageSN != "PK-1" || result.Packages[1].TrackingNum != "TRACK-PK-2" {
		t.Fatalf("unexpected packages: %#v", result.Packages)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d upstream requests, want 2", len(requests))
	}
	for _, request := range requests {
		if request["type"] != temu.TrackingInfoAPI || request["language"] != "en" {
			t.Fatalf("unexpected upstream request: %#v", request)
		}
	}
}

func TestOrderTrackingForShipmentRequiresPackageSN(t *testing.T) {
	manager := &Service{}
	_, err := manager.orderTrackingForShipment(context.Background(), model.Shipment{
		ParentOrderSN: "PO-1",
		PackageSNList: []string{"", "  "},
	}, "")
	if !errors.Is(err, ErrPackageSNNotReady) {
		t.Fatalf("got error %v, want ErrPackageSNNotReady", err)
	}
}

func TestPackageSNsFromStoredOrderDetail(t *testing.T) {
	raw := json.RawMessage(`{"success":true,"result":{"orderList":[
		{"packageSnInfo":[{"packageSn":" PK-1 "},{"packageSn":""}]},
		{"packageSnInfo":[{"packageSn":"PK-1"},{"packageSn":"PK-2"}]}
	]}}`)

	packageSNs, err := packageSNsFromStoredOrderDetail(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(packageSNs) != 2 || packageSNs[0] != "PK-1" || packageSNs[1] != "PK-2" {
		t.Fatalf("unexpected package SNs: %#v", packageSNs)
	}
}
