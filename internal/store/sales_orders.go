package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type SalesSyncState struct {
	Watermark   *time.Time `json:"watermark"`
	Before      *time.Time `json:"reconcile_before"`
	Floor       *time.Time `json:"reconcile_floor"`
	Reconciled  *time.Time `json:"last_reconciled_at"`
	LastSuccess *time.Time `json:"last_success_at"`
	LastError   string     `json:"last_error"`
	Failures    int        `json:"failure_count"`
	Checked     int        `json:"checked_orders"`
}

func (p *Postgres) SalesSyncState(ctx context.Context) (v SalesSyncState, err error) {
	err = p.pool.QueryRow(ctx, `SELECT watermark,reconcile_before,reconcile_floor,last_reconciled_at,last_success_at,last_error,failure_count,checked_orders FROM temu_sales_sync_state WHERE id=1`).Scan(&v.Watermark, &v.Before, &v.Floor, &v.Reconciled, &v.LastSuccess, &v.LastError, &v.Failures, &v.Checked)
	return
}
func (p *Postgres) BeginSalesReconciliation(ctx context.Context, before, floor time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE temu_sales_sync_state SET reconcile_before=$1,reconcile_floor=$2 WHERE id=1`, before, floor)
	return err
}
func (p *Postgres) SalesSyncFailure(ctx context.Context, err error) {
	_, _ = p.pool.Exec(ctx, `UPDATE temu_sales_sync_state SET last_error=$1,failure_count=failure_count+1 WHERE id=1`, err.Error())
}

// A complete window and its progress commit together. Failed pages never advance progress.
func (p *Postgres) CommitSalesWindow(ctx context.Context, mode string, next time.Time, items []json.RawMessage) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, raw := range items {
		var v struct {
			Parent struct {
				PO     string `json:"parentOrderSn"`
				Time   int64  `json:"parentOrderTime"`
				Update int64  `json:"updateTime"`
				Status int    `json:"parentOrderStatus"`
			} `json:"parentOrderMap"`
			Lines []json.RawMessage `json:"orderList"`
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			return err
		}
		if v.Parent.PO == "" || v.Parent.Time <= 0 || v.Parent.Status < 1 || v.Lines == nil {
			return fmt.Errorf("incomplete sales order payload")
		}
		_, err = tx.Exec(ctx, `INSERT INTO temu_sales_orders(parent_order_sn,order_time,source_update_time,parent_order_status,payload) VALUES($1,$2,$3,$4,$5)
  ON CONFLICT(parent_order_sn) DO UPDATE SET order_time=EXCLUDED.order_time,source_update_time=EXCLUDED.source_update_time,parent_order_status=EXCLUDED.parent_order_status,payload=EXCLUDED.payload,fetched_at=now()
  WHERE EXCLUDED.source_update_time>=temu_sales_orders.source_update_time`, v.Parent.PO, time.Unix(v.Parent.Time, 0), v.Parent.Update, v.Parent.Status, raw)
		if err != nil {
			return err
		}
	}
	switch mode {
	case "updated":
		_, err = tx.Exec(ctx, `UPDATE temu_sales_sync_state SET watermark=$1,last_success_at=now(),last_error='',failure_count=0,checked_orders=$2 WHERE id=1`, next, len(items))
	case "created":
		_, err = tx.Exec(ctx, `UPDATE temu_sales_sync_state SET reconcile_before=$1,last_reconciled_at=CASE WHEN $1<=reconcile_floor THEN now() ELSE last_reconciled_at END,last_success_at=now(),last_error='',failure_count=0,checked_orders=$2 WHERE id=1`, next, len(items))
	default:
		return fmt.Errorf("invalid sales sync mode")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
