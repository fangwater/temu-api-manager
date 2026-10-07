package service

import (
	"context"
	"errors"
	"temu-api-manager/internal/model"
)

func (s *Service) LegacyWarehouses(ctx context.Context) ([]model.Warehouse, []model.WarehouseMapping, error) {
	return s.store.LegacyListWarehouses(ctx)
}
func (s *Service) validateCurrentWarehouseBinding(ctx context.Context, q model.Quote, saved storedQuoteRequest) error {
	m, err := s.store.WarehouseMapping(ctx, q.OMSWarehouseKey)
	if err != nil {
		return err
	}
	if !m.Enabled || m.TemuWarehouseID != q.TemuWarehouseID || (saved.BindingRevision > 0 && saved.BindingRevision != m.Revision) {
		return errors.New("仓库或当前店铺已暂停，或映射已变化，请重新报价")
	}
	return nil
}

func (s *Service) WarehouseActivity(ctx context.Context) (map[string]int, error) {
	return s.store.WarehouseActivity(ctx)
}
