package temu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOrderDetailUsesV2EndpointAndDecodesMergeFields(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"parentOrderMap":{"parentOrderSn":"PO-1","batchOrderNumberList":["PO-2"],"isShipmentConsolidatedByMainMall":true,"regionName1":"California"},"orderList":[{"orderSn":"O-1","quantity":2,"extCode":"SKU-1","packageSnInfo":[{"packageSn":"PK-1","packageDeliveryType":2,"callSuccess":true}]}]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "app", "secret", "token", time.Second)
	result, raw, err := client.OrderDetail(context.Background(), "PO-1")
	if err != nil {
		t.Fatal(err)
	}
	if request["type"] != OrderDetailAPI || request["parentOrderSn"] != "PO-1" {
		t.Fatalf("unexpected request: %#v", request)
	}
	if result.Parent.ParentOrderSN != "PO-1" || !result.Parent.Consolidated || len(result.Parent.BatchOrderNumberList) != 1 {
		t.Fatalf("unexpected parent detail: %#v", result.Parent)
	}
	if len(result.Lines) != 1 || result.Lines[0].Quantity != 2 || result.Lines[0].Products != nil || len(result.Lines[0].PackageSNInfo) != 1 || result.Lines[0].PackageSNInfo[0].PackageSN != "PK-1" {
		t.Fatalf("unexpected lines: %#v", result.Lines)
	}
	if !json.Valid(raw) {
		t.Fatal("raw response must be retained as valid JSON")
	}
}
