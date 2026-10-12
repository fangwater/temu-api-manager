package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"temu-api-manager/internal/temu"
	"testing"
	"time"
)

func TestIncompleteSalesPaginationNeverCommits(t *testing.T) {
	for _, kind := range []string{"duplicate", "total_change", "missing_page", "api_failure"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					fmt.Fprint(w, `{"success":true,"result":{"totalItemNum":2,"pageItems":[{"parentOrderMap":{"parentOrderSn":"a"}}]}}`)
					return
				}
				switch kind {
				case "duplicate":
					fmt.Fprint(w, `{"success":true,"result":{"totalItemNum":2,"pageItems":[{"parentOrderMap":{"parentOrderSn":"a"}}]}}`)
				case "total_change":
					fmt.Fprint(w, `{"success":true,"result":{"totalItemNum":3,"pageItems":[]}}`)
				case "missing_page":
					fmt.Fprint(w, `{"success":true,"result":{"totalItemNum":2,"pageItems":[]}}`)
				default:
					fmt.Fprint(w, `{"success":false,"errorCode":4000000,"errorMsg":"Temu internal system error"}`)
				}
			}))
			defer srv.Close()
			// A nil store is intentional: incomplete pages must never reach the commit.
			s := &Service{temu: temu.NewClient(srv.URL, "test", "test", "test", time.Second)}
			err := s.syncSalesWindow(context.Background(), "created", time.Unix(1, 0), time.Unix(2, 0), time.Unix(1, 0))
			if err == nil || calls != 2 {
				t.Fatalf("incomplete pagination accepted: %v calls=%d", err, calls)
			}
			if kind != "api_failure" && !strings.Contains(err.Error(), "pagination") {
				t.Fatal(err)
			}
		})
	}
}
