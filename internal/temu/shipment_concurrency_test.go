package temu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateShipmentLimitsConcurrency(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"packageSnList":["PK-1"]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "app", "secret", "token", time.Second)
	if err := client.SetShipmentCreateConcurrency(2); err != nil {
		t.Fatal(err)
	}
	errorsChannel := make(chan error, 4)
	for range 4 {
		go func() {
			_, _, err := client.CreateShipment(context.Background(), map[string]any{"sendType": 0})
			errorsChannel <- err
		}()
	}
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("more than two shipment.create calls entered concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 4 {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent creates = %d, want 2", got)
	}
}
