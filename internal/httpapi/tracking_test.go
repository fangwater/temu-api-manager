package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"temu-api-manager/internal/service"
	"temu-api-manager/internal/temu"
)

func TestPackageTrackingEndpoint(t *testing.T) {
	var upstreamRequest map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"packageSn":"PK-HTTP-1","trackingNum":"TRACK-1","trackingInfo":[{"logisticsUpdatedAt":"1754409600","logisticsStatus":"PICKED_UP","statusText":"Package picked up"}]}}`))
	}))
	defer upstream.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	temuClient := temu.NewClient(upstream.URL, "app", "secret", "token", time.Second)
	manager := service.New(nil, temuClient, nil, nil, time.Minute, logger)
	handler := New(manager, "", "test-shop", "Test Shop", t.TempDir(), time.Second, logger)
	request := httptest.NewRequest(http.MethodGet, "/api/packages/PK-HTTP-1/tracking?language=en", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", response.Code, response.Body.String())
	}
	var payload struct {
		Success bool                    `json:"success"`
		Data    temu.TrackingInfoResult `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Success || payload.Data.TrackingNum != "TRACK-1" || len(payload.Data.TrackingInfo) != 1 {
		t.Fatalf("unexpected response payload: %#v", payload)
	}
	if upstreamRequest["type"] != temu.TrackingInfoAPI || upstreamRequest["packageSn"] != "PK-HTTP-1" || upstreamRequest["language"] != "en" {
		t.Fatalf("unexpected upstream request: %#v", upstreamRequest)
	}
}

func TestOrderTrackingEndpointRejectsBlankPO(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := service.New(nil, nil, nil, nil, time.Minute, logger)
	handler := New(manager, "", "test-shop", "Test Shop", t.TempDir(), time.Second, logger)
	request := httptest.NewRequest(http.MethodGet, "/api/orders/%20/tracking", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d; body %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
}

func TestPackageSNNotReadyIsConflict(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := &Server{logger: logger}
	response := httptest.NewRecorder()
	server.fail(response, service.ErrPackageSNNotReady)

	if response.Code != http.StatusConflict {
		t.Fatalf("got status %d, want %d; body %s", response.Code, http.StatusConflict, response.Body.String())
	}
}
