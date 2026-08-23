package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/internal/oms"
	"temu-api-manager/internal/service"
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

func TestCombinedShipmentCandidatesEndpointReturnsNormalizedGroups(t *testing.T) {
	var upstreamRequest map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"combinedShippingGroups":[{"combinedShippingGroup":[{"parentOrderSn":" PO-1 ","parentOrderStatus":2,"parentOrderTime":1760000000,"mallId":42,"semiUniqueId":" semi-1 "},{"parentOrderSn":"","parentOrderStatus":2},{"parentOrderSn":"PO-2","parentOrderStatus":4,"parentOrderTime":1760000010,"mallId":42}]},{"combinedShippingGroup":[]}]}}`))
	}))
	defer upstream.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := service.New(nil, temu.NewClient(upstream.URL, "app", "secret", "token", time.Second), nil, nil, time.Minute, logger)
	handler := New(manager, "", "panda-homes", "PANDA HOMES", t.TempDir(), time.Second, logger)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/combined-shipment-candidates", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Success bool                               `json:"success"`
		Data    service.CombinedShipmentCandidates `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Success || payload.Data.TotalGroups != 1 || payload.Data.TotalOrders != 2 {
		t.Fatalf("unexpected response: %#v", payload)
	}
	if len(payload.Data.Groups) != 1 || len(payload.Data.Groups[0].Orders) != 2 {
		t.Fatalf("unexpected normalized groups: %#v", payload.Data.Groups)
	}
	first := payload.Data.Groups[0].Orders[0]
	if first.ParentOrderSN != "PO-1" || first.SemiUniqueID != "semi-1" {
		t.Fatalf("unexpected normalized order: %#v", first)
	}
	if payload.Data.QueriedAt.IsZero() {
		t.Fatal("queried_at must be populated")
	}
	if upstreamRequest["type"] != temu.CombinedShipmentAPI {
		t.Fatalf("unexpected upstream request: %#v", upstreamRequest)
	}
}

func TestPolicyUpdatesSkipOperationKeyOnly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(nil, "secret", "panda-homes", "PANDA HOMES", t.TempDir(), time.Second, logger)

	tests := []struct {
		name string
		path string
		body string
		want int
	}{
		{name: "carrier policy", path: "/api/carrier-policies/DPS002", body: "{", want: http.StatusBadRequest},
		{name: "SKU warehouse policy", path: "/api/sku-warehouse-rules", body: "{", want: http.StatusBadRequest},
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

func TestSplitShippingRoutesDecodeRequestsWithoutOperationKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(nil, "secret", "panda-homes", "PANDA HOMES", t.TempDir(), time.Second, logger)

	for _, path := range []string{"/api/shipping/split-plan", "/api/shipping/split-quotes"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{"))
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "operation key") {
				t.Fatalf("read-only quote route unexpectedly requires operation key: %s", response.Body.String())
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
		Status:        "resolved",
		Outcome:       "manually_fulfilled",
		Note:          "等待仓库确认",
		Reasons:       []string{"inventory_rule"},
		Details:       []string{"美东库存低于安全线"},
		MergeOrderSNs: []string{"PO-211-merge"},
		DetectedAt:    detectedAt,
		UpdatedAt:     detectedAt,
		ResolvedAt:    &detectedAt,
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
	if rows[1][0] != "PANDA BUY" || rows[1][3] != "人工履约完成" || rows[1][4] != "已人工发货" || rows[1][5] != "等待仓库确认" || rows[1][6] != "库存安全线不足" || rows[1][8] != "SKU-A" || rows[1][14] == "" {
		t.Fatalf("unexpected first export row: %#v", rows[1])
	}
	if rows[2][8] != "SKU-B" || rows[2][9] != "2" {
		t.Fatalf("unexpected second export row: %#v", rows[2])
	}
}

func TestManualOutcomeExportText(t *testing.T) {
	if got := manualOutcomeExportText("manually_fulfilled"); got != "已人工发货" {
		t.Fatalf("manual outcome = %q", got)
	}
}

func TestOMSPlatformOrdersRequiresSupportedStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(nil, "", "panda-homes", "PANDA HOMES", t.TempDir(), time.Second, logger)

	for _, path := range []string{
		"/api/oms-platform-orders",
		"/api/oms-platform-orders?status=4",
		"/api/oms-platform-orders?status=-1",
		"/api/oms-platform-orders?status=invalid",
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d, want %d", path, response.Code, http.StatusBadRequest)
		}
		if !strings.Contains(response.Body.String(), "status must be missing, 0, 1, 2, or 3") {
			t.Fatalf("%s returned unexpected body: %s", path, response.Body.String())
		}
	}
}

func TestParseOMSPlatformOrderStatus(t *testing.T) {
	tests := map[string]int{"missing": -1, " MISSING ": -1, "0": 0, "3": 3}
	for value, want := range tests {
		got, err := parseOMSPlatformOrderStatus(value)
		if err != nil || got != want {
			t.Fatalf("parseOMSPlatformOrderStatus(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
}

func TestWarehouseAssignmentSubmitRequiresConfirmationAndRejectsClientSelectors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(nil, "", "panda-homes", "PANDA HOMES", t.TempDir(), time.Second, logger)

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "confirmation required", body: `{"logistics_carrier":"_AUTO_MATCH_","confirm":false}`, want: "confirm=true is required"},
		{name: "account rejected", body: `{"logistics_carrier":"_AUTO_MATCH_","confirm":true,"account":"dps"}`, want: "unknown field"},
		{name: "warehouse rejected", body: `{"logistics_carrier":"_AUTO_MATCH_","confirm":true,"warehouse_code":"DPSNY002"}`, want: "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/oms-platform-orders/PO-1/warehouse-assignment", strings.NewReader(test.body))
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestWarehouseAssignmentErrorsMapToConflictOrBadGateway(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := &Server{logger: logger}

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "local eligibility", err: service.ErrWarehouseAssignmentUnavailable, want: http.StatusConflict},
		{name: "upstream conflict", err: &oms.GatewayError{StatusCode: http.StatusConflict, Message: "订单状态已变化"}, want: http.StatusConflict},
		{name: "upstream failure", err: &oms.GatewayError{StatusCode: http.StatusServiceUnavailable, Message: "领星不可用"}, want: http.StatusBadGateway},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.fail(response, test.err)
			if response.Code != test.want {
				t.Fatalf("got status %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}
