package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
)

func TestFailMapsUpstreamDeadlineToGatewayTimeout(t *testing.T) {
	server := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	response := httptest.NewRecorder()
	server.fail(response, &temu.APIError{Message: context.DeadlineExceeded.Error(), Temporary: true, Cause: context.DeadlineExceeded})
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusGatewayTimeout)
	}
	if !strings.Contains(response.Body.String(), "上游查询超时") {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func TestStaticHandlerRejectsUnknownAPI(t *testing.T) {
	server := &Server{staticRoot: t.TempDir()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/orders/test/auto-ship", nil)
	server.staticHandler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusNotFound)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("unexpected content type %q", contentType)
	}
}

func TestCarrierPolicyUpdateSkipsOperationKeyOnly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(nil, "secret", "panda-homes", "PANDA HOMES", t.TempDir(), time.Second, logger)

	tests := []struct {
		name string
		path string
		body string
		want int
	}{
		{name: "carrier policy", path: "/api/carrier-policies/DPS002", body: "{", want: http.StatusBadRequest},
		{name: "warehouse mapping", path: "/api/warehouse-mappings/DPS002", body: "{}", want: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, test.path, strings.NewReader(test.body))
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("got status %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestSanitizeWarehouseFilename(t *testing.T) {
	tests := map[string]string{
		"DPS002":       "DPS002",
		"arp east":     "ARP_EAST",
		"../unmapped":  "UNMAPPED",
		"ARP_EAST/OMS": "ARP_EAST_OMS",
	}
	for input, want := range tests {
		if got := sanitizeWarehouseFilename(input); got != want {
			t.Fatalf("sanitizeWarehouseFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestShipmentExportRangeUsesInclusiveMinuteEnd(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/shipments/export-po.zip?from=2026-08-04T10:05:38Z&to=2026-08-04T10:07:42Z", nil)
	from, before, err := shipmentExportRange(request)
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := time.Date(2026, 8, 4, 10, 5, 0, 0, time.UTC)
	wantBefore := time.Date(2026, 8, 4, 10, 8, 0, 0, time.UTC)
	if from == nil || !from.Equal(wantFrom) {
		t.Fatalf("from = %v, want %v", from, wantFrom)
	}
	if before == nil || !before.Equal(wantBefore) {
		t.Fatalf("before = %v, want %v", before, wantBefore)
	}
}

func TestShipmentExportRangeRejectsReversedRange(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/shipments/export-po.zip?from=2026-08-04T10:08:00Z&to=2026-08-04T10:07:00Z", nil)
	if _, _, err := shipmentExportRange(request); err == nil {
		t.Fatal("expected reversed export range to fail")
	}
}

func TestWriteManualOrdersCSVExpandsOrderLines(t *testing.T) {
	detectedAt := time.Date(2026, 8, 4, 10, 5, 0, 0, time.Local)
	item := model.ManualReview{
		ParentOrderSN: "PO-211-test",
		Status:        "manual_pending",
		Reasons:       []string{"inventory_rule"},
		Details:       []string{"美东库存低于安全线"},
		MergeOrderSNs: []string{"PO-211-merge"},
		DetectedAt:    detectedAt,
		UpdatedAt:     detectedAt,
		Lines: []model.OrderLine{
			{OrderSN: "211-a", ExtCode: "SKU-A", Quantity: 1, GoodsName: "Item A"},
			{OrderSN: "211-b", ExtCode: "SKU-B", Quantity: 2, GoodsName: "Item B"},
		},
	}
	var body bytes.Buffer
	if err := writeManualOrdersCSV(&body, "PANDA BUY", []model.ManualReview{item}); err != nil {
		t.Fatal(err)
	}
	content := strings.TrimPrefix(body.String(), "\xEF\xBB\xBF")
	rows, err := csv.NewReader(strings.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want header plus two order lines", len(rows))
	}
	if rows[1][0] != "PANDA BUY" || rows[1][3] != "人工处理中" || rows[1][4] != "库存安全线不足" || rows[1][6] != "SKU-A" {
		t.Fatalf("unexpected first export row: %#v", rows[1])
	}
	if rows[2][6] != "SKU-B" || rows[2][7] != "2" {
		t.Fatalf("unexpected second export row: %#v", rows[2])
	}
}
