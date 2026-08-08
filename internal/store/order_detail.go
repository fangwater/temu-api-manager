package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"temu-api-manager/internal/model"

	"github.com/jackc/pgx/v5"
)

type DetailCandidate struct {
	ParentOrderSN  string
	SourceUpdateAt int64
	Open           bool
}

func (p *Postgres) DetailCandidates(ctx context.Context, limit int) ([]DetailCandidate, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := p.pool.Query(ctx, `
		SELECT o.parent_order_sn,coalesce(o.update_time,0),o.is_open
		FROM temu_orders o
		LEFT JOIN temu_order_details d ON d.parent_order_sn=o.parent_order_sn
		WHERE d.parent_order_sn IS NULL
		   OR (o.is_open AND d.source_update_time < coalesce(o.update_time,0))
		   OR (NOT o.is_open AND d.order_open_at_fetch)
		ORDER BY (d.parent_order_sn IS NULL) DESC,o.is_open DESC,d.fetched_at NULLS FIRST,o.update_time DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DetailCandidate, 0, limit)
	for rows.Next() {
		var item DetailCandidate
		if err := rows.Scan(&item.ParentOrderSN, &item.SourceUpdateAt, &item.Open); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (p *Postgres) SaveOrderDetail(ctx context.Context, item model.OrderDetail) error {
	if item.BatchOrderSNs == nil {
		item.BatchOrderSNs = []string{}
	}
	if item.RegionNames == nil {
		item.RegionNames = []string{}
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO temu_order_details(parent_order_sn,source_update_time,detail_payload,
			batch_order_number_list,is_shipment_consolidated,region_names,order_open_at_fetch,fetched_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT(parent_order_sn) DO UPDATE SET source_update_time=EXCLUDED.source_update_time,
			detail_payload=EXCLUDED.detail_payload,batch_order_number_list=EXCLUDED.batch_order_number_list,
			is_shipment_consolidated=EXCLUDED.is_shipment_consolidated,region_names=EXCLUDED.region_names,
			order_open_at_fetch=EXCLUDED.order_open_at_fetch,fetched_at=now()
	`, item.ParentOrderSN, item.SourceUpdateAt, jsonOrEmpty(item.Raw, "{}"), item.BatchOrderSNs,
		item.Consolidated, item.RegionNames, item.OpenAtFetch)
	return err
}

func (p *Postgres) GetOrderDetail(ctx context.Context, parentOrderSN string) (model.OrderDetail, error) {
	var item model.OrderDetail
	err := p.pool.QueryRow(ctx, `
		SELECT parent_order_sn,source_update_time,batch_order_number_list,is_shipment_consolidated,
			region_names,order_open_at_fetch,fetched_at,detail_payload
		FROM temu_order_details WHERE parent_order_sn=$1
	`, parentOrderSN).Scan(&item.ParentOrderSN, &item.SourceUpdateAt, &item.BatchOrderSNs,
		&item.Consolidated, &item.RegionNames, &item.OpenAtFetch, &item.FetchedAt, &item.Raw)
	return item, err
}

func (p *Postgres) attachOrderDetails(ctx context.Context, orders []model.Order) error {
	if len(orders) == 0 {
		return nil
	}
	ids, indexes := orderIndexes(orders)
	rows, err := p.pool.Query(ctx, `
		SELECT parent_order_sn,source_update_time,batch_order_number_list,is_shipment_consolidated,
			region_names,order_open_at_fetch,fetched_at
		FROM temu_order_details WHERE parent_order_sn=ANY($1)
	`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item model.OrderDetail
		if err := rows.Scan(&item.ParentOrderSN, &item.SourceUpdateAt, &item.BatchOrderSNs,
			&item.Consolidated, &item.RegionNames, &item.OpenAtFetch, &item.FetchedAt); err != nil {
			return err
		}
		orders[indexes[item.ParentOrderSN]].Detail = &item
	}
	return rows.Err()
}

func (p *Postgres) attachManualReviews(ctx context.Context, orders []model.Order) error {
	if len(orders) == 0 {
		return nil
	}
	ids, indexes := orderIndexes(orders)
	rows, err := p.pool.Query(ctx, `
		SELECT parent_order_sn,reasons,merge_order_sn_list,status,active,detected_at,updated_at,approved_at
		FROM temu_order_manual_reviews WHERE parent_order_sn=ANY($1)
	`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanManualReview(rows)
		if err != nil {
			return err
		}
		orders[indexes[item.ParentOrderSN]].ManualReview = &item
	}
	return rows.Err()
}

func orderIndexes(orders []model.Order) ([]string, map[string]int) {
	ids := make([]string, 0, len(orders))
	indexes := make(map[string]int, len(orders))
	for index := range orders {
		ids = append(ids, orders[index].ParentOrderSN)
		indexes[orders[index].ParentOrderSN] = index
	}
	return ids, indexes
}

func scanManualReview(row pgx.Row) (model.ManualReview, error) {
	var item model.ManualReview
	err := row.Scan(&item.ParentOrderSN, &item.Reasons, &item.MergeOrderSNs, &item.Status,
		&item.Active, &item.DetectedAt, &item.UpdatedAt, &item.ApprovedAt)
	return item, err
}

func (p *Postgres) ListManualReviews(ctx context.Context, status string, page, pageSize int) ([]model.ManualReview, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	where := "WHERE active"
	args := []any{}
	if status = strings.TrimSpace(status); status != "" {
		args = append(args, status)
		where += " AND status=$1"
	}
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM temu_order_manual_reviews `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limitPosition := len(args) + 1
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := p.pool.Query(ctx, fmt.Sprintf(`
		SELECT parent_order_sn,reasons,merge_order_sn_list,status,active,detected_at,updated_at,approved_at
		FROM temu_order_manual_reviews %s ORDER BY updated_at DESC LIMIT $%d OFFSET $%d
	`, where, limitPosition, limitPosition+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.ManualReview, 0, pageSize)
	for rows.Next() {
		item, err := scanManualReview(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for index := range items {
		order, err := p.GetOrder(ctx, items[index].ParentOrderSN)
		if err != nil {
			return nil, 0, err
		}
		items[index].Lines = order.Lines
		items[index].Detail = order.Detail
		classification, classificationErr := p.WarehouseClassification(ctx, items[index].ParentOrderSN)
		if classificationErr == nil {
			items[index].Details = classification.ReasonDetails
		} else if !errors.Is(classificationErr, pgx.ErrNoRows) {
			return nil, 0, classificationErr
		}
	}
	return items, total, nil
}

func (p *Postgres) UpsertManualReview(ctx context.Context, review model.ManualReview) error {
	if review.MergeOrderSNs == nil {
		review.MergeOrderSNs = []string{}
	}
	if len(review.Reasons) == 0 {
		return nil
	}
	_, err := p.pool.Exec(ctx, `
											INSERT INTO temu_order_manual_reviews(parent_order_sn,reasons,merge_order_sn_list,status,active)
													VALUES($1,$2,$3,'detected',true)
															ON CONFLICT(parent_order_sn) DO UPDATE SET
																		reasons=ARRAY(
																						SELECT DISTINCT preserved.reason
																										FROM unnest(
																															EXCLUDED.reasons
																																				|| CASE WHEN temu_order_manual_reviews.active THEN ARRAY(
																																										SELECT reason FROM unnest(temu_order_manual_reviews.reasons) AS dynamic(reason)
																																																WHERE reason IN ('sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported')
																																																					) ELSE ARRAY[]::text[] END
																																																									) AS preserved(reason) ORDER BY preserved.reason
																																																												),
																																																															merge_order_sn_list=EXCLUDED.merge_order_sn_list,active=true,updated_at=now(),
																																																																		status=CASE
																																																																						WHEN EXCLUDED.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]
																																																																											OR (temu_order_manual_reviews.active AND temu_order_manual_reviews.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[])
																																																																															THEN CASE WHEN temu_order_manual_reviews.status='detected' THEN 'detected' ELSE 'manual_pending' END
																																																																																			WHEN temu_order_manual_reviews.status IN ('manual_pending','approved')
																																																																																							THEN temu_order_manual_reviews.status ELSE 'detected' END,
																																																																																										approved_at=CASE
																																																																																														WHEN EXCLUDED.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]
																																																																																																			OR (temu_order_manual_reviews.active AND temu_order_manual_reviews.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[])
																																																																																																							THEN NULL ELSE temu_order_manual_reviews.approved_at END
																																																																																																								`, review.ParentOrderSN, review.Reasons, review.MergeOrderSNs)
	return err
}
func (p *Postgres) ClearManualReviewReason(ctx context.Context, parentOrderSN, reason string) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE temu_order_manual_reviews SET
			reasons=array_remove(reasons,$2),
			active=(cardinality(array_remove(reasons,$2)) > 0),
			status=CASE
				WHEN cardinality(array_remove(reasons,$2))=0 THEN 'resolved'
				WHEN status='approved' THEN 'manual_pending'
				ELSE status END,
			approved_at=NULL,updated_at=now()
		WHERE parent_order_sn=$1 AND $2=ANY(reasons)
	`, strings.TrimSpace(parentOrderSN), strings.TrimSpace(reason))
	return err
}

func (p *Postgres) UpdateManualReview(ctx context.Context, parentOrderSN, status string) (model.ManualReview, error) {
	if status != "manual_pending" && status != "approved" && status != "resolved" {
		return model.ManualReview{}, errors.New("invalid manual review status")
	}
	item, err := scanManualReview(p.pool.QueryRow(ctx, `
		UPDATE temu_order_manual_reviews SET status=$2,active=($2<>'resolved'),updated_at=now(),
			approved_at=CASE WHEN $2='approved' THEN now() ELSE NULL END
		WHERE parent_order_sn=$1
		RETURNING parent_order_sn,reasons,merge_order_sn_list,status,active,detected_at,updated_at,approved_at
	`, strings.TrimSpace(parentOrderSN), status))
	if err != nil {
		return item, err
	}
	order, err := p.GetOrder(ctx, item.ParentOrderSN)
	if err != nil {
		return item, err
	}
	item.Lines = order.Lines
	item.Detail = order.Detail
	return item, nil
}

func (p *Postgres) ListWarehouseClassificationCandidates(ctx context.Context, limit int) ([]model.Order, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `
        SELECT o.parent_order_sn
        FROM temu_orders o
        LEFT JOIN temu_order_warehouse_checks warehouse_check ON warehouse_check.parent_order_sn=o.parent_order_sn
        WHERE o.is_open AND o.parent_order_status=2
          AND NOT EXISTS (SELECT 1 FROM temu_shipment_orders queued WHERE queued.parent_order_sn=o.parent_order_sn)
          AND NOT EXISTS (
            SELECT 1 FROM temu_order_manual_reviews manual
            WHERE manual.parent_order_sn=o.parent_order_sn AND manual.active AND manual.status<>'approved'
              AND EXISTS (
                SELECT 1 FROM unnest(manual.reasons) AS classified(reason)
                WHERE classified.reason NOT IN ('sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported')
              )
          )
          AND (
            warehouse_check.parent_order_sn IS NULL
            OR warehouse_check.source_update_time<>coalesce(o.update_time,0)
            OR warehouse_check.checked_at < now()-interval '5 minutes'
          )
        ORDER BY warehouse_check.checked_at NULLS FIRST,o.expect_ship_latest_time NULLS LAST,o.update_time DESC
        LIMIT $1
    `, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			return nil, err
		}
		ids = append(ids, parent)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	orders := make([]model.Order, 0, len(ids))
	for _, parent := range ids {
		order, err := p.GetOrder(ctx, parent)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func (p *Postgres) SaveWarehouseClassification(ctx context.Context, item model.WarehouseClassification) error {
	if item.Status != "eligible" && item.Status != "manual" && item.Status != "failed" {
		return errors.New("invalid warehouse classification status")
	}
	if item.Categories == nil {
		item.Categories = []string{}
	}
	if item.ReasonDetails == nil {
		item.ReasonDetails = []string{}
	}
	_, err := p.pool.Exec(ctx, `
															INSERT INTO temu_order_warehouse_checks(
																		parent_order_sn,source_update_time,status,categories,reason_details,error_message,checked_at
																				) VALUES($1,$2,$3,$4,$5,$6,now())
																						ON CONFLICT(parent_order_sn) DO UPDATE SET
																									source_update_time=EXCLUDED.source_update_time,status=EXCLUDED.status,
																												categories=EXCLUDED.categories,reason_details=EXCLUDED.reason_details,
																															error_message=EXCLUDED.error_message,checked_at=now()
																																`, item.ParentOrderSN, item.SourceUpdateAt, item.Status, item.Categories, item.ReasonDetails, item.ErrorMessage)
	return err
}

func (p *Postgres) WarehouseClassification(ctx context.Context, parentOrderSN string) (model.WarehouseClassification, error) {
	var item model.WarehouseClassification
	err := p.pool.QueryRow(ctx, `
				SELECT parent_order_sn,source_update_time,status,categories,reason_details,error_message,checked_at
						FROM temu_order_warehouse_checks WHERE parent_order_sn=$1
							`, strings.TrimSpace(parentOrderSN)).Scan(&item.ParentOrderSN, &item.SourceUpdateAt, &item.Status,
		&item.Categories, &item.ReasonDetails, &item.ErrorMessage, &item.CheckedAt)
	return item, err
}
func (p *Postgres) ListOrderHistory(ctx context.Context, page, pageSize int) ([]model.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM temu_order_details`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.pool.Query(ctx, `
		SELECT o.parent_order_sn,o.parent_order_status,o.fulfillment_type,coalesce(o.region_id,0),
			coalesce(o.expect_ship_latest_time,0),coalesce(o.update_time,0),o.order_labels,
			o.fulfillment_warnings,o.raw_payload,o.is_open,o.first_seen_at,o.last_seen_at,o.last_synced_at
		FROM temu_order_details d JOIN temu_orders o ON o.parent_order_sn=d.parent_order_sn
		ORDER BY d.fetched_at DESC LIMIT $1 OFFSET $2
	`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.Order, 0, pageSize)
	for rows.Next() {
		var item model.Order
		if err := rows.Scan(&item.ParentOrderSN, &item.Status, &item.FulfillmentType, &item.RegionID,
			&item.ExpectShipLatestTime, &item.UpdateTime, &item.Labels, &item.Warnings, &item.Raw,
			&item.Open, &item.FirstSeenAt, &item.LastSeenAt, &item.LastSyncedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := p.attachLines(ctx, items); err != nil {
		return nil, 0, err
	}
	if err := p.attachManualReviews(ctx, items); err != nil {
		return nil, 0, err
	}
	if err := p.attachOrderDetails(ctx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
