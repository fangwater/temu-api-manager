package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool     *pgxpool.Pool
	shopCode string
}

var postgresSchemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	return NewPostgresInSchema(ctx, databaseURL, "public")
}

func NewPostgresInSchema(ctx context.Context, databaseURL, schemaName string) (*Postgres, error) {
	return NewPostgresForShop(ctx, databaseURL, schemaName, "")
}

func NewPostgresForShop(ctx context.Context, databaseURL, schemaName, shopCode string) (*Postgres, error) {
	if !postgresSchemaPattern.MatchString(schemaName) {
		return nil, errors.New("invalid PostgreSQL shop schema")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return &Postgres{pool: pool, shopCode: strings.TrimSpace(shopCode)}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Migrate(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, migrations.InitSQL); err != nil {
		return fmt.Errorf("apply Temu migration: %w", err)
	}
	return nil
}

func (p *Postgres) StartSync(ctx context.Context) (int64, time.Time, error) {
	var id int64
	var started time.Time
	err := p.pool.QueryRow(ctx, `INSERT INTO temu_sync_runs(status) VALUES ('running') RETURNING id, started_at`).Scan(&id, &started)
	return id, started, err
}

func (p *Postgres) FinishSync(ctx context.Context, id int64, status string, orders, lines int, message string) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE temu_sync_runs SET status=$2, completed_at=now(), fetched_orders=$3,
		       fetched_lines=$4, error_message=$5 WHERE id=$1
	`, id, status, orders, lines, message)
	return err
}

func (p *Postgres) LatestSync(ctx context.Context) (model.SyncStatus, error) {
	var item model.SyncStatus
	err := p.pool.QueryRow(ctx, `
		SELECT id, status, started_at, completed_at, fetched_orders, fetched_lines, error_message
		FROM temu_sync_runs ORDER BY id DESC LIMIT 1
	`).Scan(&item.ID, &item.Status, &item.StartedAt, &item.CompletedAt, &item.FetchedOrders, &item.FetchedLines, &item.ErrorMessage)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.SyncStatus{}, nil
	}
	return item, err
}

func (p *Postgres) ReplaceOpenOrders(ctx context.Context, orders []model.Order, syncedAt time.Time) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	lineCount := 0
	for _, order := range orders {
		if _, err := tx.Exec(ctx, `
			INSERT INTO temu_orders (
				parent_order_sn, parent_order_status, fulfillment_type, region_id,
				expect_ship_latest_time, update_time, order_labels, fulfillment_warnings,
				raw_payload, is_open, first_seen_at, last_seen_at, last_synced_at
			) VALUES ($1,$2,$3,NULLIF($4::bigint,0),NULLIF($5::bigint,0),NULLIF($6::bigint,0),$7,$8,$9,true,now(),now(),$10)
			ON CONFLICT (parent_order_sn) DO UPDATE SET
				parent_order_status=EXCLUDED.parent_order_status,
				fulfillment_type=EXCLUDED.fulfillment_type,
				region_id=EXCLUDED.region_id,
				expect_ship_latest_time=EXCLUDED.expect_ship_latest_time,
				update_time=EXCLUDED.update_time,
				order_labels=EXCLUDED.order_labels,
				fulfillment_warnings=EXCLUDED.fulfillment_warnings,
				raw_payload=EXCLUDED.raw_payload,
				is_open=true, last_seen_at=now(), last_synced_at=EXCLUDED.last_synced_at
		`, order.ParentOrderSN, order.Status, order.FulfillmentType, order.RegionID,
			order.ExpectShipLatestTime, order.UpdateTime, jsonOrEmpty(order.Labels, "[]"),
			jsonOrEmpty(order.Warnings, "[]"), jsonOrEmpty(order.Raw, "{}"), syncedAt); err != nil {
			return 0, fmt.Errorf("upsert order %s: %w", order.ParentOrderSN, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM temu_order_lines WHERE parent_order_sn=$1`, order.ParentOrderSN); err != nil {
			return 0, err
		}
		for _, line := range order.Lines {
			if _, err := tx.Exec(ctx, `
				INSERT INTO temu_order_lines (
					order_sn,parent_order_sn,order_status,quantity,goods_id,sku_id,
					ext_code,goods_name,spec,raw_payload
				) VALUES ($1,$2,$3,$4,NULLIF($5::bigint,0),NULLIF($6::bigint,0),$7,$8,$9,$10)
			`, line.OrderSN, order.ParentOrderSN, line.Status, line.Quantity, line.GoodsID,
				line.SKUID, line.ExtCode, line.GoodsName, line.Spec, jsonOrEmpty(line.Raw, "{}")); err != nil {
				return 0, fmt.Errorf("insert order line %s: %w", line.OrderSN, err)
			}
			lineCount++
		}
		if review := order.ManualReview; review != nil {
			if _, err := tx.Exec(ctx, `
											INSERT INTO temu_order_manual_reviews(parent_order_sn,reasons,merge_order_sn_list,status,active)
															VALUES($1,$2,$3,'detected',true)
																			ON CONFLICT(parent_order_sn) DO UPDATE SET
																							reasons=ARRAY(
																												SELECT DISTINCT preserved.reason
																																	FROM unnest(
																																							EXCLUDED.reasons
																																													|| CASE WHEN temu_order_manual_reviews.active
																																																				AND temu_order_manual_reviews.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]
																																																										THEN ARRAY(
																																																																	SELECT reason FROM unnest(temu_order_manual_reviews.reasons) AS dynamic(reason)
																																																																								WHERE reason IN ('sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported')
																																																																														) ELSE ARRAY[]::text[] END
																																																																																			) AS preserved(reason) ORDER BY preserved.reason
																																																																																							),
																																																																																											merge_order_sn_list=EXCLUDED.merge_order_sn_list,active=true,updated_at=now(),
																																																																																															status=CASE
																																																																																																				WHEN temu_order_manual_reviews.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]
																																																																																																										OR EXCLUDED.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]
																																																																																																															THEN CASE WHEN temu_order_manual_reviews.status='detected' THEN 'detected' ELSE 'manual_pending' END
																																																																																																																				WHEN temu_order_manual_reviews.status IN ('manual_pending','approved')
																																																																																																																									THEN temu_order_manual_reviews.status ELSE 'detected' END,
																																																																																																																													approved_at=CASE
																																																																																																																																		WHEN temu_order_manual_reviews.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]
																																																																																																																																								OR EXCLUDED.reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[] THEN NULL
																																																																																																																																													WHEN temu_order_manual_reviews.status='approved'
																																																																																																																																																		THEN temu_order_manual_reviews.approved_at ELSE NULL END
																																																																																																																																																					`, order.ParentOrderSN, review.Reasons, review.MergeOrderSNs); err != nil {
				return 0, fmt.Errorf("upsert manual review %s: %w", order.ParentOrderSN, err)
			}
		} else if _, err := tx.Exec(ctx, `
																																																																																																																																																																	UPDATE temu_order_manual_reviews SET
																																																																																																																																																																				active=(reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]),
																																																																																																																																																																							status=CASE
																																																																																																																																																																											WHEN reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[]
																																																																																																																																																																															THEN CASE WHEN status='detected' THEN 'detected' ELSE 'manual_pending' END
																																																																																																																																																																																			WHEN status='approved' THEN status ELSE 'resolved' END,
																																																																																																																																																																																						approved_at=CASE WHEN reasons && ARRAY['sku_unbound','inventory_rule','warehouse_sku_spec_incomplete','delivery_address_unsupported']::text[] THEN NULL ELSE approved_at END,
																																																																																																																																																																																									updated_at=now()
																																																																																																																																																																																												WHERE parent_order_sn=$1 AND active
																																																																																																																																																																																														`, order.ParentOrderSN); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE temu_orders SET is_open=false, last_synced_at=$1
		WHERE is_open AND last_synced_at < $1
	`, syncedAt); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE temu_order_manual_reviews m SET active=false,status='resolved',updated_at=now()
		FROM temu_orders o WHERE o.parent_order_sn=m.parent_order_sn AND NOT o.is_open AND m.active
	`); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return lineCount, nil
}

func (p *Postgres) ListOrders(ctx context.Context, query string, unreservedOnly bool, page, pageSize int) ([]model.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	where := "WHERE o.is_open"
	if unreservedOnly {
		where += ` AND o.parent_order_status=2
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
        )`
	}
	args := []any{}
	if query = strings.TrimSpace(query); query != "" {
		args = append(args, "%"+query+"%")
		where += ` AND (o.parent_order_sn ILIKE $1 OR EXISTS (
			SELECT 1 FROM temu_order_lines l WHERE l.parent_order_sn=o.parent_order_sn
			AND (l.order_sn ILIKE $1 OR l.ext_code ILIKE $1)))`
	}
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM temu_orders o `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limitPos := len(args) + 1
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := p.pool.Query(ctx, fmt.Sprintf(`
		SELECT o.parent_order_sn,o.parent_order_status,o.fulfillment_type,coalesce(o.region_id,0),
		       coalesce(o.expect_ship_latest_time,0),coalesce(o.update_time,0),o.order_labels,
		       o.fulfillment_warnings,o.is_open,o.first_seen_at,o.last_seen_at,o.last_synced_at,
		       s.id,s.status,s.shipping_company_name,s.tracking_number,s.created_at,
		       j.parent_order_sn,j.shipment_id,j.status,j.attempts,j.last_error,
		       j.created_at,j.updated_at,j.started_at,j.completed_at
		FROM temu_orders o
		LEFT JOIN temu_shipment_orders so ON so.parent_order_sn=o.parent_order_sn
		LEFT JOIN temu_shipments s ON s.id=so.shipment_id
		LEFT JOIN temu_auto_fulfillment_jobs j ON j.parent_order_sn=o.parent_order_sn
		%s ORDER BY o.expect_ship_latest_time NULLS LAST,o.update_time DESC
		LIMIT $%d OFFSET $%d
	`, where, limitPos, limitPos+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.Order, 0)
	for rows.Next() {
		var item model.Order
		var shipmentID, shipmentStatus, company, tracking *string
		var shipmentCreated *time.Time
		var jobParent, jobShipment, jobStatus, jobError *string
		var jobAttempts *int
		var jobCreated, jobUpdated, jobStarted, jobCompleted *time.Time
		if err := rows.Scan(&item.ParentOrderSN, &item.Status, &item.FulfillmentType, &item.RegionID,
			&item.ExpectShipLatestTime, &item.UpdateTime, &item.Labels, &item.Warnings, &item.Open,
			&item.FirstSeenAt, &item.LastSeenAt, &item.LastSyncedAt, &shipmentID, &shipmentStatus,
			&company, &tracking, &shipmentCreated, &jobParent, &jobShipment, &jobStatus,
			&jobAttempts, &jobError, &jobCreated, &jobUpdated, &jobStarted, &jobCompleted); err != nil {
			return nil, 0, err
		}
		if shipmentID != nil {
			item.Shipment = &model.ShipmentBrief{ID: *shipmentID, Status: value(shipmentStatus), ShippingCompanyName: value(company), TrackingNumber: value(tracking), CreatedAt: *shipmentCreated}
		}
		if jobParent != nil {
			item.AutoFulfillment = &model.AutoFulfillmentJob{ParentOrderSN: *jobParent, ShipmentID: value(jobShipment), Status: value(jobStatus), Attempts: intValue(jobAttempts), LastError: value(jobError), StartedAt: jobStarted, CompletedAt: jobCompleted}
			if jobCreated != nil {
				item.AutoFulfillment.CreatedAt = *jobCreated
			}
			if jobUpdated != nil {
				item.AutoFulfillment.UpdatedAt = *jobUpdated
			}
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

func (p *Postgres) GetOrder(ctx context.Context, parentOrderSN string) (model.Order, error) {
	var item model.Order
	err := p.pool.QueryRow(ctx, `
		SELECT parent_order_sn,parent_order_status,fulfillment_type,coalesce(region_id,0),
		       coalesce(expect_ship_latest_time,0),coalesce(update_time,0),order_labels,
		       fulfillment_warnings,raw_payload,is_open,first_seen_at,last_seen_at,last_synced_at
		FROM temu_orders WHERE parent_order_sn=$1
	`, parentOrderSN).Scan(&item.ParentOrderSN, &item.Status, &item.FulfillmentType, &item.RegionID,
		&item.ExpectShipLatestTime, &item.UpdateTime, &item.Labels, &item.Warnings, &item.Raw,
		&item.Open, &item.FirstSeenAt, &item.LastSeenAt, &item.LastSyncedAt)
	if err != nil {
		return model.Order{}, err
	}
	items := []model.Order{item}
	if err := p.attachLines(ctx, items); err != nil {
		return model.Order{}, err
	}
	if err := p.attachManualReviews(ctx, items); err != nil {
		return model.Order{}, err
	}
	if err := p.attachOrderDetails(ctx, items); err != nil {
		return model.Order{}, err
	}
	return items[0], nil
}

func (p *Postgres) attachLines(ctx context.Context, orders []model.Order) error {
	if len(orders) == 0 {
		return nil
	}
	ids := make([]string, 0, len(orders))
	indexes := make(map[string]int, len(orders))
	for i := range orders {
		ids = append(ids, orders[i].ParentOrderSN)
		indexes[orders[i].ParentOrderSN] = i
		orders[i].Lines = []model.OrderLine{}
	}
	rows, err := p.pool.Query(ctx, `
		SELECT order_sn,parent_order_sn,order_status,quantity,coalesce(goods_id,0),coalesce(sku_id,0),
		       ext_code,goods_name,spec FROM temu_order_lines WHERE parent_order_sn=ANY($1) ORDER BY order_sn
	`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line model.OrderLine
		if err := rows.Scan(&line.OrderSN, &line.ParentOrderSN, &line.Status, &line.Quantity, &line.GoodsID, &line.SKUID, &line.ExtCode, &line.GoodsName, &line.Spec); err != nil {
			return err
		}
		i := indexes[line.ParentOrderSN]
		orders[i].Lines = append(orders[i].Lines, line)
	}
	return rows.Err()
}

// Warehouse registrations and mappings are shared by every shop schema.
func (p *Postgres) ReplaceWarehouses(ctx context.Context, warehouses []model.Warehouse) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if p.shopCode != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM public.temu_shop_warehouses WHERE shop_code=$1`, p.shopCode); err != nil {
			return err
		}
	}
	for _, item := range warehouses {
		if _, err := tx.Exec(ctx, `
INSERT INTO public.temu_warehouses(warehouse_id,warehouse_name,region_id,enable_buy_shipping_label,
			default_warehouse,warehouse_management_type,raw_payload,synced_at)
			VALUES($1,$2,NULLIF($3::bigint,0),$4,$5,$6,$7,now())
			ON CONFLICT(warehouse_id) DO UPDATE SET warehouse_name=EXCLUDED.warehouse_name,
			region_id=EXCLUDED.region_id,enable_buy_shipping_label=EXCLUDED.enable_buy_shipping_label,
			default_warehouse=EXCLUDED.default_warehouse,warehouse_management_type=EXCLUDED.warehouse_management_type,
			raw_payload=EXCLUDED.raw_payload,synced_at=now()
`, item.ID, item.Name, item.RegionID, item.EnableBuyShippingLabel, item.Default, item.ManagementType, jsonOrEmpty(item.Raw, "{}")); err != nil {
			return err
		}
		if p.shopCode != "" {
			if _, err := tx.Exec(ctx, `
INSERT INTO public.temu_shop_warehouses(shop_code,logical_warehouse_key,warehouse_id,synced_at)
VALUES($1,$2,$3,now())
ON CONFLICT(shop_code,logical_warehouse_key) DO UPDATE SET
warehouse_id=EXCLUDED.warehouse_id,synced_at=now()
`, p.shopCode, logicalWarehouseKey(item), item.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func logicalWarehouseKey(item model.Warehouse) string {
	name := strings.ToUpper(strings.TrimSpace(item.Name + " " + item.ID))
	switch {
	case strings.Contains(name, "DPSNY002") || strings.Contains(name, "DPS002"):
		return "DPS002"
	case strings.Contains(name, "DPSCA004") || strings.Contains(name, "DPS004"):
		return "DPS004"
	case strings.Contains(name, "ARP") && (strings.Contains(name, "美东") || strings.Contains(name, "EAST")):
		return "ARP_EAST"
	case strings.Contains(name, "ARP") && (strings.Contains(name, "美西") || strings.Contains(name, "WEST")):
		return "ARP_WEST"
	case strings.Contains(name, "PG1955"):
		return "PG1955"
	case strings.Contains(name, "PA30"):
		return "PA30"
	default:
		return "TEMU_" + strings.ToUpper(strings.TrimSpace(item.ID))
	}
}

func (p *Postgres) ListWarehouses(ctx context.Context) ([]model.Warehouse, []model.WarehouseMapping, error) {
	rows, err := p.pool.Query(ctx, `
SELECT w.warehouse_id,w.warehouse_name,coalesce(w.region_id,0),w.enable_buy_shipping_label,
w.default_warehouse,coalesce(w.warehouse_management_type,0),w.synced_at,
coalesce(max(sw.logical_warehouse_key) FILTER (WHERE sw.shop_code=$1),''),
coalesce(array_agg(sw.shop_code ORDER BY sw.shop_code) FILTER (WHERE sw.shop_code IS NOT NULL),'{}'::text[])
FROM public.temu_warehouses w
LEFT JOIN public.temu_shop_warehouses sw ON sw.warehouse_id=w.warehouse_id
GROUP BY w.warehouse_id,w.warehouse_name,w.region_id,w.enable_buy_shipping_label,
w.default_warehouse,w.warehouse_management_type,w.synced_at
ORDER BY w.warehouse_name
`, p.shopCode)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	warehouses := []model.Warehouse{}
	for rows.Next() {
		var w model.Warehouse
		if err := rows.Scan(&w.ID, &w.Name, &w.RegionID, &w.EnableBuyShippingLabel, &w.Default, &w.ManagementType, &w.SyncedAt, &w.LogicalKey, &w.ShopCodes); err != nil {
			return nil, nil, err
		}
		warehouses = append(warehouses, w)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	mrows, err := p.pool.Query(ctx, `
SELECT m.oms_warehouse_key,coalesce(sw.warehouse_id,m.temu_warehouse_id),
coalesce(resolved.warehouse_name,canonical.warehouse_name,''),m.oms_warehouse_code,m.updated_at
FROM public.temu_warehouse_mappings m
LEFT JOIN public.temu_shop_warehouses sw
ON sw.shop_code=$1 AND sw.logical_warehouse_key=m.logical_warehouse_key
LEFT JOIN public.temu_warehouses resolved ON resolved.warehouse_id=sw.warehouse_id
LEFT JOIN public.temu_warehouses canonical ON canonical.warehouse_id=m.temu_warehouse_id
ORDER BY m.oms_warehouse_key
`, p.shopCode)
	if err != nil {
		return nil, nil, err
	}
	defer mrows.Close()
	mappings := []model.WarehouseMapping{}
	for mrows.Next() {
		var m model.WarehouseMapping
		if err := mrows.Scan(&m.OMSKey, &m.TemuWarehouseID, &m.TemuName, &m.OMSWarehouseCode, &m.UpdatedAt); err != nil {
			return nil, nil, err
		}
		mappings = append(mappings, m)
	}
	return warehouses, mappings, mrows.Err()
}

func (p *Postgres) SetWarehouseMapping(ctx context.Context, omsKey, temuID, omsWarehouseCode string) (model.WarehouseMapping, error) {
	omsKey = strings.ToUpper(strings.TrimSpace(omsKey))
	temuID = strings.TrimSpace(temuID)
	logicalKey := omsKey
	_ = p.pool.QueryRow(ctx, `
SELECT logical_warehouse_key FROM public.temu_shop_warehouses
WHERE warehouse_id=$1
ORDER BY (shop_code=$2) DESC
LIMIT 1
`, temuID, p.shopCode).Scan(&logicalKey)
	_, err := p.pool.Exec(ctx, `
INSERT INTO public.temu_warehouse_mappings(
oms_warehouse_key,temu_warehouse_id,oms_warehouse_code,logical_warehouse_key
) VALUES($1,$2,$3,$4)
ON CONFLICT(oms_warehouse_key) DO UPDATE SET
temu_warehouse_id=EXCLUDED.temu_warehouse_id,
oms_warehouse_code=EXCLUDED.oms_warehouse_code,
logical_warehouse_key=EXCLUDED.logical_warehouse_key,
updated_at=now()
`, omsKey, temuID, strings.TrimSpace(omsWarehouseCode), logicalKey)
	if err != nil {
		return model.WarehouseMapping{}, err
	}
	return p.WarehouseMapping(ctx, omsKey)
}

func (p *Postgres) DeleteWarehouseMapping(ctx context.Context, omsKey string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM public.temu_warehouse_mappings WHERE oms_warehouse_key=$1`, strings.ToUpper(strings.TrimSpace(omsKey)))
	return err
}

func (p *Postgres) WarehouseMapping(ctx context.Context, omsKey string) (model.WarehouseMapping, error) {
	var m model.WarehouseMapping
	err := p.pool.QueryRow(ctx, `
SELECT m.oms_warehouse_key,coalesce(sw.warehouse_id,m.temu_warehouse_id),
coalesce(resolved.warehouse_name,canonical.warehouse_name,''),m.oms_warehouse_code,m.updated_at
FROM public.temu_warehouse_mappings m
LEFT JOIN public.temu_shop_warehouses sw
ON sw.shop_code=$2 AND sw.logical_warehouse_key=m.logical_warehouse_key
LEFT JOIN public.temu_warehouses resolved ON resolved.warehouse_id=sw.warehouse_id
LEFT JOIN public.temu_warehouses canonical ON canonical.warehouse_id=m.temu_warehouse_id
WHERE m.oms_warehouse_key=$1
`, strings.ToUpper(strings.TrimSpace(omsKey)), p.shopCode).Scan(
		&m.OMSKey, &m.TemuWarehouseID, &m.TemuName,
		&m.OMSWarehouseCode, &m.UpdatedAt,
	)
	return m, err
}

func (p *Postgres) MappedWarehouse(ctx context.Context, omsKey string) (model.Warehouse, error) {
	var w model.Warehouse
	err := p.pool.QueryRow(ctx, `
SELECT w.warehouse_id,w.warehouse_name,coalesce(w.region_id,0),w.enable_buy_shipping_label,
w.default_warehouse,coalesce(w.warehouse_management_type,0),w.synced_at
FROM public.temu_warehouse_mappings m
LEFT JOIN public.temu_shop_warehouses sw
ON sw.shop_code=$2 AND sw.logical_warehouse_key=m.logical_warehouse_key
JOIN public.temu_warehouses w ON w.warehouse_id=coalesce(sw.warehouse_id,m.temu_warehouse_id)
WHERE m.oms_warehouse_key=$1
`, strings.ToUpper(strings.TrimSpace(omsKey)), p.shopCode).Scan(&w.ID, &w.Name, &w.RegionID, &w.EnableBuyShippingLabel, &w.Default, &w.ManagementType, &w.SyncedAt)
	return w, err
}

func (p *Postgres) ListCarrierPolicies(ctx context.Context) ([]model.CarrierPolicy, error) {
	rows, err := p.pool.Query(ctx, `
SELECT oms_warehouse_key,carrier_code,priority,enabled
FROM public.temu_carrier_policies
WHERE shop_code=$1
ORDER BY oms_warehouse_key,priority,carrier_code
`, p.shopCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.CarrierPolicy, 0)
	for rows.Next() {
		var item model.CarrierPolicy
		if err := rows.Scan(&item.WarehouseKey, &item.CarrierCode, &item.Priority, &item.Enabled); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (p *Postgres) ReplaceCarrierPolicies(ctx context.Context, warehouseKey string, policies []model.CarrierPolicy) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
DELETE FROM public.temu_carrier_policies
WHERE shop_code=$1 AND oms_warehouse_key=$2
`, p.shopCode, warehouseKey); err != nil {
		return err
	}
	for _, policy := range policies {
		if _, err := tx.Exec(ctx, `
INSERT INTO public.temu_carrier_policies(
shop_code,oms_warehouse_key,carrier_code,priority,enabled,updated_at
) VALUES($1,$2,$3,$4,$5,now())
`, p.shopCode, warehouseKey, policy.CarrierCode, policy.Priority, policy.Enabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *Postgres) SaveQuote(ctx context.Context, quote model.Quote) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO temu_shipping_quotes(id,parent_order_sn,oms_warehouse_key,temu_warehouse_id,region,selected_channel_id,selected_ship_company_id,selected_company_name,selected_logistics_type,selected_reason,request_payload,response_payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, quote.ID, quote.ParentOrderSN, quote.OMSWarehouseKey, quote.TemuWarehouseID, quote.Region, quote.ChannelID, quote.ShipCompanyID, quote.ShippingCompanyName, quote.ShipLogisticsType, quote.SelectionReason, jsonOrEmpty(quote.RequestPayload, "{}"), jsonOrEmpty(quote.ResponsePayload, "{}"), quote.ExpiresAt)
	return err
}

func (p *Postgres) GetQuote(ctx context.Context, id string) (model.Quote, error) {
	var q model.Quote
	err := p.pool.QueryRow(ctx, `SELECT id,parent_order_sn,oms_warehouse_key,temu_warehouse_id,region,selected_channel_id,selected_ship_company_id,selected_company_name,selected_logistics_type,selected_reason,request_payload,response_payload,expires_at,created_at FROM temu_shipping_quotes WHERE id=$1`, id).Scan(&q.ID, &q.ParentOrderSN, &q.OMSWarehouseKey, &q.TemuWarehouseID, &q.Region, &q.ChannelID, &q.ShipCompanyID, &q.ShippingCompanyName, &q.ShipLogisticsType, &q.SelectionReason, &q.RequestPayload, &q.ResponsePayload, &q.ExpiresAt, &q.CreatedAt)
	return q, err
}

func recordLabelPurchaseChoice(ctx context.Context, tx pgx.Tx, quoteID, shipmentID, parentOrderSN string, choice model.LabelPurchaseChoice) error {
	if choice.Selected.ChannelID == 0 && len(choice.TopCandidates) == 0 {
		return nil
	}
	if choice.SelectionSource != "automatic" && choice.SelectionSource != "manual" {
		return errors.New("stored quote choice analysis has an invalid selection source")
	}
	if choice.Selected.ChannelID == 0 || choice.Selected.OMSWarehouseKey == "" || choice.Selected.TemuWarehouseID == "" {
		return errors.New("stored quote choice analysis is missing the selected channel")
	}
	if len(choice.TopCandidates) == 0 || len(choice.TopCandidates) > 3 {
		return errors.New("stored quote choice analysis must contain one to three candidates")
	}
	if err := validateLabelPurchaseAmount(choice.Selected.EstimatedAmount); err != nil {
		return fmt.Errorf("invalid selected shipping amount: %w", err)
	}
	var selectedRank any
	if choice.Selected.PriceRank > 0 {
		if choice.Selected.PriceRank > len(choice.TopCandidates) || !sameLabelPurchaseCandidate(choice.Selected, choice.TopCandidates[choice.Selected.PriceRank-1]) {
			return errors.New("stored quote choice analysis has an invalid selected price rank")
		}
		selectedRank = choice.Selected.PriceRank
	}
	_, err := tx.Exec(ctx, `
INSERT INTO temu_label_purchase_choices(
    quote_id,shipment_id,parent_order_sn,selection_source,selected_price_rank,
    selected_oms_warehouse_key,selected_temu_warehouse_id,selected_channel_id,
    selected_ship_company_id,selected_carrier_code,selected_company_name,
    selected_logistics_type,selected_estimated_amount,selected_currency_code,selection_reason
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::numeric,$14,$15)
ON CONFLICT(quote_id) DO NOTHING
`, quoteID, shipmentID, parentOrderSN, choice.SelectionSource, selectedRank,
		choice.Selected.OMSWarehouseKey, choice.Selected.TemuWarehouseID, choice.Selected.ChannelID,
		choice.Selected.ShipCompanyID, choice.Selected.CarrierCode, choice.Selected.ShippingCompanyName,
		choice.Selected.ShipLogisticsType, choice.Selected.EstimatedAmount,
		choice.Selected.EstimatedCurrencyCode, choice.SelectionReason)
	if err != nil {
		return err
	}
	for index, candidate := range choice.TopCandidates {
		if candidate.PriceRank != index+1 || candidate.ChannelID == 0 || candidate.OMSWarehouseKey == "" || candidate.TemuWarehouseID == "" {
			return errors.New("stored quote choice analysis has an invalid Top 3 candidate")
		}
		if err := validateLabelPurchaseAmount(candidate.EstimatedAmount); err != nil {
			return fmt.Errorf("invalid candidate shipping amount at rank %d: %w", candidate.PriceRank, err)
		}
		_, err = tx.Exec(ctx, `
INSERT INTO temu_label_purchase_candidates(
    quote_id,price_rank,oms_warehouse_key,temu_warehouse_id,channel_id,ship_company_id,
    carrier_code,shipping_company_name,ship_logistics_type,estimated_amount,currency_code,is_selected
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,$12)
ON CONFLICT(quote_id,price_rank) DO NOTHING
`, quoteID, candidate.PriceRank, candidate.OMSWarehouseKey, candidate.TemuWarehouseID,
			candidate.ChannelID, candidate.ShipCompanyID, candidate.CarrierCode,
			candidate.ShippingCompanyName, candidate.ShipLogisticsType, candidate.EstimatedAmount,
			candidate.EstimatedCurrencyCode, choice.Selected.PriceRank == candidate.PriceRank)
		if err != nil {
			return err
		}
	}
	return nil
}

func validateLabelPurchaseAmount(raw string) error {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return errors.New("amount must be a finite non-negative number")
	}
	return nil
}

func sameLabelPurchaseCandidate(left, right model.LabelPurchaseCandidate) bool {
	return left.OMSWarehouseKey == right.OMSWarehouseKey &&
		left.ChannelID == right.ChannelID && left.ShipCompanyID == right.ShipCompanyID
}

func (p *Postgres) ReserveShipment(ctx context.Context, shipment model.Shipment, choice model.LabelPurchaseChoice) (model.Shipment, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.Shipment{}, false, err
	}
	defer tx.Rollback(ctx)
	existing, err := shipmentForOrder(ctx, tx, shipment.ParentOrderSN)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.Shipment{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO temu_shipments(id,quote_id,idempotency_key,status,selection_mode,warehouse_id,channel_id,ship_company_id,shipping_company_name,ship_logistics_type,request_payload,submission_attempts,last_submission_at) VALUES($1,$2,$3,'submitting',$4,$5,$6,$7,$8,$9,$10,1,now())`, shipment.ID, shipment.QuoteID, shipment.IdempotencyKey, shipment.SelectionMode, shipment.WarehouseID, shipment.ChannelID, shipment.ShipCompanyID, shipment.ShippingCompanyName, shipment.ShipLogisticsType, jsonOrEmpty(shipment.RequestPayload, "{}"))
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO temu_shipment_orders(parent_order_sn,shipment_id) VALUES($1,$2)`, shipment.ParentOrderSN, shipment.ID)
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(ctx)
			existing, lookupErr := p.ShipmentForOrder(ctx, shipment.ParentOrderSN)
			return existing, true, lookupErr
		}
		return model.Shipment{}, false, err
	}
	if err = recordLabelPurchaseChoice(ctx, tx, shipment.QuoteID, shipment.ID, shipment.ParentOrderSN, choice); err != nil {
		return model.Shipment{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type) VALUES($1,'submission_reserved')`, shipment.ID); err != nil {
		return model.Shipment{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Shipment{}, false, err
	}
	created, err := p.GetShipment(ctx, shipment.ID)
	return created, false, err
}

func (p *Postgres) PrepareShipmentRetry(ctx context.Context, id string, replacement model.Shipment, choice model.LabelPurchaseChoice) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE temu_shipments SET quote_id=$2,status='submitting',selection_mode=$3,warehouse_id=$4,channel_id=$5,ship_company_id=$6,shipping_company_name=$7,ship_logistics_type=$8,package_sn_list='{}',tracking_number='',request_payload=$9,response_payload=NULL,error_code='',error_message='',submission_attempts=submission_attempts+1,last_submission_at=now(),updated_at=now(),confirmed_at=NULL WHERE id=$1 AND status IN ('submission_unknown','label_failed') AND cardinality(package_sn_list)=0`, id, replacement.QuoteID, replacement.SelectionMode, replacement.WarehouseID, replacement.ChannelID, replacement.ShipCompanyID, replacement.ShippingCompanyName, replacement.ShipLogisticsType, jsonOrEmpty(replacement.RequestPayload, "{}"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err := recordLabelPurchaseChoice(ctx, tx, replacement.QuoteID, id, replacement.ParentOrderSN, choice); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"quote_id": replacement.QuoteID})
	if _, err := tx.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type,payload) VALUES($1,'submission_retry_reserved',$2)`, id, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) PrepareFailedShipmentRecovery(ctx context.Context, id string, replacement model.Shipment, choice model.LabelPurchaseChoice, failedCarrierCode string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status, tracking, companyName, logisticsType, errorCode, errorMessage string
	var packageSNs, failedCarrierCodes []string
	var confirmedAt *time.Time
	err = tx.QueryRow(ctx, `
SELECT status,tracking_number,shipping_company_name,ship_logistics_type,
       package_sn_list,failed_carrier_codes,error_code,error_message,confirmed_at
FROM temu_shipments WHERE id=$1 FOR UPDATE
`, id).Scan(&status, &tracking, &companyName, &logisticsType, &packageSNs,
		&failedCarrierCodes, &errorCode, &errorMessage, &confirmedAt)
	if err != nil {
		return err
	}
	if status != "label_failed" || strings.TrimSpace(tracking) != "" || confirmedAt != nil {
		return pgx.ErrNoRows
	}
	failedCarrierCode = strings.ToUpper(strings.TrimSpace(failedCarrierCode))
	if failedCarrierCode != "" && !slices.Contains(failedCarrierCodes, failedCarrierCode) {
		failedCarrierCodes = append(failedCarrierCodes, failedCarrierCode)
	}
	payload, _ := json.Marshal(map[string]any{
		"source":                  "operator_recovery",
		"replacement_quote_id":    replacement.QuoteID,
		"previous_package_sns":    packageSNs,
		"previous_company_name":   companyName,
		"previous_logistics_type": logisticsType,
		"previous_error_code":     errorCode,
		"previous_error_message":  errorMessage,
	})
	tag, err := tx.Exec(ctx, `
UPDATE temu_shipments SET
    quote_id=$2,status='submitting',selection_mode=$3,warehouse_id=$4,channel_id=$5,
    ship_company_id=$6,shipping_company_name=$7,ship_logistics_type=$8,
    failed_carrier_codes=$9,package_sn_list='{}',tracking_number='',request_payload=$10,
    response_payload=NULL,error_code='',error_message='',submission_attempts=submission_attempts+1,
    last_submission_at=now(),updated_at=now(),confirmed_at=NULL
WHERE id=$1 AND status='label_failed' AND tracking_number='' AND confirmed_at IS NULL
`, id, replacement.QuoteID, replacement.SelectionMode, replacement.WarehouseID,
		replacement.ChannelID, replacement.ShipCompanyID, replacement.ShippingCompanyName,
		replacement.ShipLogisticsType, failedCarrierCodes, jsonOrEmpty(replacement.RequestPayload, "{}"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err := recordLabelPurchaseChoice(ctx, tx, replacement.QuoteID, id, replacement.ParentOrderSN, choice); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type,payload) VALUES($1,'operator_recovery_reserved',$2)`, id, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) PrepareUnknownShipmentRetry(ctx context.Context, id string, maxAttempts int, retryBefore time.Time) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var attempt int
	err = tx.QueryRow(ctx, `
		UPDATE temu_shipments SET
			status='submitting',response_payload=NULL,error_code='',error_message='',
			submission_attempts=submission_attempts+1,last_submission_at=now(),updated_at=now()
		WHERE id=$1 AND status='submission_unknown' AND cardinality(package_sn_list)=0
		  AND submission_attempts < $2 AND last_submission_at <= $3
		RETURNING submission_attempts
	`, id, maxAttempts, retryBefore).Scan(&attempt)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"attempt": attempt, "source": "automatic_recovery"})
	if _, err := tx.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type,payload) VALUES($1,'submission_retry_reserved',$2)`, id, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) RecordShipmentConfirmationAttempt(ctx context.Context, id string) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var attempt int
	err = tx.QueryRow(ctx, `
		UPDATE temu_shipments SET
			confirmation_attempts=confirmation_attempts+1,last_confirmation_at=now(),updated_at=now()
		WHERE id=$1 AND status IN ('label_ready','confirm_failed')
		RETURNING confirmation_attempts
	`, id).Scan(&attempt)
	if err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]int{"attempt": attempt})
	if _, err := tx.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type,payload) VALUES($1,'confirmation_attempted',$2)`, id, payload); err != nil {
		return 0, err
	}
	return attempt, tx.Commit(ctx)
}

func (p *Postgres) ShipmentForOrder(ctx context.Context, parent string) (model.Shipment, error) {
	return shipmentForOrder(ctx, p.pool, parent)
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func shipmentForOrder(ctx context.Context, q rowQuerier, parent string) (model.Shipment, error) {
	return scanShipment(q.QueryRow(ctx, shipmentSelect+` JOIN temu_shipment_orders so ON so.shipment_id=s.id WHERE so.parent_order_sn=$1`, parent))
}

func (p *Postgres) GetShipment(ctx context.Context, id string) (model.Shipment, error) {
	return scanShipment(p.pool.QueryRow(ctx, shipmentSelect+` JOIN temu_shipment_orders so ON so.shipment_id=s.id WHERE s.id=$1`, id))
}

const shipmentSelect = `SELECT
	s.id,s.quote_id,s.idempotency_key,s.status,s.selection_mode,s.warehouse_id,
s.channel_id,s.ship_company_id,s.shipping_company_name,s.ship_logistics_type,
s.failed_carrier_codes,s.package_sn_list,s.tracking_number,s.request_payload,
	coalesce(s.response_payload,'{}'::jsonb),s.error_code,s.error_message,
	s.submission_attempts,s.last_submission_at,
	s.confirmation_attempts,s.last_confirmation_at,
	s.created_at,s.updated_at,s.confirmed_at,so.parent_order_sn,
	q.oms_warehouse_key,coalesce(m.oms_warehouse_code,''),
	d.shipment_id,d.oms_warehouse_key,d.warehouse_code,d.status,
	coalesce(d.outbound_order_nos,'{}'::text[]),coalesce(d.tracking_number,''),
	coalesce(d.attempts,0),coalesce(d.error_message,''),
	coalesce(d.response_summary,'{}'::jsonb),d.created_at,d.updated_at,d.verified_at
	FROM temu_shipments s
	JOIN temu_shipping_quotes q ON q.id=s.quote_id
LEFT JOIN public.temu_warehouse_mappings m ON m.oms_warehouse_key=q.oms_warehouse_key
	LEFT JOIN temu_oms_sync_checks d ON d.shipment_id=s.id`

func scanShipment(row pgx.Row) (model.Shipment, error) {
	var s model.Shipment
	var syncID, syncKey, syncCode, syncStatus *string
	var syncOrders []string
	var syncTracking string
	var syncAttempts int
	var syncError string
	var syncSummary json.RawMessage
	var syncCreated, syncUpdated, syncVerified *time.Time
	err := row.Scan(
		&s.ID, &s.QuoteID, &s.IdempotencyKey, &s.Status, &s.SelectionMode,
		&s.WarehouseID, &s.ChannelID, &s.ShipCompanyID, &s.ShippingCompanyName,
		&s.ShipLogisticsType, &s.FailedCarrierCodes, &s.PackageSNList, &s.TrackingNumber, &s.RequestPayload,
		&s.ResponsePayload, &s.ErrorCode, &s.ErrorMessage, &s.SubmissionAttempts, &s.LastSubmissionAt, &s.ConfirmationAttempts, &s.LastConfirmationAt, &s.CreatedAt, &s.UpdatedAt,
		&s.ConfirmedAt, &s.ParentOrderSN, &s.OMSWarehouseKey, &s.OMSWarehouseCode,
		&syncID, &syncKey, &syncCode, &syncStatus, &syncOrders, &syncTracking,
		&syncAttempts, &syncError, &syncSummary, &syncCreated,
		&syncUpdated, &syncVerified,
	)
	if err == nil && syncID != nil {
		s.OMSSync = &model.OMSSync{
			ShipmentID: *syncID, OMSWarehouseKey: value(syncKey),
			WarehouseCode: value(syncCode),
			Status:        value(syncStatus), OutboundOrderNos: syncOrders,
			TrackingNumber: syncTracking, Attempts: syncAttempts,
			ErrorMessage: syncError, Summary: syncSummary,
			VerifiedAt: syncVerified,
		}
		if syncCreated != nil {
			s.OMSSync.CreatedAt = *syncCreated
		}
		if syncUpdated != nil {
			s.OMSSync.UpdatedAt = *syncUpdated
		}
	}
	return s, err
}

func (p *Postgres) ListShipments(ctx context.Context, queue string, page, pageSize int) ([]model.Shipment, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	where := ""
	switch strings.ToLower(strings.TrimSpace(queue)) {
	case "", "all":
	case "processing":
		where = ` WHERE s.status IN ('submitting','label_pending','label_ready')`
	case "exceptions":
		where = ` WHERE s.status IN ('submission_unknown','label_failed','confirm_failed')`
	default:
		return nil, 0, errors.New("invalid shipment queue")
	}
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM temu_shipments s`+where).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.pool.Query(ctx, shipmentSelect+` JOIN temu_shipment_orders so ON so.shipment_id=s.id`+where+` ORDER BY s.created_at DESC LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []model.Shipment{}
	for rows.Next() {
		s, err := scanShipment(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, s)
	}
	return items, total, rows.Err()
}

func (p *Postgres) ShipmentStatusCounts(ctx context.Context) (map[string]int, error) {
	rows, err := p.pool.Query(ctx, `SELECT status,count(*) FROM temu_shipments GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (p *Postgres) ListShipmentPOGroups(ctx context.Context, from, before *time.Time) ([]model.ShipmentPOGroup, error) {
	rows, err := p.pool.Query(ctx, `
SELECT coalesce(nullif(trim(q.oms_warehouse_key),''),'UNMAPPED'),so.parent_order_sn
FROM temu_shipment_orders so
JOIN temu_shipments s ON s.id=so.shipment_id
JOIN temu_shipping_quotes q ON q.id=s.quote_id
WHERE ($1::timestamptz IS NULL OR s.created_at >= $1)
  AND ($2::timestamptz IS NULL OR s.created_at < $2)
ORDER BY 1,s.created_at,so.parent_order_sn
`, from, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.ShipmentPOGroup, 0)
	indexes := make(map[string]int)
	for rows.Next() {
		var warehouseKey, parentOrderSN string
		if err := rows.Scan(&warehouseKey, &parentOrderSN); err != nil {
			return nil, err
		}
		if parentOrderSN = strings.TrimSpace(parentOrderSN); parentOrderSN != "" {
			index, ok := indexes[warehouseKey]
			if !ok {
				index = len(items)
				indexes[warehouseKey] = index
				items = append(items, model.ShipmentPOGroup{WarehouseKey: warehouseKey, PONumbers: []string{}})
			}
			items[index].PONumbers = append(items[index].PONumbers, parentOrderSN)
		}
	}
	return items, rows.Err()
}

func (p *Postgres) UpdateShipmentSubmission(ctx context.Context, id, status string, packageSNs []string, raw json.RawMessage, code, message string) error {
	if packageSNs == nil {
		packageSNs = []string{}
	}
	_, err := p.pool.Exec(ctx, `UPDATE temu_shipments SET status=$2,package_sn_list=$3,response_payload=$4,error_code=$5,error_message=$6,updated_at=now() WHERE id=$1`, id, status, packageSNs, jsonOrEmpty(raw, "{}"), code, message)
	if err == nil {
		_, err = p.pool.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type,payload) VALUES($1,$2,$3)`, id, status, jsonOrEmpty(raw, "{}"))
	}
	return err
}

func (p *Postgres) RecordShipmentCarrierFailure(ctx context.Context, id, carrierCode string) error {
	carrierCode = strings.ToUpper(strings.TrimSpace(carrierCode))
	if carrierCode == "" {
		return nil
	}
	_, err := p.pool.Exec(ctx, `
UPDATE temu_shipments SET
failed_carrier_codes=CASE
WHEN $2=ANY(failed_carrier_codes) THEN failed_carrier_codes
ELSE array_append(failed_carrier_codes,$2)
END,
updated_at=now()
WHERE id=$1
`, id, carrierCode)
	return err
}

func (p *Postgres) UpdateShipmentResult(ctx context.Context, id, status, tracking string, raw json.RawMessage, code, message string) error {
	_, err := p.pool.Exec(ctx, `UPDATE temu_shipments SET status=$2,tracking_number=CASE WHEN $3='' THEN tracking_number ELSE $3 END,response_payload=$4,error_code=$5,error_message=$6,updated_at=now() WHERE id=$1`, id, status, tracking, jsonOrEmpty(raw, "{}"), code, message)
	if err == nil {
		_, err = p.pool.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type,payload) VALUES($1,$2,$3)`, id, status, jsonOrEmpty(raw, "{}"))
	}
	return err
}

func (p *Postgres) ReconcileShipmentResultCarrier(ctx context.Context, id string, channelID, shipCompanyID int64, companyName, logisticsType string) error {
	if strings.TrimSpace(companyName) == "" {
		return nil
	}
	_, err := p.pool.Exec(ctx, `
UPDATE temu_shipments SET
    channel_id=CASE WHEN $2::bigint=0 THEN channel_id ELSE $2::bigint END,
    ship_company_id=CASE WHEN $3::bigint=0 THEN ship_company_id ELSE $3::bigint END,
    shipping_company_name=$4,
    ship_logistics_type=CASE WHEN $5='' THEN ship_logistics_type ELSE $5 END,
    updated_at=now()
WHERE id=$1
`, id, channelID, shipCompanyID, companyName, logisticsType)
	return err
}

func (p *Postgres) MarkShipmentConfirmed(ctx context.Context, id string, raw json.RawMessage) error {
	_, err := p.pool.Exec(ctx, `UPDATE temu_shipments SET status='shipped',response_payload=$2,error_code='',error_message='',updated_at=now(),confirmed_at=now() WHERE id=$1`, id, jsonOrEmpty(raw, "{}"))
	if err == nil {
		_, err = p.pool.Exec(ctx, `INSERT INTO temu_shipment_events(shipment_id,event_type,payload) VALUES($1,'shipped',$2)`, id, jsonOrEmpty(raw, "{}"))
	}
	return err
}

func (p *Postgres) StartOMSSync(ctx context.Context, shipmentID string, mapping model.WarehouseMapping, tracking string) (model.OMSSync, bool, error) {
	row := p.pool.QueryRow(ctx, `
		INSERT INTO temu_oms_sync_checks(
			shipment_id,oms_warehouse_key,warehouse_code,status,tracking_number,attempts
		) VALUES($1,$2,$3,'querying',$4,1)
		ON CONFLICT(shipment_id) DO UPDATE SET
			oms_warehouse_key=EXCLUDED.oms_warehouse_key,
			warehouse_code=EXCLUDED.warehouse_code,status='querying',
			tracking_number=EXCLUDED.tracking_number,
			attempts=temu_oms_sync_checks.attempts+1,
			error_message='',updated_at=now()
		WHERE temu_oms_sync_checks.status <> 'verified'
		  AND (temu_oms_sync_checks.status <> 'querying'
		       OR temu_oms_sync_checks.updated_at < now()-interval '2 minutes')
		RETURNING shipment_id,oms_warehouse_key,warehouse_code,status,
			outbound_order_nos,tracking_number,attempts,error_message,response_summary,
			created_at,updated_at,verified_at
	`, shipmentID, mapping.OMSKey, mapping.OMSWarehouseCode, tracking)
	item, err := scanOMSSync(row)
	if err == nil {
		payload, _ := json.Marshal(map[string]any{"warehouse_code": mapping.OMSWarehouseCode})
		_, eventErr := p.pool.Exec(ctx, `
			INSERT INTO temu_shipment_events(shipment_id,event_type,payload)
			VALUES($1,'oms_sync_started',$2)
		`, shipmentID, payload)
		return item, true, eventErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.OMSSync{}, false, err
	}
	current, currentErr := p.GetOMSSync(ctx, shipmentID)
	return current, false, currentErr
}

func (p *Postgres) GetOMSSync(ctx context.Context, shipmentID string) (model.OMSSync, error) {
	return scanOMSSync(p.pool.QueryRow(ctx, `
		SELECT shipment_id,oms_warehouse_key,warehouse_code,status,
			outbound_order_nos,tracking_number,attempts,error_message,response_summary,
			created_at,updated_at,verified_at
		FROM temu_oms_sync_checks WHERE shipment_id=$1
	`, shipmentID))
}

func scanOMSSync(row pgx.Row) (model.OMSSync, error) {
	var item model.OMSSync
	err := row.Scan(
		&item.ShipmentID, &item.OMSWarehouseKey, &item.WarehouseCode,
		&item.Status, &item.OutboundOrderNos, &item.TrackingNumber,
		&item.Attempts, &item.ErrorMessage, &item.Summary,
		&item.CreatedAt, &item.UpdatedAt, &item.VerifiedAt,
	)
	return item, err
}

func (p *Postgres) UpdateOMSSync(
	ctx context.Context, shipmentID, status string, outboundOrderNos []string,
	tracking string, summary json.RawMessage, message string,
) error {
	if outboundOrderNos == nil {
		outboundOrderNos = []string{}
	}
	if len(summary) == 0 || !json.Valid(summary) {
		summary = json.RawMessage("{}")
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE temu_oms_sync_checks SET
			status=$2,outbound_order_nos=$3,tracking_number=$4,
			response_summary=$5,error_message=$6,updated_at=now(),
			verified_at=CASE WHEN $2='verified' THEN now() ELSE verified_at END
		WHERE shipment_id=$1
	`, shipmentID, status, outboundOrderNos, tracking, summary, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	eventPayload, _ := json.Marshal(map[string]any{
		"status": status, "outbound_order_nos": outboundOrderNos,
		"tracking_number": tracking, "message": message,
	})
	_, err = p.pool.Exec(ctx, `
		INSERT INTO temu_shipment_events(shipment_id,event_type,payload)
		VALUES($1,$2,$3)
	`, shipmentID, "oms_sync_"+status, eventPayload)
	return err
}

func (p *Postgres) PendingOMSSyncShipmentIDs(ctx context.Context, retryBefore time.Time, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := p.pool.Query(ctx, `
		SELECT s.id
		FROM temu_shipments s
		JOIN temu_shipping_quotes q ON q.id=s.quote_id
JOIN public.temu_warehouse_mappings m ON m.oms_warehouse_key=q.oms_warehouse_key
		LEFT JOIN temu_oms_sync_checks d ON d.shipment_id=s.id
		WHERE s.status='shipped'
		  AND s.tracking_number <> ''
		  AND cardinality(s.package_sn_list) > 0
		  AND m.oms_warehouse_code <> ''
		  AND (d.shipment_id IS NULL OR (d.status <> 'verified' AND d.updated_at < $1))
		ORDER BY s.confirmed_at, s.created_at
		LIMIT $2
	`, retryBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (p *Postgres) EnqueueAutoFulfillment(ctx context.Context, parentOrderSN string) (model.AutoFulfillmentJob, error) {
	row := p.pool.QueryRow(ctx, `
		INSERT INTO temu_auto_fulfillment_jobs(parent_order_sn,status)
		VALUES($1,'queued')
		ON CONFLICT(parent_order_sn) DO UPDATE SET
			status='queued',last_error='',updated_at=now(),completed_at=NULL
		WHERE temu_auto_fulfillment_jobs.status='failed'
		RETURNING parent_order_sn,coalesce(shipment_id,''),status,attempts,last_error,
			created_at,updated_at,started_at,completed_at
	`, parentOrderSN)
	job, err := scanAutoFulfillmentJob(row)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return job, err
	}
	return p.GetAutoFulfillment(ctx, parentOrderSN)
}

func (p *Postgres) GetAutoFulfillment(ctx context.Context, parentOrderSN string) (model.AutoFulfillmentJob, error) {
	return scanAutoFulfillmentJob(p.pool.QueryRow(ctx, `
		SELECT parent_order_sn,coalesce(shipment_id,''),status,attempts,last_error,
			created_at,updated_at,started_at,completed_at
		FROM temu_auto_fulfillment_jobs WHERE parent_order_sn=$1
	`, parentOrderSN))
}

const autoFulfillmentClaimOrder = `CASE j.status
  WHEN 'confirming' THEN 0
  WHEN 'waiting_label' THEN 1
  WHEN 'running' THEN 2
  WHEN 'failed' THEN 3
  WHEN 'queued' THEN 4
  WHEN 'waiting_oms' THEN 5
  ELSE 6
END,
CASE WHEN j.status IN ('confirming','waiting_label','running') THEN j.updated_at END ASC NULLS LAST`

const autoFulfillmentRateLimitMarker = "%code=4000004%"
const autoFulfillmentRateLimitBackoff = time.Minute

func (p *Postgres) ClaimAutoFulfillments(ctx context.Context, retryBefore time.Time, limit int) ([]model.AutoFulfillmentJob, error) {
	if limit < 1 || limit > 50 {
		limit = 4
	}
	rows, err := p.pool.Query(ctx, `
		WITH picked AS (
			SELECT j.parent_order_sn
			FROM temu_auto_fulfillment_jobs j
			JOIN temu_orders o ON o.parent_order_sn=j.parent_order_sn
WHERE (j.status='queued' AND (j.last_error NOT LIKE $3 OR j.updated_at < $4))
   OR (j.status='waiting_oms' AND j.updated_at < now()-interval '2 minutes')
   OR (j.status IN ('waiting_label','confirming') AND j.updated_at < CASE WHEN j.last_error LIKE $3 THEN $4 ELSE $1 END)
   OR (j.status='running' AND j.updated_at < now()-interval '5 minutes')
   OR (j.status='failed' AND j.last_error LIKE $3 AND j.updated_at < $4 AND (
       j.shipment_id IS NULL OR EXISTS (
SELECT 1 FROM temu_shipments retry_shipment
WHERE retry_shipment.id=j.shipment_id
  AND retry_shipment.status IN ('submitting','label_pending','label_ready','confirm_failed','submission_unknown')
       )
   ))
   OR (j.status='failed' AND j.updated_at < now()-interval '20 seconds' AND EXISTS (
SELECT 1 FROM temu_shipments failed_shipment
WHERE failed_shipment.id=j.shipment_id
  AND failed_shipment.status='label_failed'
  AND failed_shipment.tracking_number=''
  AND failed_shipment.confirmed_at IS NULL
  AND cardinality(failed_shipment.package_sn_list)=0
  AND failed_shipment.submission_attempts < 3
  AND lower(failed_shipment.error_message) LIKE '%delivery address is not supported%'
   ))
ORDER BY `+autoFulfillmentClaimOrder+`,
EXISTS (
SELECT 1 FROM temu_bulk_fulfillment_items bulk_item
JOIN temu_bulk_fulfillment_batches bulk_batch ON bulk_batch.id=bulk_item.batch_id
WHERE bulk_item.parent_order_sn=j.parent_order_sn
  AND bulk_batch.status='running' AND bulk_item.status IN ('pending','running')
) DESC,o.expect_ship_latest_time NULLS LAST,o.update_time,j.created_at
			FOR UPDATE OF j SKIP LOCKED
			LIMIT $2
		)
		UPDATE temu_auto_fulfillment_jobs j SET
			status='running',attempts=j.attempts+1,updated_at=now(),
			started_at=coalesce(j.started_at,now())
		FROM picked WHERE j.parent_order_sn=picked.parent_order_sn
		RETURNING j.parent_order_sn,coalesce(j.shipment_id,''),j.status,j.attempts,j.last_error,
			j.created_at,j.updated_at,j.started_at,j.completed_at
	`, retryBefore, limit, autoFulfillmentRateLimitMarker, time.Now().Add(-autoFulfillmentRateLimitBackoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.AutoFulfillmentJob, 0, limit)
	for rows.Next() {
		item, scanErr := scanAutoFulfillmentJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (p *Postgres) UpdateAutoFulfillment(ctx context.Context, parentOrderSN, shipmentID, status, lastError string) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE temu_auto_fulfillment_jobs SET
			shipment_id=coalesce(nullif($2,''),shipment_id),status=$3,last_error=$4,
			updated_at=now(),completed_at=CASE WHEN $3='completed' THEN now() ELSE NULL END
		WHERE parent_order_sn=$1
	`, parentOrderSN, shipmentID, status, lastError)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func scanAutoFulfillmentJob(row pgx.Row) (model.AutoFulfillmentJob, error) {
	var item model.AutoFulfillmentJob
	err := row.Scan(&item.ParentOrderSN, &item.ShipmentID, &item.Status, &item.Attempts,
		&item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.StartedAt, &item.CompletedAt)
	return item, err
}

func jsonOrEmpty(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(fallback)
	}
	return raw
}
func intValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
