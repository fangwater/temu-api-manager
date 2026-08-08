package store

import (
	"context"

	"temu-api-manager/internal/model"
)

func (p *Postgres) FulfillmentAuditShipments(ctx context.Context, parentOrderSNs []string) (map[string]model.FulfillmentAuditShipment, error) {
	result := make(map[string]model.FulfillmentAuditShipment)
	if len(parentOrderSNs) == 0 {
		return result, nil
	}
	rows, err := p.pool.Query(ctx, `
SELECT so.parent_order_sn,q.oms_warehouse_key,coalesce(m.oms_warehouse_code,''),
       s.tracking_number,s.confirmed_at
FROM temu_shipment_orders so
JOIN temu_shipments s ON s.id=so.shipment_id
JOIN temu_shipping_quotes q ON q.id=s.quote_id
LEFT JOIN public.temu_warehouse_mappings m ON m.oms_warehouse_key=q.oms_warehouse_key
WHERE so.parent_order_sn=ANY($1)
`, parentOrderSNs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item model.FulfillmentAuditShipment
		if err := rows.Scan(&item.ParentOrderSN, &item.WarehouseKey, &item.WarehouseCode, &item.TrackingNumber, &item.ConfirmedAt); err != nil {
			return nil, err
		}
		result[item.ParentOrderSN] = item
	}
	return result, rows.Err()
}
