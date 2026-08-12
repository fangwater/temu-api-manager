package oms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryByReferencesFallsBackToReferOrder(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Warehouse string         `json:"warehouse"`
			Data      map[string]any `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if calls == 1 {
			if values, ok := body.Data["thirdOrderNoList"].([]any); !ok || len(values) != 2 {
				t.Fatalf("thirdOrderNoList must be a deduplicated array: %#v", body.Data)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"code":200,"msg":"ok","data":[]}}`))
			return
		}
		if values, ok := body.Data["referOrderNoList"].([]any); !ok || len(values) != 2 {
			t.Fatalf("referOrderNoList must be a deduplicated array: %#v", body.Data)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"code":200,"msg":"ok","data":[{"whCode":"HYTX30","outboundOrderNo":"OB1","referOrderNo":"PO1","status":1,"logisticsTrackNo":"T1"}]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second)
	orders, err := client.QueryByReferences(context.Background(), "HYTX30", []string{"PO1", "PO2", "PO1"})
	if err != nil || len(orders) != 1 || orders[0].OutboundOrderNo != "OB1" {
		t.Fatalf("orders=%#v err=%v", orders, err)
	}
}

func TestQueryPlatformOrderUsesAccountOwnerAndServiceEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/temu/platform-orders/PO-1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-OMS-Account") != "dps" {
			t.Fatalf("unexpected OMS account: %q", r.Header.Get("X-OMS-Account"))
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"account":"dps","platform_order_no":"PO-1","found":true,"match_count":1,"orders":[{"oms_order_no":"SO-1","platform_order_no":"PO-1","status":4,"status_key":"canceled","status_text":"已取消","send_warehouse_code":"DPSNY002","audit_time":"2026-08-06 18:00:05"}],"queried_at":"2026-08-11T10:00:00Z"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/outbound", time.Second)
	result, err := client.QueryPlatformOrder(context.Background(), "dps", "PO-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Account != "dps" || !result.Found || result.MatchCount != 1 ||
		len(result.Orders) != 1 || result.Orders[0].Status != 4 || result.Orders[0].StatusKey != "canceled" {
		t.Fatalf("unexpected platform order result: %#v", result)
	}
}

func TestQueryPlatformOrderValidatesInputsAndGatewayFailure(t *testing.T) {
	client := NewClient("https://xlwms.example.test/outbound", time.Second)
	if _, err := client.QueryPlatformOrder(context.Background(), "", "PO-1"); err == nil {
		t.Fatal("missing account must fail")
	}
	if _, err := client.QueryPlatformOrder(context.Background(), "arp", ""); err == nil {
		t.Fatal("missing platform order number must fail")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"success":false,"error":"OMS unavailable"}`))
	}))
	defer server.Close()
	_, err := NewClient(server.URL+"/outbound", time.Second).QueryPlatformOrder(context.Background(), "arp", "PO-1")
	if err == nil || !strings.Contains(err.Error(), "OMS unavailable") {
		t.Fatalf("unexpected gateway error: %v", err)
	}
}

func TestSyncFulfillmentAuditsUsesSiblingEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fulfillment-audits/sync" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var snapshot FulfillmentAuditSnapshot
		if err := json.NewDecoder(r.Body).Decode(&snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.ShopCode != "panda-buy" || len(snapshot.Orders) != 1 || snapshot.Orders[0].PlatformOrderNo != "PO-1" {
			t.Fatalf("unexpected snapshot: %#v", snapshot)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"orders":1}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/outbound", time.Second)
	err := client.SyncFulfillmentAudits(context.Background(), FulfillmentAuditSnapshot{
		Platform: "temu", ShopCode: "panda-buy", Orders: []FulfillmentAuditSnapshotOrder{{PlatformOrderNo: "PO-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
