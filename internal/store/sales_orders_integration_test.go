package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func salesFixture(po string, update int, status int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"parentOrderMap":{"parentOrderSn":%q,"parentOrderTime":1791730000,"updateTime":%d,"parentOrderStatus":%d},"orderList":[]}`, po, update, status))
}
func TestSalesWindowAtomicProgressAndStaleCancellation(t *testing.T) {
	p := retryIntegrationStore(t)
	ctx := context.Background()
	next := time.Now().UTC().Truncate(time.Second)
	if err := p.CommitSalesWindow(ctx, "updated", next, []json.RawMessage{salesFixture("a", 20, 3)}); err != nil {
		t.Fatal(err)
	}
	if err := p.CommitSalesWindow(ctx, "updated", next.Add(time.Hour), []json.RawMessage{salesFixture("b", 30, 4), json.RawMessage(`{}`)}); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	state, err := p.SalesSyncState(ctx)
	if err != nil || state.Watermark == nil || !state.Watermark.Equal(next) {
		t.Fatalf("failure advanced cursor: %+v %v", state, err)
	}
	var count int
	p.pool.QueryRow(ctx, `SELECT count(*) FROM temu_sales_orders`).Scan(&count)
	if count != 1 {
		t.Fatal("failed window partially committed")
	}
	if err = p.CommitSalesWindow(ctx, "created", next, []json.RawMessage{salesFixture("a", 10, 2)}); err != nil {
		t.Fatal(err)
	}
	var status int
	p.pool.QueryRow(ctx, `SELECT parent_order_status FROM temu_sales_orders WHERE parent_order_sn='a'`).Scan(&status)
	if status != 3 {
		t.Fatal("older data undid cancellation")
	}
	p.pool.QueryRow(ctx, `SELECT count(*) FROM temu_orders`).Scan(&count)
	if count != 0 {
		t.Fatal("sales replay modified fulfillment queue")
	}
	if err = p.BeginSalesReconciliation(ctx, next, next.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = p.CommitSalesWindow(ctx, "created", next.Add(-24*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	state, err = p.SalesSyncState(ctx)
	if err != nil || state.Reconciled == nil {
		t.Fatalf("reconciliation not completed: %v", err)
	}
}
