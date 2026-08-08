package temu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrackingInfoUsesPackageSNAndDecodesEvents(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"packageSn":"PK-1","trackingNum":"LAST-MILE-1","trackingInfo":[{"logisticsUpdatedAt":"1754409600","logisticsStatus":"IN_TRANSIT","statusText":"Departed carrier facility"}]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "app", "secret", "token", time.Second)
	result, raw, err := client.TrackingInfo(context.Background(), "PK-1", "en")
	if err != nil {
		t.Fatal(err)
	}
	if request["type"] != TrackingInfoAPI || request["packageSn"] != "PK-1" || request["language"] != "en" {
		t.Fatalf("unexpected request: %#v", request)
	}
	if result.PackageSN != "PK-1" || result.TrackingNum != "LAST-MILE-1" || len(result.TrackingInfo) != 1 {
		t.Fatalf("unexpected tracking result: %#v", result)
	}
	event := result.TrackingInfo[0]
	if event.LogisticsUpdatedAt != "1754409600" || event.LogisticsStatus != "IN_TRANSIT" || event.StatusText != "Departed carrier facility" {
		t.Fatalf("unexpected tracking event: %#v", event)
	}
	if !json.Valid(raw) {
		t.Fatal("raw response must be retained as valid JSON")
	}
}

func TestTrackingInfoOmitsEmptyLanguage(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{"packageSn":"PK-1","trackingInfo":[]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "app", "secret", "token", time.Second)
	if _, _, err := client.TrackingInfo(context.Background(), "PK-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, exists := request["language"]; exists {
		t.Fatalf("empty language must be omitted: %#v", request)
	}
}
