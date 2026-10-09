package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"temu-api-manager/internal/model"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run against an isolated PostgreSQL database, never the application database.
func retryIntegrationStore(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("TEMU_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEMU_TEST_DATABASE_URL is not configured")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "temu_retry_test_") {
		t.Fatal("retry integration tests require a dedicated temu_retry_test_ database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("retry_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.temu_shops(code text PRIMARY KEY); CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	p, err := NewPostgresInSchema(ctx, dsn, schema)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.Close()
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
		if err != nil {
			t.Errorf("clean up test schema: %v", err)
		}
	})
	if err := p.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaimAutoFulfillmentsRetriesTransientErrorsSafely(t *testing.T) {
	p := retryIntegrationStore(t)
	ctx := context.Background()
	internalError := "no automatic shipping option is available: DPS002: insufficient stock\nARP_EAST: Temu API bg.logistics.shippingservices.get error code=4000000 msg=Temu internal system error, please try again later."
	wantClaims := make(map[string]bool)
	for _, fixture := range []struct {
		parent, status, lastError, shipmentStatus string
		age                                       time.Duration
		closed, want                              bool
	}{
		{parent: "new", status: "queued", want: true},
		{parent: "queued_internal_fresh", status: "queued", lastError: internalError},
		{parent: "queued_internal_due", status: "queued", lastError: internalError, age: 90 * time.Second, want: true},
		{parent: "failed_internal_due", status: "failed", lastError: internalError, age: 90 * time.Second, want: true},
		{parent: "failed_internal_fresh", status: "failed", lastError: internalError},
		{parent: "failed_internal_closed", status: "failed", lastError: internalError, age: 90 * time.Second, closed: true},
		{parent: "failed_business", status: "failed", lastError: "invalid package", age: 90 * time.Second},
		{parent: "failed_unknown", status: "failed", lastError: "Temu API error code=4000000 msg=invalid warehouse", age: 90 * time.Second},
		{parent: "queued_network_fresh", status: "queued", lastError: "Temu API HTTP 0: timeout"},
		{parent: "queued_network_due", status: "queued", lastError: "Temu API HTTP 0: timeout", age: 20 * time.Second, want: true},
		{parent: "failed_rate_limit", status: "failed", lastError: "Temu API error code=4000004 msg=too frequent requests", age: 90 * time.Second, want: true},
		{parent: "failed_business_service", status: "failed", lastError: "Temu API error code=7000000 msg=BUSINESS_SERVICE_ERROR", age: 90 * time.Second, want: true},
		{parent: "failed_label_pending", status: "failed", lastError: internalError, shipmentStatus: "label_pending", age: 90 * time.Second, want: true},
		{parent: "waiting_label_fresh", status: "waiting_label", lastError: internalError, shipmentStatus: "label_pending"},
		{parent: "confirming_due", status: "confirming", lastError: internalError, shipmentStatus: "label_ready", age: 90 * time.Second, want: true},
		{parent: "failed_label_failed", status: "failed", lastError: internalError, shipmentStatus: "label_failed", age: 90 * time.Second},
		{parent: "failed_shipped", status: "failed", lastError: internalError, shipmentStatus: "shipped", age: 90 * time.Second},
	} {
		if _, err := p.pool.Exec(ctx, `INSERT INTO temu_orders(parent_order_sn,parent_order_status,is_open,raw_payload) VALUES($1,2,$2,'{}')`, fixture.parent, !fixture.closed); err != nil {
			t.Fatal(err)
		}
		shipmentID := ""
		if fixture.shipmentStatus != "" {
			shipmentID = "s_" + fixture.parent
			if _, err := p.pool.Exec(ctx, `INSERT INTO temu_shipping_quotes(id,parent_order_sn,oms_warehouse_key,temu_warehouse_id,region,selected_channel_id,selected_ship_company_id,request_payload,response_payload,expires_at) VALUES($1,$2,'ARP_EAST','WH-test','east',1,1,'{}','{}',now()+interval '10 minutes')`, "q_"+fixture.parent, fixture.parent); err != nil {
				t.Fatal(err)
			}
			if _, err := p.pool.Exec(ctx, `INSERT INTO temu_shipments(id,quote_id,idempotency_key,status,warehouse_id,channel_id,ship_company_id,request_payload) VALUES($1,$2,$3,$4,'WH-test',1,1,'{}')`, shipmentID, "q_"+fixture.parent, "buy-label:v1:"+fixture.parent, fixture.shipmentStatus); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := p.pool.Exec(ctx, `INSERT INTO temu_auto_fulfillment_jobs(parent_order_sn,status,last_error,shipment_id,updated_at) VALUES($1,$2,$3,nullif($4,''),$5)`, fixture.parent, fixture.status, fixture.lastError, shipmentID, time.Now().Add(-fixture.age)); err != nil {
			t.Fatal(err)
		}
		wantClaims[fixture.parent] = fixture.want
	}
	jobs, err := p.ClaimAutoFulfillments(ctx, time.Now().Add(-10*time.Second), 50)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]bool)
	for _, job := range jobs {
		claimed[job.ParentOrderSN] = true
		if job.Status != "running" || job.Attempts != 1 {
			t.Fatalf("claim must atomically advance attempt: %+v", job)
		}
	}
	for parent, want := range wantClaims {
		if claimed[parent] != want {
			t.Errorf("claim(%s) = %t, expected %t", parent, claimed[parent], want)
		}
	}
	// A concurrent worker cannot claim the same in-flight order again.
	second, err := p.ClaimAutoFulfillments(ctx, time.Now().Add(-10*time.Second), 50)
	if err != nil || len(second) != 0 {
		t.Fatalf("duplicate claims = %v, error = %v", second, err)
	}
}

func TestConcurrentShipmentReservationsKeepOnePurchase(t *testing.T) {
	p := retryIntegrationStore(t)
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, `INSERT INTO temu_orders(parent_order_sn,parent_order_status,raw_payload) VALUES('PO-retry',2,'{}')`); err != nil {
		t.Fatal(err)
	}
	quote := model.Quote{ID: "q_retry", ParentOrderSN: "PO-retry", OMSWarehouseKey: "ARP_EAST", TemuWarehouseID: "WH-test", Region: "east", ChannelID: 1, ShipCompanyID: 1, RequestPayload: json.RawMessage(`{}`), ResponsePayload: json.RawMessage(`{}`), ExpiresAt: time.Now().Add(10 * time.Minute)}
	if err := p.SaveQuote(ctx, quote); err != nil {
		t.Fatal(err)
	}
	type result struct {
		shipment  model.Shipment
		duplicate bool
		err       error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shipment := model.Shipment{ID: fmt.Sprintf("s_retry_%d", i), QuoteID: quote.ID, ParentOrderSN: quote.ParentOrderSN, IdempotencyKey: "buy-label:v1:PO-retry", SelectionMode: "exact_channel", WarehouseID: "WH-test", ChannelID: 1, ShipCompanyID: 1, RequestPayload: json.RawMessage(`{}`)}
			reserved, duplicate, err := p.ReserveShipment(ctx, shipment, model.LabelPurchaseChoice{})
			results <- result{reserved, duplicate, err}
		}()
	}
	wg.Wait()
	close(results)
	id, purchases := "", 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id != "" && r.shipment.ID != id {
			t.Fatal("concurrent retries reserved different shipments")
		}
		id = r.shipment.ID
		if !r.duplicate {
			purchases++
		}
	}
	if purchases != 1 {
		t.Fatalf("create shipment operations = %d, want 1", purchases)
	}
}
