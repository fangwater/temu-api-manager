package store

import (
	"context"
	"errors"

	"temu-api-manager/internal/model"

	"github.com/jackc/pgx/v5"
)

const bulkBatchSelect = `SELECT b.id,b.status,b.total_orders,b.succeeded_orders,b.failed_orders,
	coalesce((SELECT i.parent_order_sn FROM temu_bulk_fulfillment_items i
		WHERE i.batch_id=b.id AND i.status IN ('pending','running') ORDER BY i.sequence_no LIMIT 1),''),
	b.failed_order_sn,b.last_error,b.created_at,b.updated_at,b.completed_at
	FROM temu_bulk_fulfillment_batches b`

func (p *Postgres) CreateBulkFulfillmentBatch(ctx context.Context, batchID string) (model.BulkFulfillmentBatch, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.BulkFulfillmentBatch{}, false, err
	}
	defer tx.Rollback(ctx)

	existing, err := scanBulkFulfillmentBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.status='running' ORDER BY b.created_at DESC LIMIT 1`))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.BulkFulfillmentBatch{}, false, err
	}

	rows, err := tx.Query(ctx, `
		SELECT o.parent_order_sn
		FROM temu_orders o
		WHERE o.is_open AND o.parent_order_status=2
		  AND NOT EXISTS (SELECT 1 FROM temu_shipment_orders queued WHERE queued.parent_order_sn=o.parent_order_sn)
		  AND EXISTS (
			SELECT 1 FROM temu_order_warehouse_checks warehouse_check
			WHERE warehouse_check.parent_order_sn=o.parent_order_sn
			  AND warehouse_check.source_update_time=coalesce(o.update_time,0)
			  AND warehouse_check.status='eligible'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM temu_order_manual_reviews manual
			WHERE manual.parent_order_sn=o.parent_order_sn AND manual.active
			  AND (manual.status<>'approved' OR manual.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[])
		  )
		ORDER BY o.expect_ship_latest_time NULLS LAST,o.update_time,o.parent_order_sn
		FOR UPDATE OF o
	`)
	if err != nil {
		return model.BulkFulfillmentBatch{}, false, err
	}
	parents := make([]string, 0, 600)
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			rows.Close()
			return model.BulkFulfillmentBatch{}, false, err
		}
		parents = append(parents, parent)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return model.BulkFulfillmentBatch{}, false, err
	}
	rows.Close()
	if len(parents) == 0 {
		return model.BulkFulfillmentBatch{}, false, errors.New("没有可自动发货的待发货订单")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO temu_bulk_fulfillment_batches(id,status,total_orders)
		VALUES($1,'running',$2)
	`, batchID, len(parents)); err != nil {
		return model.BulkFulfillmentBatch{}, false, err
	}
	for index, parent := range parents {
		if _, err := tx.Exec(ctx, `
			INSERT INTO temu_bulk_fulfillment_items(batch_id,sequence_no,parent_order_sn,status)
			VALUES($1,$2,$3,'pending')
		`, batchID, index+1, parent); err != nil {
			return model.BulkFulfillmentBatch{}, false, err
		}
	}
	batch, err := scanBulkFulfillmentBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.id=$1`, batchID))
	if err != nil {
		return model.BulkFulfillmentBatch{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.BulkFulfillmentBatch{}, false, err
	}
	return batch, true, nil
}

func (p *Postgres) LatestBulkFulfillmentBatch(ctx context.Context) (model.BulkFulfillmentBatch, error) {
	return scanBulkFulfillmentBatch(p.pool.QueryRow(ctx, bulkBatchSelect+` ORDER BY b.created_at DESC LIMIT 1`))
}

func (p *Postgres) RestartBulkFulfillmentBatch(ctx context.Context) (model.BulkFulfillmentBatch, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	defer tx.Rollback(ctx)
	var batchID string
	if err := tx.QueryRow(ctx, `
SELECT id FROM temu_bulk_fulfillment_batches
WHERE status IN ('running','stopped')
ORDER BY (status='running') DESC,created_at DESC
LIMIT 1 FOR UPDATE
`).Scan(&batchID); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO temu_auto_fulfillment_jobs(parent_order_sn,status)
SELECT i.parent_order_sn,'queued'
FROM temu_bulk_fulfillment_items i
WHERE i.batch_id=$1 AND i.status IN ('running','failed')
ON CONFLICT(parent_order_sn) DO NOTHING
`, batchID); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE temu_auto_fulfillment_jobs j SET
status=CASE
WHEN s.status='shipped' THEN 'waiting_oms'
WHEN s.status IN ('label_ready','confirm_failed') THEN 'confirming'
WHEN s.status IN ('submitting','label_pending','submission_unknown') THEN 'waiting_label'
WHEN s.status='label_failed' THEN 'failed'
ELSE 'queued'
END,
shipment_id=coalesce(s.id,j.shipment_id),
last_error=CASE WHEN s.status='label_failed' THEN j.last_error ELSE '' END,
updated_at=now()-interval '10 minutes',completed_at=NULL
FROM temu_bulk_fulfillment_items i
LEFT JOIN temu_shipment_orders so ON so.parent_order_sn=i.parent_order_sn
LEFT JOIN temu_shipments s ON s.id=so.shipment_id
WHERE i.batch_id=$1 AND i.status IN ('running','failed')
  AND j.parent_order_sn=i.parent_order_sn
`, batchID); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE temu_bulk_fulfillment_items SET status='pending',last_error='',updated_at=now()
WHERE batch_id=$1 AND status IN ('running','failed')
`, batchID); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE temu_bulk_fulfillment_batches b SET
status='running',
succeeded_orders=(SELECT count(*) FROM temu_bulk_fulfillment_items i WHERE i.batch_id=b.id AND i.status='succeeded'),
failed_orders=0,failed_order_sn='',last_error='',updated_at=now(),completed_at=NULL
WHERE b.id=$1
`, batchID); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	batch, err := scanBulkFulfillmentBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.id=$1`, batchID))
	if err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	return batch, nil
}

func (p *Postgres) RunningBulkFulfillmentItems(ctx context.Context, limit int) (model.BulkFulfillmentBatch, []model.BulkFulfillmentItem, error) {
	if limit < 1 {
		limit = 1
	}
	batch, err := scanBulkFulfillmentBatch(p.pool.QueryRow(ctx, bulkBatchSelect+` WHERE b.status='running' ORDER BY b.created_at DESC LIMIT 1`))
	if err != nil {
		return model.BulkFulfillmentBatch{}, nil, err
	}
	items := make([]model.BulkFulfillmentItem, 0, limit)
	load := func(status string, itemLimit int) error {
		if itemLimit < 1 {
			return nil
		}
		rows, queryErr := p.pool.Query(ctx, `
		SELECT batch_id,sequence_no,parent_order_sn,status,last_error,updated_at
		FROM temu_bulk_fulfillment_items
		WHERE batch_id=$1 AND status=$2
		ORDER BY sequence_no LIMIT $3
	`, batch.ID, status, itemLimit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item model.BulkFulfillmentItem
			if scanErr := rows.Scan(&item.BatchID, &item.SequenceNo, &item.ParentOrderSN, &item.Status, &item.LastError, &item.UpdatedAt); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	}
	if err := load("running", limit); err != nil {
		return model.BulkFulfillmentBatch{}, nil, err
	}
	if err := load("pending", limit-len(items)); err != nil {
		return model.BulkFulfillmentBatch{}, nil, err
	}
	if len(items) == 0 {
		return batch, nil, pgx.ErrNoRows
	}
	return batch, items, nil
}

func (p *Postgres) MarkBulkFulfillmentItemRunning(ctx context.Context, batchID, parentOrderSN string) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE temu_bulk_fulfillment_items SET status='running',last_error='',updated_at=now()
		WHERE batch_id=$1 AND parent_order_sn=$2 AND status='pending'
	`, batchID, parentOrderSN)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (p *Postgres) FinishBulkFulfillmentItem(ctx context.Context, batchID, parentOrderSN, status, lastError string) (model.BulkFulfillmentBatch, error) {
	if status != "succeeded" && status != "failed" {
		return model.BulkFulfillmentBatch{}, errors.New("invalid bulk fulfillment item status")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE temu_bulk_fulfillment_items SET status=$3,last_error=$4,updated_at=now()
		WHERE batch_id=$1 AND parent_order_sn=$2 AND status IN ('pending','running')
	`, batchID, parentOrderSN, status, lastError); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	var succeeded, failed, remaining int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status='succeeded'),count(*) FILTER (WHERE status='failed'),
			count(*) FILTER (WHERE status IN ('pending','running'))
		FROM temu_bulk_fulfillment_items WHERE batch_id=$1
	`, batchID).Scan(&succeeded, &failed, &remaining); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	batchStatus := "running"
	if failed > 0 {
		batchStatus = "stopped"
	} else if remaining == 0 {
		batchStatus = "completed"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE temu_bulk_fulfillment_batches SET status=$2,succeeded_orders=$3,failed_orders=$4,
			failed_order_sn=CASE WHEN $2='stopped' THEN $5 ELSE '' END,
			last_error=CASE WHEN $2='stopped' THEN $6 ELSE '' END,
			updated_at=now(),completed_at=CASE WHEN $2 IN ('stopped','completed') THEN now() ELSE NULL END
		WHERE id=$1
	`, batchID, batchStatus, succeeded, failed, parentOrderSN, lastError); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	batch, err := scanBulkFulfillmentBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.id=$1`, batchID))
	if err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	return batch, nil
}

func scanBulkFulfillmentBatch(row pgx.Row) (model.BulkFulfillmentBatch, error) {
	var item model.BulkFulfillmentBatch
	err := row.Scan(&item.ID, &item.Status, &item.TotalOrders, &item.SucceededOrders, &item.FailedOrders,
		&item.CurrentOrderSN, &item.FailedOrderSN, &item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
	return item, err
}
