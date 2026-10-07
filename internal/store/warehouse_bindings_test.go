package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"testing"
)

type warehouseSourceFake struct {
	shop string
	fail bool
}

func (f *warehouseSourceFake) WarehouseBindings(_ context.Context, platform, shop string) ([]inventory.WarehouseBinding, error) {
	f.shop = shop
	if f.fail {
		return nil, errors.New("service unavailable")
	}
	return []inventory.WarehouseBinding{{WarehouseKey: "ARP_HOUSTON", OMSCode: "ARP06A", PlatformWarehouseID: "WH-SHOP", Effective: false, Revision: 3}}, nil
}
func TestWarehouseMappingUsesCurrentShopAndStopsWhenPaused(t *testing.T) {
	source := &warehouseSourceFake{}
	p := &Postgres{shopCode: "panda-buy", warehouseBindings: source}
	b, err := p.WarehouseMapping(context.Background(), "ARP_HOUSTON")
	if err != nil || b.Enabled || source.shop != "panda-buy" || b.Revision != 3 {
		t.Fatal("mapping ignored shop or runtime pause")
	}
	if _, err = p.MappedWarehouse(context.Background(), "ARP_HOUSTON"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("paused mapping still selectable")
	}
	source.fail = true
	if _, err = p.WarehouseMapping(context.Background(), "ARP_HOUSTON"); err == nil {
		t.Fatal("failure fell back to shared mapping")
	}
}
func TestPurchasedWarehouseKeepsPhysicalIdentityAfterPause(t *testing.T) {
	p := &Postgres{warehouseBindings: &warehouseSourceFake{fail: true}}
	b, err := p.PurchasedWarehouseMapping(model.Shipment{OMSWarehouseKey: "ARP_HOUSTON", WarehouseID: "WH-ORIGINAL"})
	if err != nil || b.OMSWarehouseCode != "ARP06A" || b.TemuWarehouseID != "WH-ORIGINAL" {
		t.Fatal("bought label depends on current mapping")
	}
}
