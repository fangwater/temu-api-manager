package temu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCombinedShipmentsUsesDedicatedEndpointAndDecodesGroups(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"combinedShippingGroups":[{"combinedShippingGroup":[{"parentOrderSn":"PO-1","parentOrderStatus":2,"parentOrderTime":1760000000,"mallId":42,"semiUniqueId":"semi-1"},{"parentOrderSn":"PO-2","parentOrderStatus":2,"parentOrderTime":1760000010,"mallId":42,"semiUniqueId":"semi-1"}]}]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "app", "secret", "token", time.Second)
	result, raw, err := client.CombinedShipments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if request["type"] != CombinedShipmentAPI {
		t.Fatalf("unexpected API type: %#v", request)
	}
	if _, exists := request["parentOrderSn"]; exists {
		t.Fatalf("combined shipment list must not send parentOrderSn: %#v", request)
	}
	if len(result.Groups) != 1 || len(result.Groups[0].Orders) != 2 {
		t.Fatalf("unexpected groups: %#v", result.Groups)
	}
	first := result.Groups[0].Orders[0]
	if first.ParentOrderSN != "PO-1" || first.ParentOrderStatus != 2 || first.MallID != 42 || first.SemiUniqueID != "semi-1" {
		t.Fatalf("unexpected first order: %#v", first)
	}
	if !json.Valid(raw) {
		t.Fatal("raw response must be retained as valid JSON")
	}
}

func TestCombinedShipmentsAcceptsNullResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errorCode":1000000,"result":null}`))
	}))
	defer server.Close()

	result, _, err := NewClient(server.URL, "app", "secret", "token", time.Second).CombinedShipments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 0 {
		t.Fatalf("null result must produce no groups: %#v", result.Groups)
	}
}
