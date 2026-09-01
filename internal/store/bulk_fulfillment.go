package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"temu-api-manager/internal/model"

	"github.com/jackc/pgx/v5"
)

var ErrBulkInventoryCapacity = errors.New("替换批次库存额度不足")

const bulkBatchSelect = `SELECT b.id,b.fulfillment_mode,b.status,b.total_orders,b.succeeded_orders,b.failed_orders,
	coalesce((SELECT i.parent_order_sn FROM temu_bulk_fulfillment_items i
		WHERE i.batch_id=b.id AND i.status IN ('pending','running') ORDER BY i.sequence_no LIMIT 1),''),
	b.failed_order_sn,b.last_error,b.created_at,b.updated_at,b.completed_at
	FROM temu_bulk_fulfillment_batches b`

func (p *Postgres) CreateBulkFulfillmentBatch(ctx context.Context, batchID, fulfillmentMode string, substitutionSKUs []string) (model.BulkFulfillmentBatch, bool, error) {
	if fulfillmentMode != model.FulfillmentModeDirect && fulfillmentMode != model.FulfillmentModeSubstitution {
		return model.BulkFulfillmentBatch{}, false, errors.New("invalid fulfillment mode")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.BulkFulfillmentBatch{}, false, err
	}
	defer tx.Rollback(ctx)

	existing, err := scanBulkFulfillmentBatch(tx.QueryRow(ctx, bulkBatchSelect+` WHERE b.status='running' AND b.fulfillment_mode=$1 ORDER BY b.created_at DESC LIMIT 1`, fulfillmentMode))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.BulkFulfillmentBatch{}, false, err
	}

	query := `
		SELECT o.parent_order_sn
		FROM temu_orders o
		WHERE o.is_open AND o.parent_order_status=2
		  AND NOT EXISTS (SELECT 1 FROM temu_shipment_orders queued WHERE queued.parent_order_sn=o.parent_order_sn)
		  AND NOT EXISTS (
			SELECT 1 FROM temu_bulk_fulfillment_items active_item
			JOIN temu_bulk_fulfillment_batches active_batch ON active_batch.id=active_item.batch_id
			WHERE active_item.parent_order_sn=o.parent_order_sn AND active_batch.status='running'
		  )
		  AND EXISTS (
			SELECT 1 FROM temu_order_warehouse_checks warehouse_check
			WHERE warehouse_check.parent_order_sn=o.parent_order_sn
			  AND warehouse_check.source_update_time=coalesce(o.update_time,0)
			  AND warehouse_check.status='eligible'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM temu_order_manual_reviews manual
			WHERE manual.parent_order_sn=o.parent_order_sn
			  AND ((manual.status='resolved' AND manual.outcome<>'') OR
			       (manual.active AND (manual.status<>'approved' OR manual.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[])))
		  )
		ORDER BY o.expect_ship_latest_time NULLS LAST,o.update_time,o.parent_order_sn
		FOR UPDATE OF o
	`
	args := []any{}
	if fulfillmentMode == model.FulfillmentModeSubstitution {
		if len(substitutionSKUs) == 0 {
			return model.BulkFulfillmentBatch{}, false, errors.New("没有启用的替代发货组合")
		}
		query = `
			SELECT o.parent_order_sn
			FROM temu_orders o
			WHERE o.is_open AND o.parent_order_status=2
			  AND NOT EXISTS (SELECT 1 FROM temu_shipment_orders queued WHERE queued.parent_order_sn=o.parent_order_sn)
			  AND NOT EXISTS (
				SELECT 1 FROM temu_bulk_fulfillment_items active_item
				JOIN temu_bulk_fulfillment_batches active_batch ON active_batch.id=active_item.batch_id
				WHERE active_item.parent_order_sn=o.parent_order_sn AND active_batch.status='running'
			  )
			  AND (SELECT count(*) FROM temu_order_lines line_count WHERE line_count.parent_order_sn=o.parent_order_sn)=1
			  AND EXISTS (
				SELECT 1 FROM temu_order_lines candidate
				WHERE candidate.parent_order_sn=o.parent_order_sn
				  AND candidate.quantity=1 AND btrim(candidate.ext_code)=ANY($1::text[])
			  )
			ORDER BY o.expect_ship_latest_time NULLS LAST,o.update_time,o.parent_order_sn
			FOR UPDATE OF o
		`
		args = append(args, substitutionSKUs)
	}
	rows, err := tx.Query(ctx, query, args...)
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
		if fulfillmentMode == model.FulfillmentModeSubstitution {
			return model.BulkFulfillmentBatch{}, false, errors.New("没有可替换发货的待发货订单")
		}
		return model.BulkFulfillmentBatch{}, false, errors.New("没有可自动发货的待发货订单")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO temu_bulk_fulfillment_batches(id,fulfillment_mode,status,total_orders)
		VALUES($1,$2,'running',$3)
	`, batchID, fulfillmentMode, len(parents)); err != nil {
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

func (p *Postgres) LatestBulkFulfillmentBatch(ctx context.Context, fulfillmentMode string) (model.BulkFulfillmentBatch, error) {
	return scanBulkFulfillmentBatch(p.pool.QueryRow(ctx, bulkBatchSelect+` WHERE b.fulfillment_mode=$1 ORDER BY b.created_at DESC LIMIT 1`, fulfillmentMode))
}

func (p *Postgres) RestartBulkFulfillmentBatch(ctx context.Context, fulfillmentMode string) (model.BulkFulfillmentBatch, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	defer tx.Rollback(ctx)
	var batchID string
	if err := tx.QueryRow(ctx, `
SELECT id FROM temu_bulk_fulfillment_batches
WHERE status IN ('running','stopped') AND fulfillment_mode=$1
ORDER BY (status='running') DESC,created_at DESC
LIMIT 1 FOR UPDATE
`, fulfillmentMode).Scan(&batchID); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO temu_auto_fulfillment_jobs(parent_order_sn,fulfillment_mode,status)
SELECT i.parent_order_sn,b.fulfillment_mode,'queued'
FROM temu_bulk_fulfillment_items i
JOIN temu_bulk_fulfillment_batches b ON b.id=i.batch_id
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
fulfillment_mode=(SELECT fulfillment_mode FROM temu_bulk_fulfillment_batches WHERE id=$1),
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
	batch, err := scanBulkFulfillmentBatch(p.pool.QueryRow(ctx, bulkBatchSelect+`
		WHERE b.status='running'
		ORDER BY EXISTS (
			SELECT 1 FROM temu_bulk_fulfillment_items pending
			WHERE pending.batch_id=b.id AND pending.status='pending'
		) DESC,b.updated_at,b.created_at LIMIT 1`))
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
		WITH marked AS (
			UPDATE temu_bulk_fulfillment_items SET status='running',last_error='',updated_at=now()
			WHERE batch_id=$1 AND parent_order_sn=$2 AND status='pending'
			RETURNING batch_id
		)
		UPDATE temu_bulk_fulfillment_batches batch SET updated_at=now()
		FROM marked WHERE batch.id=marked.batch_id
	`, batchID, parentOrderSN)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (p *Postgres) ReserveBulkSubstitutionInventory(ctx context.Context, parentOrderSN, warehouseKey string, rawItems []model.BulkInventoryReservationItem) (bool, error) {
	parentOrderSN = strings.TrimSpace(parentOrderSN)
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	items, err := normalizeBulkInventoryReservationItems(rawItems)
	if err != nil {
		return false, err
	}
	if parentOrderSN == "" || warehouseKey == "" {
		return false, errors.New("bulk inventory reservation requires an order and warehouse")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var batchID string
	err = tx.QueryRow(ctx, `
		SELECT item.batch_id
		FROM temu_bulk_fulfillment_items item
		JOIN temu_bulk_fulfillment_batches batch ON batch.id=item.batch_id
		WHERE item.parent_order_sn=$1 AND item.status='running'
		  AND batch.status='running' AND batch.fulfillment_mode='substitution'
		ORDER BY batch.created_at DESC LIMIT 1
		FOR UPDATE OF batch
	`, parentOrderSN).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	existing, err := bulkInventoryReservations(ctx, tx, batchID, parentOrderSN)
	if err != nil {
		return false, err
	}
	if sameBulkInventoryReservation(existing, warehouseKey, items) {
		return true, tx.Commit(ctx)
	}
	if len(existing) > 0 {
		if err := releaseBulkInventoryReservation(ctx, tx, batchID, parentOrderSN); err != nil {
			return false, err
		}
	}

	for _, item := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO temu_bulk_inventory_budgets(batch_id,oms_warehouse_key,warehouse_sku,capacity)
			VALUES($1,$2,$3,$4)
			ON CONFLICT(batch_id,oms_warehouse_key,warehouse_sku) DO NOTHING
		`, batchID, warehouseKey, item.WarehouseSKU, item.AvailableStock); err != nil {
			return false, err
		}
		var capacity, reserved int
		if err := tx.QueryRow(ctx, `
			SELECT capacity,reserved_quantity FROM temu_bulk_inventory_budgets
			WHERE batch_id=$1 AND oms_warehouse_key=$2 AND warehouse_sku=$3
			FOR UPDATE
		`, batchID, warehouseKey, item.WarehouseSKU).Scan(&capacity, &reserved); err != nil {
			return false, err
		}
		if !bulkInventoryCapacityAllows(capacity, reserved, item.Quantity) {
			return false, fmt.Errorf("%w: warehouse %s SKU %s has %d units left in this batch, requires %d",
				ErrBulkInventoryCapacity, warehouseKey, item.WarehouseSKU, capacity-reserved, item.Quantity)
		}
	}
	for _, item := range items {
		if _, err := tx.Exec(ctx, `
			UPDATE temu_bulk_inventory_budgets
			SET reserved_quantity=reserved_quantity+$4,updated_at=now()
			WHERE batch_id=$1 AND oms_warehouse_key=$2 AND warehouse_sku=$3
		`, batchID, warehouseKey, item.WarehouseSKU, item.Quantity); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO temu_bulk_inventory_reservations(
				batch_id,parent_order_sn,oms_warehouse_key,warehouse_sku,quantity
			) VALUES($1,$2,$3,$4,$5)
		`, batchID, parentOrderSN, warehouseKey, item.WarehouseSKU, item.Quantity); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

func (p *Postgres) ReleaseBulkSubstitutionInventory(ctx context.Context, parentOrderSN string) (bool, error) {
	parentOrderSN = strings.TrimSpace(parentOrderSN)
	if parentOrderSN == "" {
		return false, errors.New("bulk inventory release requires an order")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var batchID string
	err = tx.QueryRow(ctx, `
		SELECT reservation.batch_id
		FROM temu_bulk_inventory_reservations reservation
		JOIN temu_bulk_fulfillment_batches batch ON batch.id=reservation.batch_id
		WHERE reservation.parent_order_sn=$1 AND batch.status='running'
		ORDER BY batch.created_at DESC LIMIT 1
		FOR UPDATE OF batch
	`, parentOrderSN).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := releaseBulkInventoryReservation(ctx, tx, batchID, parentOrderSN); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type bulkInventoryReservation struct {
	WarehouseKey string
	WarehouseSKU string
	Quantity     int
}

func bulkInventoryReservations(ctx context.Context, tx pgx.Tx, batchID, parentOrderSN string) ([]bulkInventoryReservation, error) {
	rows, err := tx.Query(ctx, `
		SELECT oms_warehouse_key,warehouse_sku,quantity
		FROM temu_bulk_inventory_reservations
		WHERE batch_id=$1 AND parent_order_sn=$2
		ORDER BY warehouse_sku
	`, batchID, parentOrderSN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]bulkInventoryReservation, 0)
	for rows.Next() {
		var item bulkInventoryReservation
		if err := rows.Scan(&item.WarehouseKey, &item.WarehouseSKU, &item.Quantity); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func releaseBulkInventoryReservation(ctx context.Context, tx pgx.Tx, batchID, parentOrderSN string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE temu_bulk_inventory_budgets budget
		SET reserved_quantity=budget.reserved_quantity-released.quantity,updated_at=now()
		FROM temu_bulk_inventory_reservations released
		WHERE released.batch_id=$1 AND released.parent_order_sn=$2
		  AND budget.batch_id=released.batch_id
		  AND budget.oms_warehouse_key=released.oms_warehouse_key
		  AND budget.warehouse_sku=released.warehouse_sku
	`, batchID, parentOrderSN); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM temu_bulk_inventory_reservations WHERE batch_id=$1 AND parent_order_sn=$2
	`, batchID, parentOrderSN)
	return err
}

func normalizeBulkInventoryReservationItems(rawItems []model.BulkInventoryReservationItem) ([]model.BulkInventoryReservationItem, error) {
	quantities := make(map[string]int, len(rawItems))
	available := make(map[string]int, len(rawItems))
	for _, item := range rawItems {
		sku := strings.TrimSpace(item.WarehouseSKU)
		if sku == "" || item.Quantity <= 0 || item.AvailableStock < 0 {
			return nil, errors.New("bulk inventory reservation items are invalid")
		}
		quantities[sku] += item.Quantity
		if previous, exists := available[sku]; exists && previous != item.AvailableStock {
			return nil, errors.New("bulk inventory reservation has conflicting available stock")
		}
		available[sku] = item.AvailableStock
	}
	if len(quantities) == 0 {
		return nil, errors.New("bulk inventory reservation items are required")
	}
	skus := make([]string, 0, len(quantities))
	for sku := range quantities {
		skus = append(skus, sku)
	}
	sort.Strings(skus)
	items := make([]model.BulkInventoryReservationItem, 0, len(skus))
	for _, sku := range skus {
		items = append(items, model.BulkInventoryReservationItem{
			WarehouseSKU: sku, Quantity: quantities[sku], AvailableStock: available[sku],
		})
	}
	return items, nil
}

func sameBulkInventoryReservation(existing []bulkInventoryReservation, warehouseKey string, items []model.BulkInventoryReservationItem) bool {
	if len(existing) != len(items) {
		return false
	}
	for index := range items {
		if existing[index].WarehouseKey != warehouseKey || existing[index].WarehouseSKU != items[index].WarehouseSKU || existing[index].Quantity != items[index].Quantity {
			return false
		}
	}
	return true
}

func bulkInventoryCapacityAllows(capacity, reserved, requested int) bool {
	return capacity >= 0 && reserved >= 0 && requested > 0 && reserved <= capacity-requested
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
	var failedOrderSN, lastBatchError string
	if err := tx.QueryRow(ctx, `
		SELECT failed_order_sn,last_error FROM temu_bulk_fulfillment_batches WHERE id=$1 FOR UPDATE
	`, batchID).Scan(&failedOrderSN, &lastBatchError); err != nil {
		return model.BulkFulfillmentBatch{}, err
	}
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
	if status == "failed" {
		failedOrderSN = parentOrderSN
		lastBatchError = lastError
	}
	batchStatus := bulkFulfillmentBatchStatus(remaining)
	if _, err := tx.Exec(ctx, `
		UPDATE temu_bulk_fulfillment_batches SET status=$2,succeeded_orders=$3,failed_orders=$4,
			failed_order_sn=$5,last_error=$6,
			updated_at=now(),completed_at=CASE WHEN $2='completed' THEN now() ELSE NULL END
		WHERE id=$1
	`, batchID, batchStatus, succeeded, failed, failedOrderSN, lastBatchError); err != nil {
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

func bulkFulfillmentBatchStatus(remaining int) string {
	if remaining > 0 {
		return "running"
	}
	return "completed"
}

func scanBulkFulfillmentBatch(row pgx.Row) (model.BulkFulfillmentBatch, error) {
	var item model.BulkFulfillmentBatch
	err := row.Scan(&item.ID, &item.FulfillmentMode, &item.Status, &item.TotalOrders, &item.SucceededOrders, &item.FailedOrders,
		&item.CurrentOrderSN, &item.FailedOrderSN, &item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
	return item, err
}
