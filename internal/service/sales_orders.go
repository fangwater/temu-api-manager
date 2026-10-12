package service

import (
	"context"
	"encoding/json"
	"fmt"
	"temu-api-manager/internal/store"
	"time"
)

func (s *Service) SalesSyncStatus(ctx context.Context) (store.SalesSyncState, error) {
	return s.store.SalesSyncState(ctx)
}

func (s *Service) SyncSalesOrders(ctx context.Context) (err error) {
	if !s.salesSyncMu.TryLock() {
		return nil
	}
	defer s.salesSyncMu.Unlock()
	defer func() {
		if err != nil {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.store.SalesSyncFailure(c, err)
		}
	}()
	state, err := s.store.SalesSyncState(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	start := now.Add(-2 * time.Hour)
	if state.Watermark != nil {
		start = state.Watermark.Add(-time.Hour)
	}
	end := now
	if end.Sub(start) > 24*time.Hour {
		end = start.Add(24 * time.Hour)
	}
	if err = s.syncSalesWindow(ctx, "updated", start, end, end); err != nil {
		return err
	}
	if state.Before == nil || (state.Floor != nil && !state.Before.After(*state.Floor) && (state.Reconciled == nil || time.Since(*state.Reconciled) >= 24*time.Hour)) {
		floor := now.AddDate(0, 0, -90)
		if err = s.store.BeginSalesReconciliation(ctx, now, floor); err != nil {
			return err
		}
		state.Before, state.Floor = &now, &floor
	}
	for n := 0; n < 20 && state.Before != nil && state.Floor != nil && state.Before.After(*state.Floor); n++ {
		end = *state.Before
		start = end.Add(-24 * time.Hour)
		if start.Before(*state.Floor) {
			start = *state.Floor
		}
		if err = s.syncSalesWindow(ctx, "created", start, end, start); err != nil {
			return err
		}
		state.Before = &start
	}
	return nil
}
func (s *Service) syncSalesWindow(ctx context.Context, mode string, start, end, next time.Time) error {
	items := make([]json.RawMessage, 0)
	seen := map[string]bool{}
	expected := -1
	for page := 1; page <= 1000; page++ {
		result, err := s.temu.SalesOrders(ctx, mode, start, end, page)
		if err != nil {
			return err
		}
		if expected < 0 {
			expected = result.TotalItemNum
		}
		if expected != result.TotalItemNum {
			return fmt.Errorf("sales pagination changed; retry window")
		}
		for _, raw := range result.PageItems {
			var v struct {
				Parent struct {
					PO string `json:"parentOrderSn"`
				} `json:"parentOrderMap"`
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				return err
			}
			if v.Parent.PO == "" || seen[v.Parent.PO] {
				return fmt.Errorf("sales pagination duplicate/missing identity; retry window")
			}
			seen[v.Parent.PO] = true
			items = append(items, raw)
		}
		if len(items) == expected {
			return s.store.CommitSalesWindow(ctx, mode, next, items)
		}
		if len(result.PageItems) == 0 || len(items) > expected {
			return fmt.Errorf("sales pagination incomplete; retry window")
		}
	}
	return fmt.Errorf("sales pagination exceeded limit")
}
