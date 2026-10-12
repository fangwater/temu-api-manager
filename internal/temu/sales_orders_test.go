package temu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSalesOrdersIncludesEveryStatusAndTimeFilter(t *testing.T) {
	for _, mode := range []string{"updated", "created"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Fatal(err)
				}
				if _, ok := p["parentOrderStatus"]; ok {
					t.Error("sales must include cancelled and already shipped orders")
				}
				key := "createAfter"
				if mode == "updated" {
					key = "updateAtStart"
				}
				if p[key] != float64(1000) || p["pageNumber"] != float64(2) {
					t.Errorf("incorrect time/pagination")
				}
				w.Write([]byte(`{"success":true,"result":{"totalItemNum":1,"pageItems":[{"parentOrderMap":{"parentOrderStatus":3}}]}}`))
			}))
			defer srv.Close()
			result, err := NewClient(srv.URL, "test", "test", "test", time.Second).SalesOrders(context.Background(), mode, time.Unix(1000, 0), time.Unix(2000, 0), 2)
			if err != nil || result.TotalItemNum != 1 || len(result.PageItems) != 1 {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

func TestSalesPageRejectsMissingTotals(t *testing.T) {
	var page SalesOrderPage
	if err := json.Unmarshal([]byte(`{"pageItems":[]}`), &page); err == nil {
		t.Fatal("missing total silently accepted as empty window")
	}
	if err := json.Unmarshal([]byte(`{"totalItemNum":0,"pageItems":[]}`), &page); err != nil {
		t.Fatal(err)
	}
}
