package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPurchaseSubstitutionRequiresExplicitConfirmation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(nil, "", "panda-homes", "PANDA HOMES", t.TempDir(), time.Second, logger)
	request := httptest.NewRequest(http.MethodPost, "/api/substitution-orders/PO-1/purchase", strings.NewReader(
		`{"warehouse_key":"ARP_EAST","channel_id":42,"confirm":false}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "confirm=true is required") {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
}
