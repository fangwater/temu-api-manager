package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strings"
	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
)

func (p *Postgres) SetWarehouseBindingSource(source interface {
	WarehouseBindings(context.Context, string, string) ([]inventory.WarehouseBinding, error)
}) { p.warehouseBindings = source }
func (p *Postgres) ListWarehouses(ctx context.Context) ([]model.Warehouse, []model.WarehouseMapping, error) {
	warehouses, legacy, err := p.LegacyListWarehouses(ctx)
	if err != nil {
		return nil, nil, err
	}
	if p.warehouseBindings == nil {
		return warehouses, legacy, nil
	}
	bindings, err := p.warehouseBindings.WarehouseBindings(ctx, "temu", p.shopCode)
	if err != nil {
		return nil, nil, err
	}
	mappings := []model.WarehouseMapping{}
	for _, b := range bindings {
		mappings = append(mappings, model.WarehouseMapping{OMSKey: b.WarehouseKey, OMSWarehouseCode: b.OMSCode, TemuWarehouseID: b.PlatformWarehouseID, TemuName: b.PlatformWarehouseName, Enabled: b.Effective, Revision: b.Revision})
	}
	return warehouses, mappings, nil
}
func (p *Postgres) WarehouseMapping(ctx context.Context, key string) (model.WarehouseMapping, error) {
	if p.warehouseBindings == nil {
		return p.LegacyWarehouseMapping(ctx, key)
	}
	bindings, err := p.warehouseBindings.WarehouseBindings(ctx, "temu", p.shopCode)
	if err != nil {
		return model.WarehouseMapping{}, err
	}
	for _, b := range bindings {
		if b.WarehouseKey == strings.ToUpper(strings.TrimSpace(key)) {
			return model.WarehouseMapping{OMSKey: b.WarehouseKey, OMSWarehouseCode: b.OMSCode, TemuWarehouseID: b.PlatformWarehouseID, TemuName: b.PlatformWarehouseName, Enabled: b.Effective, Revision: b.Revision}, nil
		}
	}
	return model.WarehouseMapping{}, pgx.ErrNoRows
}
func (p *Postgres) MappedWarehouse(ctx context.Context, key string) (model.Warehouse, error) {
	if p.warehouseBindings == nil {
		return p.LegacyMappedWarehouse(ctx, key)
	}
	b, err := p.WarehouseMapping(ctx, key)
	if err != nil {
		return model.Warehouse{}, err
	}
	if !b.Enabled || b.TemuWarehouseID == "" {
		return model.Warehouse{}, pgx.ErrNoRows
	}
	var w model.Warehouse
	err = p.pool.QueryRow(ctx, `SELECT w.warehouse_id,w.warehouse_name,coalesce(w.region_id,0),w.enable_buy_shipping_label,w.default_warehouse,coalesce(w.warehouse_management_type,0),w.synced_at FROM public.temu_warehouses w JOIN public.temu_shop_warehouses sw USING(warehouse_id) WHERE sw.shop_code=$1 AND w.warehouse_id=$2`, p.shopCode, b.TemuWarehouseID).Scan(&w.ID, &w.Name, &w.RegionID, &w.EnableBuyShippingLabel, &w.Default, &w.ManagementType, &w.SyncedAt)
	w.BindingRevision = b.Revision
	return w, err
}

// Fulfillment after purchase follows immutable physical identity from the quote.
func (p *Postgres) PurchasedWarehouseMapping(shipment model.Shipment) (model.WarehouseMapping, error) {
	codes := map[string]string{"DPS002": "DPSNY002", "DPS004": "DPSCA004", "ARP_EAST": "HYTX30", "ARP_WEST": "ARPCA01", "ARP_HOUSTON": "ARP06A", "ARP_ATLANTA": "ARPGA"}
	code := codes[shipment.OMSWarehouseKey]
	if code == "" {
		return model.WarehouseMapping{}, pgx.ErrNoRows
	}
	return model.WarehouseMapping{OMSKey: shipment.OMSWarehouseKey, OMSWarehouseCode: code, TemuWarehouseID: shipment.WarehouseID}, nil
}

func (p *Postgres) WarehouseActivity(ctx context.Context) (map[string]int, error) {
	rows, err := p.pool.Query(ctx, `SELECT q.oms_warehouse_key,count(*) FROM temu_shipments s JOIN temu_shipping_quotes q ON q.id=s.quote_id WHERE s.status IN ('submitting','submission_unknown') GROUP BY q.oms_warehouse_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err = rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		counts[key] = count
	}
	return counts, rows.Err()
}
