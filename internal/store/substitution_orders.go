package store

import (
	"context"
	"fmt"
	"strings"
)

func (p *Postgres) ListSubstitutionOrderSNs(ctx context.Context, warehouseSKUs []string, query string, page, pageSize int) ([]string, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	seen := make(map[string]bool, len(warehouseSKUs))
	targets := make([]string, 0, len(warehouseSKUs))
	for _, sku := range warehouseSKUs {
		sku = strings.TrimSpace(sku)
		if sku != "" && !seen[sku] {
			seen[sku] = true
			targets = append(targets, sku)
		}
	}
	if len(targets) == 0 {
		return []string{}, 0, nil
	}
	query = strings.TrimSpace(query)
	where := `WHERE o.is_open AND o.parent_order_status=2
		AND NOT EXISTS (SELECT 1 FROM temu_shipment_orders shipped WHERE shipped.parent_order_sn=o.parent_order_sn)
		AND EXISTS (SELECT 1 FROM temu_order_lines candidate WHERE candidate.parent_order_sn=o.parent_order_sn AND btrim(candidate.ext_code)=ANY($1::text[]))
		AND ($2='' OR o.parent_order_sn ILIKE '%' || $2 || '%' OR EXISTS (
			SELECT 1 FROM temu_order_lines searched WHERE searched.parent_order_sn=o.parent_order_sn
			AND (searched.order_sn ILIKE '%' || $2 || '%' OR searched.ext_code ILIKE '%' || $2 || '%')))`
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM temu_orders o `+where, targets, query).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count substitution orders: %w", err)
	}
	rows, err := p.pool.Query(ctx, `SELECT o.parent_order_sn FROM temu_orders o `+where+`
		ORDER BY o.expect_ship_latest_time NULLS LAST,o.update_time DESC LIMIT $3 OFFSET $4`, targets, query, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list substitution orders: %w", err)
	}
	defer rows.Close()
	items := make([]string, 0, pageSize)
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			return nil, 0, err
		}
		items = append(items, parent)
	}
	return items, total, rows.Err()
}
