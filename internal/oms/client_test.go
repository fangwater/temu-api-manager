package oms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
