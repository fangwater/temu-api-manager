package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/oms"
	"temu-api-manager/internal/store"
	"temu-api-manager/internal/temu"

	"github.com/jackc/pgx/v5"
)

var numericText = regexp.MustCompile(`[-+]?[0-9]*\.?[0-9]+`)

const (
	shipmentSubmissionRetryDelay    = 2 * time.Minute
	shipmentConfirmationStallDelay  = 5 * time.Minute
	maxShipmentSubmissionAttempts   = 3
	maxShipmentConfirmationAttempts = 3
)

var (
	errShipmentSubmissionAttemptsExhausted = errors.New("Temu 购单安全重试已达到 3 次，订单详情仍无 packageSn")
	errOrderNoLongerAwaitingShipment       = errors.New("order is no longer awaiting shipment")
	ErrPackageSNNotReady                   = errors.New("packageSn is not available for this order")
)

type Service struct {
	store         *store.Postgres
	temu          *temu.Client
	inventory     *inventory.Client
	oms           *oms.Client
	shopCode      string
	shopName      string
	quoteLifetime time.Duration
	logger        *slog.Logger
	syncMu        sync.Mutex
}

func New(destination *store.Postgres, temuClient *temu.Client, inventoryClient *inventory.Client, omsClient *oms.Client, quoteLifetime time.Duration, logger *slog.Logger) *Service {
	return NewForShop(destination, temuClient, inventoryClient, omsClient, "", "", quoteLifetime, logger)
}

func NewForShop(destination *store.Postgres, temuClient *temu.Client, inventoryClient *inventory.Client, omsClient *oms.Client, shopCode, shopName string, quoteLifetime time.Duration, logger *slog.Logger) *Service {
	return &Service{
		store: destination, temu: temuClient, inventory: inventoryClient, oms: omsClient,
		shopCode: strings.ToLower(strings.TrimSpace(shopCode)), shopName: strings.TrimSpace(shopName),
		quoteLifetime: quoteLifetime, logger: logger,
	}
}

func (s *Service) queryInventory(ctx context.Context, quantities map[string]int) (inventory.DecisionResponse, error) {
	if s.shopCode == "" {
		return s.inventory.Query(ctx, quantities)
	}
	return s.inventory.QueryForShop(ctx, "temu", s.shopCode, quantities)
}

func (s *Service) ShopInventoryThresholds(ctx context.Context) (inventory.ShopInventoryThresholds, error) {
	if s.shopCode == "" {
		return inventory.ShopInventoryThresholds{}, errors.New("shop code is required")
	}
	return s.inventory.ShopInventoryThresholds(ctx, "temu", s.shopCode)
}

func (s *Service) UpdateShopInventoryThresholds(ctx context.Context, thresholds inventory.InventoryThresholds) (inventory.ShopInventoryThresholds, error) {
	if s.shopCode == "" {
		return inventory.ShopInventoryThresholds{}, errors.New("shop code is required")
	}
	return s.inventory.UpdateShopInventoryThresholds(ctx, "temu", s.shopCode, thresholds)
}

func (s *Service) ResetShopInventoryThresholds(ctx context.Context) (inventory.ShopInventoryThresholds, error) {
	if s.shopCode == "" {
		return inventory.ShopInventoryThresholds{}, errors.New("shop code is required")
	}
	return s.inventory.ResetShopInventoryThresholds(ctx, "temu", s.shopCode)
}

func (s *Service) ListShopSKUInventoryThresholds(ctx context.Context, query string, page, pageSize int) (inventory.InventoryThresholdPage, error) {
	if s.shopCode == "" {
		return inventory.InventoryThresholdPage{}, errors.New("shop code is required")
	}
	return s.inventory.ListShopSKUInventoryThresholds(ctx, "temu", s.shopCode, query, page, pageSize)
}

func (s *Service) UpdateShopSKUInventoryThreshold(ctx context.Context, warehouseSKU string, thresholds inventory.InventoryThresholds) (inventory.SKUInventoryThreshold, error) {
	if s.shopCode == "" {
		return inventory.SKUInventoryThreshold{}, errors.New("shop code is required")
	}
	return s.inventory.UpdateShopSKUInventoryThreshold(ctx, "temu", s.shopCode, warehouseSKU, thresholds)
}

func (s *Service) ResetShopSKUInventoryThreshold(ctx context.Context, warehouseSKU string) error {
	if s.shopCode == "" {
		return errors.New("shop code is required")
	}
	return s.inventory.ResetShopSKUInventoryThreshold(ctx, "temu", s.shopCode, warehouseSKU)
}

type TokenStatus struct {
	Valid            bool            `json:"valid"`
	State            string          `json:"state"`
	ExpiresAt        time.Time       `json:"expires_at"`
	RemainingSeconds int64           `json:"remaining_seconds"`
	RemainingText    string          `json:"remaining_text"`
	MallID           int64           `json:"mall_id"`
	MallType         int             `json:"mall_type"`
	RegionID         int64           `json:"region_id"`
	ScopeCount       int             `json:"scope_count"`
	RequiredScopes   map[string]bool `json:"required_scopes"`
}

func (s *Service) TokenStatus(ctx context.Context) (TokenStatus, error) {
	info, _, err := s.temu.TokenInfo(ctx)
	if err != nil {
		var apiErr *temu.APIError
		if errors.As(err, &apiErr) && (apiErr.Code == "3000034" || strings.Contains(strings.ToLower(apiErr.Message), "access_token is expired")) {
			return TokenStatus{Valid: false, State: "expired", RemainingText: "现在需要重新授权", RequiredScopes: map[string]bool{}}, nil
		}
		return TokenStatus{}, err
	}
	now := time.Now()
	expires := time.Unix(info.ExpiredTime, 0)
	remaining := info.ExpiredTime - now.Unix()
	state := "healthy"
	if remaining <= 0 {
		state = "expired"
	} else if remaining <= int64((30 * 24 * time.Hour).Seconds()) {
		state = "warning"
	}
	scopes := make(map[string]bool)
	for _, required := range []string{temu.OrderListAPI, temu.OrderDetailAPI, temu.CombinedShipmentAPI, temu.WarehouseListAPI, temu.ShippingServicesAPI, temu.ShipmentCreateAPI, temu.ShipmentResultAPI, temu.ShipmentDocumentAPI, temu.ShipmentConfirmAPI, temu.TrackingInfoAPI} {
		scopes[required] = contains(info.APIScopes, required)
	}
	return TokenStatus{Valid: remaining > 0, State: state, ExpiresAt: expires, RemainingSeconds: remaining,
		RemainingText: humanDuration(remaining), MallID: info.MallID, MallType: info.MallType,
		RegionID: info.RegionID, ScopeCount: len(info.APIScopes), RequiredScopes: scopes}, nil
}

type CombinedShipmentCandidate struct {
	ParentOrderSN     string `json:"parent_order_sn"`
	ParentOrderStatus int    `json:"parent_order_status"`
	ParentOrderTime   int64  `json:"parent_order_time"`
	MallID            int64  `json:"mall_id"`
	SemiUniqueID      string `json:"semi_unique_id,omitempty"`
}

type CombinedShipmentCandidateGroup struct {
	Orders []CombinedShipmentCandidate `json:"orders"`
}

type CombinedShipmentCandidates struct {
	Groups      []CombinedShipmentCandidateGroup `json:"groups"`
	TotalGroups int                              `json:"total_groups"`
	TotalOrders int                              `json:"total_orders"`
	QueriedAt   time.Time                        `json:"queried_at"`
}

func (s *Service) CombinedShipmentCandidates(ctx context.Context) (CombinedShipmentCandidates, error) {
	result, _, err := s.temu.CombinedShipments(ctx)
	if err != nil {
		return CombinedShipmentCandidates{}, err
	}
	groups := make([]CombinedShipmentCandidateGroup, 0, len(result.Groups))
	totalOrders := 0
	for _, sourceGroup := range result.Groups {
		orders := make([]CombinedShipmentCandidate, 0, len(sourceGroup.Orders))
		for _, sourceOrder := range sourceGroup.Orders {
			parentOrderSN := strings.TrimSpace(sourceOrder.ParentOrderSN)
			if parentOrderSN == "" {
				continue
			}
			orders = append(orders, CombinedShipmentCandidate{
				ParentOrderSN: parentOrderSN, ParentOrderStatus: sourceOrder.ParentOrderStatus,
				ParentOrderTime: sourceOrder.ParentOrderTime, MallID: sourceOrder.MallID,
				SemiUniqueID: strings.TrimSpace(sourceOrder.SemiUniqueID),
			})
		}
		if len(orders) == 0 {
			continue
		}
		groups = append(groups, CombinedShipmentCandidateGroup{Orders: orders})
		totalOrders += len(orders)
	}
	return CombinedShipmentCandidates{
		Groups: groups, TotalGroups: len(groups), TotalOrders: totalOrders, QueriedAt: time.Now(),
	}, nil
}

func (s *Service) SyncOrders(ctx context.Context) (model.SyncStatus, error) {
	if !s.syncMu.TryLock() {
		return s.store.LatestSync(ctx)
	}
	defer s.syncMu.Unlock()
	runID, started, err := s.store.StartSync(ctx)
	if err != nil {
		return model.SyncStatus{}, err
	}
	orders := make([]model.Order, 0)
	for page := 1; ; page++ {
		result, _, fetchErr := s.temu.OrderPage(ctx, page, 100)
		if fetchErr != nil {
			_ = s.store.FinishSync(context.WithoutCancel(ctx), runID, "failed", len(orders), countLines(orders), fetchErr.Error())
			return model.SyncStatus{}, fetchErr
		}
		for _, item := range result.PageItems {
			orders = append(orders, normalizeOrder(item))
		}
		if len(result.PageItems) < 100 || len(orders) >= result.TotalItemNum {
			break
		}
		if page >= 1000 {
			return model.SyncStatus{}, errors.New("Temu order sync exceeded 1000 pages")
		}
	}
	lines, err := s.store.ReplaceOpenOrders(ctx, orders, started)
	if err != nil {
		_ = s.store.FinishSync(context.WithoutCancel(ctx), runID, "failed", len(orders), lines, err.Error())
		return model.SyncStatus{}, err
	}
	if err := s.store.FinishSync(ctx, runID, "succeeded", len(orders), lines, ""); err != nil {
		return model.SyncStatus{}, err
	}
	return s.store.LatestSync(ctx)
}

func (s *Service) LatestSync(ctx context.Context) (model.SyncStatus, error) {
	return s.store.LatestSync(ctx)
}

type PendingPickupAuditSync struct {
	Fetched int `json:"fetched"`
	Synced  int `json:"synced"`
}

func (s *Service) SyncPendingPickupAudits(ctx context.Context, shopCode, shopName string) (PendingPickupAuditSync, error) {
	const pendingPickupStatus = 4
	items := make([]temu.OrderPageItem, 0, 500)
	for page := 1; ; page++ {
		result, _, err := s.temu.OrderPageByStatus(ctx, pendingPickupStatus, page, 100)
		if err != nil {
			return PendingPickupAuditSync{Fetched: len(items)}, err
		}
		items = append(items, result.PageItems...)
		if len(items) > 5000 {
			return PendingPickupAuditSync{Fetched: len(items)}, errors.New("pending-pickup snapshot exceeds 5000 orders")
		}
		if len(result.PageItems) == 0 || len(items) >= result.TotalItemNum || len(result.PageItems) < 100 {
			break
		}
	}
	parentOrderSNs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		parent := strings.TrimSpace(item.Parent.ParentOrderSN)
		if parent == "" {
			continue
		}
		if _, exists := seen[parent]; exists {
			continue
		}
		seen[parent] = struct{}{}
		parentOrderSNs = append(parentOrderSNs, parent)
	}
	shipments, err := s.store.FulfillmentAuditShipments(ctx, parentOrderSNs)
	if err != nil {
		return PendingPickupAuditSync{Fetched: len(parentOrderSNs)}, err
	}
	orders := make([]oms.FulfillmentAuditSnapshotOrder, 0, len(parentOrderSNs))
	for _, item := range items {
		parent := strings.TrimSpace(item.Parent.ParentOrderSN)
		if _, exists := seen[parent]; !exists {
			continue
		}
		delete(seen, parent)
		status := item.Parent.ParentOrderStatus
		var shippingAt *time.Time
		if item.Parent.ParentShippingTime > 0 {
			value := time.Unix(item.Parent.ParentShippingTime, 0)
			shippingAt = &value
		}
		shipment := shipments[parent]
		if shippingAt == nil {
			shippingAt = shipment.ConfirmedAt
		}
		orders = append(orders, oms.FulfillmentAuditSnapshotOrder{
			PlatformOrderNo: parent, PlatformStatus: "pending_pickup", PlatformStatusCode: &status,
			PlatformShippingAt: shippingAt, WarehouseKey: shipment.WarehouseKey,
			WarehouseCode: shipment.WarehouseCode, TrackingNumber: shipment.TrackingNumber,
		})
	}
	if err := s.oms.SyncFulfillmentAudits(ctx, oms.FulfillmentAuditSnapshot{
		Platform: "temu", ShopCode: shopCode, ShopName: shopName, Orders: orders,
	}); err != nil {
		return PendingPickupAuditSync{Fetched: len(parentOrderSNs)}, err
	}
	return PendingPickupAuditSync{Fetched: len(parentOrderSNs), Synced: len(orders)}, nil
}

func (s *Service) ListOrders(ctx context.Context, query string, unreservedOnly bool, page, pageSize int) ([]model.Order, int, error) {
	return s.store.ListOrders(ctx, query, unreservedOnly, page, pageSize)
}
func (s *Service) GetOrder(ctx context.Context, parent string) (model.Order, error) {
	return s.store.GetOrder(ctx, strings.TrimSpace(parent))
}

func (s *Service) RefreshOrderDetail(ctx context.Context, parent string) (model.Order, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return model.Order{}, errors.New("parent order number is required")
	}
	stored, err := s.store.GetOrder(ctx, parent)
	if err != nil {
		return model.Order{}, err
	}
	result, raw, err := s.temu.OrderDetail(ctx, parent)
	if err != nil {
		return model.Order{}, err
	}
	regions := make([]string, 0, 3)
	for _, region := range []string{result.Parent.RegionName1, result.Parent.RegionName2, result.Parent.RegionName3} {
		if region = strings.TrimSpace(region); region != "" {
			regions = append(regions, region)
		}
	}
	detail := model.OrderDetail{ParentOrderSN: parent, RegionNames: regions,
		BatchOrderSNs: append([]string{}, result.Parent.BatchOrderNumberList...),
		Consolidated:  result.Parent.Consolidated, SourceUpdateAt: stored.UpdateTime,
		OpenAtFetch: stored.Open, Raw: raw}
	if err := s.store.SaveOrderDetail(ctx, detail); err != nil {
		return model.Order{}, err
	}
	if len(detail.BatchOrderSNs) > 0 {
		stored.BatchOrderSNs = detail.BatchOrderSNs
	}
	stored.Consolidated = stored.Consolidated || detail.Consolidated
	if review := classifyOrder(stored); review != nil {
		if err := s.store.UpsertManualReview(ctx, *review); err != nil {
			return model.Order{}, err
		}
	}
	return s.store.GetOrder(ctx, parent)
}

func (s *Service) SyncOrderDetails(ctx context.Context, limit int) (int, error) {
	candidates, err := s.store.DetailCandidates(ctx, limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, candidate := range candidates {
		if _, err := s.RefreshOrderDetail(ctx, candidate.ParentOrderSN); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

type WarehouseClassificationStats struct {
	Checked  int `json:"checked"`
	Eligible int `json:"eligible"`
	Manual   int `json:"manual"`
	Failed   int `json:"failed"`
}

type warehouseClassificationGroup struct {
	quantities map[string]int
	orders     []model.Order
}

func (s *Service) ClassifyWarehouseQueue(ctx context.Context, limit int) (WarehouseClassificationStats, error) {
	orders, err := s.store.ListWarehouseClassificationCandidates(ctx, limit)
	if err != nil {
		return WarehouseClassificationStats{}, err
	}
	stats := WarehouseClassificationStats{Checked: len(orders)}
	groups := make(map[string]*warehouseClassificationGroup)
	for _, order := range orders {
		quantities, missing := warehouseQuantities(order)
		if len(missing) > 0 {
			classification := model.WarehouseClassification{
				ParentOrderSN: order.ParentOrderSN, SourceUpdateAt: order.UpdateTime, Status: "manual",
				Categories: []string{"sku_unbound"}, ReasonDetails: missing,
			}
			if err := s.persistWarehouseClassification(ctx, order, classification); err != nil {
				return stats, err
			}
			stats.Manual++
			continue
		}
		keyParts := make([]string, 0, len(quantities))
		for _, sku := range sortedKeys(quantities) {
			keyParts = append(keyParts, fmt.Sprintf("%s=%d", sku, quantities[sku]))
		}
		key := strings.Join(keyParts, "\x00")
		group := groups[key]
		if group == nil {
			group = &warehouseClassificationGroup{quantities: quantities}
			groups[key] = group
		}
		group.orders = append(group.orders, order)
	}
	for _, group := range groups {
		decision, queryErr := s.queryInventory(ctx, group.quantities)
		if queryErr == nil {
			queryErr = s.applyShopSKUWarehouseRules(ctx, &decision)
		}
		for _, order := range group.orders {
			classification := warehouseClassificationFromDecision(order, decision, queryErr)
			if err := s.persistWarehouseClassification(ctx, order, classification); err != nil {
				return stats, err
			}
			switch classification.Status {
			case "eligible":
				stats.Eligible++
			case "manual":
				stats.Manual++
			default:
				stats.Failed++
			}
		}
	}
	return stats, nil
}

func warehouseQuantities(order model.Order) (map[string]int, []string) {
	quantities := make(map[string]int)
	missing := make([]string, 0)
	for _, line := range order.Lines {
		sku := strings.TrimSpace(line.ExtCode)
		if sku == "" {
			missing = append(missing, fmt.Sprintf("商品行 %s 缺少仓库 SKU（extCode），无法自动发货", line.OrderSN))
			continue
		}
		quantities[sku] += line.Quantity
	}
	return quantities, missing
}

func warehouseClassificationFromDecision(order model.Order, decision inventory.DecisionResponse, queryErr error) model.WarehouseClassification {
	item := model.WarehouseClassification{
		ParentOrderSN: order.ParentOrderSN, SourceUpdateAt: order.UpdateTime,
		Status: "eligible", Categories: []string{}, ReasonDetails: []string{},
	}
	if queryErr != nil {
		item.Status = "failed"
		item.ErrorMessage = queryErr.Error()
		return item
	}
	unboundSKUs := inventoryUnboundSKUs(decision)
	if len(unboundSKUs) > 0 {
		item.Categories = append(item.Categories, "sku_unbound")
		for _, sku := range unboundSKUs {
			item.ReasonDetails = append(item.ReasonDetails, sku+"：领星 OMS 未返回该商品，需人工完成 SKU 绑定")
		}
	}
	inventoryManual := false
	for _, record := range decision.Records {
		if record.RequiresManual && !contains(unboundSKUs, record.SKU) {
			inventoryManual = true
			item.ReasonDetails = append(item.ReasonDetails, record.SKU+"："+record.Reason)
		}
	}
	if inventoryManual {
		item.Categories = append(item.Categories, manualReasonInventoryRule)
	}
	if !inventoryManual && len(unboundSKUs) == 0 && decisionHasShopSKUWarehouseRestrictions(decision) {
		quantities, _ := warehouseQuantities(order)
		_, eastErr := inventory.SelectWarehouse(decision, "east", quantities)
		_, westErr := inventory.SelectWarehouse(decision, "west", quantities)
		if eastErr != nil && westErr != nil {
			item.Categories = append(item.Categories, manualReasonSKUWarehousePolicy)
			item.ReasonDetails = append(item.ReasonDetails,
				"当前店铺的 SKU 发货仓库规则未保留可覆盖订单全部商品的仓库")
		}
	}
	if !decision.PackageResolution.Complete {
		item.Categories = append(item.Categories, manualReasonWarehouseSKUSpec)
		reason := strings.TrimSpace(decision.PackageResolution.Error)
		if reason == "" {
			reason = "仓库 SKU 的重量或包裹尺寸不完整，无法自动购买面单"
		}
		item.ReasonDetails = append(item.ReasonDetails, reason)
	}
	if len(item.Categories) > 0 {
		item.Status = "manual"
	}
	sort.Strings(item.Categories)
	return item
}

func (s *Service) persistWarehouseClassification(ctx context.Context, order model.Order, item model.WarehouseClassification) error {
	if err := s.store.SaveWarehouseClassification(ctx, item); err != nil {
		return fmt.Errorf("save warehouse classification for %s: %w", order.ParentOrderSN, err)
	}
	if item.Status == "failed" {
		return nil
	}
	if item.Status == "eligible" {
		for _, reason := range []string{"sku_unbound", manualReasonInventoryRule, manualReasonWarehouseSKUSpec, manualReasonSKUWarehousePolicy} {
			if err := s.store.ClearManualReviewReason(ctx, order.ParentOrderSN, reason); err != nil {
				return fmt.Errorf("clear recovered warehouse classification %s for %s: %w", reason, order.ParentOrderSN, err)
			}
		}
		return nil
	}
	review := classifyOrder(order)
	if review == nil {
		review = &model.ManualReview{ParentOrderSN: order.ParentOrderSN, Reasons: []string{}, MergeOrderSNs: []string{}, Status: "detected", Active: true}
	}
	for _, category := range item.Categories {
		if !contains(review.Reasons, category) {
			review.Reasons = append(review.Reasons, category)
		}
	}
	sort.Strings(review.Reasons)
	if err := s.store.UpsertManualReview(ctx, *review); err != nil {
		return fmt.Errorf("persist manual warehouse classification for %s: %w", order.ParentOrderSN, err)
	}
	if _, err := s.store.UpdateManualReview(ctx, order.ParentOrderSN, "manual_pending", "", ""); err != nil {
		return fmt.Errorf("move %s to manual queue: %w", order.ParentOrderSN, err)
	}
	return nil
}

func (s *Service) addManualReviewReason(ctx context.Context, parentOrderSN, reason string) error {
	order, err := s.store.GetOrder(ctx, parentOrderSN)
	if err != nil {
		return err
	}
	review := classifyOrder(order)
	if review == nil {
		review = &model.ManualReview{
			ParentOrderSN: order.ParentOrderSN,
			Reasons:       []string{},
			MergeOrderSNs: []string{},
			Status:        "manual_pending",
			Active:        true,
		}
	}
	if !contains(review.Reasons, reason) {
		review.Reasons = append(review.Reasons, reason)
	}
	sort.Strings(review.Reasons)
	if err := s.store.UpsertManualReview(ctx, *review); err != nil {
		return err
	}
	_, err = s.store.UpdateManualReview(ctx, parentOrderSN, "manual_pending", "", "")
	return err
}

func (s *Service) ListOrderHistory(ctx context.Context, page, pageSize int) ([]model.Order, int, error) {
	return s.store.ListOrderHistory(ctx, page, pageSize)
}

func (s *Service) ListManualReviews(ctx context.Context, status, query string, page, pageSize int) ([]model.ManualReview, int, error) {
	return s.store.ListManualReviews(ctx, status, query, page, pageSize)
}

func (s *Service) ListAllManualReviews(ctx context.Context, status, query string) ([]model.ManualReview, error) {
	const pageSize = 100
	items := make([]model.ManualReview, 0, pageSize)
	for page := 1; ; page++ {
		batch, total, err := s.store.ListManualReviews(ctx, status, query, page, pageSize)
		if err != nil {
			return nil, err
		}
		items = append(items, batch...)
		if len(items) >= total || len(batch) < pageSize {
			return items, nil
		}
	}
}

func (s *Service) UpdateManualReview(ctx context.Context, parent, status, outcome, note string) (model.ManualReview, error) {
	if status != "manual_pending" && status != "approved" && status != "resolved" {
		return model.ManualReview{}, errors.New("status must be manual_pending, approved, or resolved")
	}
	parent = strings.TrimSpace(parent)
	outcome = strings.TrimSpace(outcome)
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 1000 {
		return model.ManualReview{}, errors.New("note must not exceed 1000 characters")
	}
	if status == "resolved" {
		if !validManualReviewOutcome(outcome) {
			return model.ManualReview{}, errors.New("outcome must be manually_fulfilled, cancelled, not_required, or other")
		}
		order, err := s.store.GetOrder(ctx, parent)
		if err != nil {
			return model.ManualReview{}, err
		}
		if order.ManualReview == nil || !order.ManualReview.Active || order.ManualReview.Status != "manual_pending" {
			return model.ManualReview{}, errors.New("only an order currently in manual processing can be completed")
		}
	}
	if status == "manual_pending" || status == "approved" {
		order, err := s.RefreshOrderDetail(ctx, parent)
		if err != nil {
			return model.ManualReview{}, fmt.Errorf("refresh order detail before manual transition: %w", err)
		}
		if status == "approved" {
			if hasActiveManualReason(order, "sku_unbound") {
				return model.ManualReview{}, errors.New("order still has an unverified OMS SKU binding; recheck the binding before approval")
			}
			if hasActiveManualReason(order, manualReasonInventoryRule) {
				return model.ManualReview{}, errors.New("order still fails the inventory safety rule; recheck inventory before approval")
			}
			if hasActiveManualReason(order, manualReasonWarehouseSKUSpec) {
				return model.ManualReview{}, errors.New("order still has incomplete warehouse SKU package data; recheck the package data before approval")
			}
			if hasActiveManualReason(order, manualReasonDeliveryAddress) {
				return model.ManualReview{}, errors.New("Temu integrated logistics does not support this delivery address; switch the order to manual shipping in Temu before resolving it")
			}
			for _, line := range order.Lines {
				if strings.TrimSpace(line.ExtCode) == "" {
					return model.ManualReview{}, fmt.Errorf("order line %s has no extCode; bind the SKU before approving automatic fulfillment", line.OrderSN)
				}
			}
		}
	}
	return s.store.UpdateManualReview(ctx, parent, status, outcome, note)
}

func validManualReviewOutcome(outcome string) bool {
	switch outcome {
	case "manually_fulfilled", "cancelled", "not_required", "other":
		return true
	default:
		return false
	}
}
func (s *Service) SyncWarehouses(ctx context.Context) ([]model.Warehouse, []model.WarehouseMapping, error) {
	result, _, err := s.temu.Warehouses(ctx)
	if err != nil {
		return nil, nil, err
	}
	items := make([]model.Warehouse, 0, len(result.Warehouses))
	for _, item := range result.Warehouses {
		raw, _ := json.Marshal(item)
		items = append(items, model.Warehouse{ID: item.ID, Name: item.Name, RegionID: item.RegionID,
			EnableBuyShippingLabel: item.EnableBuyShippingLabel, Default: item.Default,
			ManagementType: item.ManagementType, Raw: raw})
	}
	if err := s.store.ReplaceWarehouses(ctx, items); err != nil {
		return nil, nil, err
	}
	return s.store.ListWarehouses(ctx)
}

func (s *Service) ListWarehouses(ctx context.Context) ([]model.Warehouse, []model.WarehouseMapping, error) {
	return s.store.ListWarehouses(ctx)
}
func (s *Service) SetWarehouseMapping(ctx context.Context, omsKey, temuID, omsWarehouseCode, omsAccount string) (model.WarehouseMapping, error) {
	omsKey = strings.ToUpper(strings.TrimSpace(omsKey))
	if reason := disabledOMSWarehouseReason(omsKey); reason != "" {
		return model.WarehouseMapping{}, errors.New(reason)
	}
	omsWarehouseCode = strings.TrimSpace(omsWarehouseCode)
	if omsWarehouseCode == "" {
		return model.WarehouseMapping{}, errors.New("oms_warehouse_code is required")
	}
	omsAccount, ok := normalizeOMSAccount(omsAccount)
	if !ok {
		return model.WarehouseMapping{}, errors.New("oms_account must be dps or arp")
	}
	return s.store.SetWarehouseMapping(ctx, omsKey, temuID, omsWarehouseCode, omsAccount)
}
func (s *Service) DeleteWarehouseMapping(ctx context.Context, omsKey string) error {
	return s.store.DeleteWarehouseMapping(ctx, omsKey)
}

func disabledOMSWarehouseReason(omsKey string) string {
	if strings.ToUpper(strings.TrimSpace(omsKey)) == "ARP_WEST" {
		return "ARP美西暂不启用（空置），不能用于自动发货"
	}
	return ""
}

var supportedOMSWarehouseKeys = []string{"DPS002", "ARP_EAST", "DPS004", "ARP_WEST"}

func (s *Service) ListCarrierPolicies(ctx context.Context) ([]model.WarehouseCarrierPolicies, error) {
	stored, err := s.store.ListCarrierPolicies(ctx)
	if err != nil {
		return nil, err
	}
	return mergeCarrierPolicies(stored), nil
}

func (s *Service) UpdateCarrierPolicies(ctx context.Context, warehouseKey string, policies []model.CarrierPolicy) (model.WarehouseCarrierPolicies, error) {
	normalized, err := validateCarrierPolicies(warehouseKey, policies)
	if err != nil {
		return model.WarehouseCarrierPolicies{}, err
	}
	if err := s.store.ReplaceCarrierPolicies(ctx, normalized[0].WarehouseKey, normalized); err != nil {
		return model.WarehouseCarrierPolicies{}, err
	}
	return model.WarehouseCarrierPolicies{WarehouseKey: normalized[0].WarehouseKey, Carriers: normalized}, nil
}

func defaultCarrierPolicies(warehouseKey string) []model.CarrierPolicy {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	policies := make([]model.CarrierPolicy, 0, len(supportedAutomaticCarrierCodes))
	for index, code := range supportedAutomaticCarrierCodes {
		policies = append(policies, model.CarrierPolicy{
			WarehouseKey: warehouseKey,
			CarrierCode:  code,
			Priority:     index + 1,
			Enabled:      true,
		})
	}
	return policies
}

func mergeCarrierPolicies(stored []model.CarrierPolicy) []model.WarehouseCarrierPolicies {
	byWarehouse := make(map[string][]model.CarrierPolicy)
	for _, policy := range stored {
		key := strings.ToUpper(strings.TrimSpace(policy.WarehouseKey))
		byWarehouse[key] = append(byWarehouse[key], policy)
	}
	groups := make([]model.WarehouseCarrierPolicies, 0, 4)
	for _, warehouseKey := range []string{"DPS002", "ARP_EAST", "DPS004", "ARP_WEST"} {
		policies, err := validateCarrierPolicies(warehouseKey, byWarehouse[warehouseKey])
		if err != nil {
			policies = defaultCarrierPolicies(warehouseKey)
		}
		groups = append(groups, model.WarehouseCarrierPolicies{WarehouseKey: warehouseKey, Carriers: policies})
	}
	return groups
}

func validateCarrierPolicies(warehouseKey string, policies []model.CarrierPolicy) ([]model.CarrierPolicy, error) {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	if !contains([]string{"DPS002", "ARP_EAST", "DPS004", "ARP_WEST"}, warehouseKey) {
		return nil, errors.New("unknown OMS warehouse")
	}
	if len(policies) != len(supportedAutomaticCarrierCodes) {
		return nil, fmt.Errorf("exactly %d carrier policies are required", len(supportedAutomaticCarrierCodes))
	}
	seenCodes := make(map[string]bool, len(policies))
	seenPriorities := make(map[int]bool, len(policies))
	normalized := make([]model.CarrierPolicy, 0, len(policies))
	for _, policy := range policies {
		code := strings.ToUpper(strings.TrimSpace(policy.CarrierCode))
		if !automaticCarrierWhitelist[code] {
			return nil, fmt.Errorf("unsupported automatic carrier %q", policy.CarrierCode)
		}
		if seenCodes[code] {
			return nil, fmt.Errorf("carrier %s is duplicated", code)
		}
		if policy.Priority < 1 || policy.Priority > len(supportedAutomaticCarrierCodes) || seenPriorities[policy.Priority] {
			return nil, errors.New("carrier priorities must be unique consecutive values")
		}
		seenCodes[code] = true
		seenPriorities[policy.Priority] = true
		normalized = append(normalized, model.CarrierPolicy{
			WarehouseKey: warehouseKey,
			CarrierCode:  code,
			Priority:     policy.Priority,
			Enabled:      policy.Enabled,
		})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Priority < normalized[j].Priority })
	return normalized, nil
}

func (s *Service) carrierPoliciesByWarehouse(ctx context.Context) (map[string][]model.CarrierPolicy, error) {
	groups, err := s.ListCarrierPolicies(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]model.CarrierPolicy, len(groups))
	for _, group := range groups {
		result[group.WarehouseKey] = group.Carriers
	}
	return result, nil
}

func (s *Service) ListSKUWarehouseRules(ctx context.Context, query string, page, pageSize int) ([]model.SKUWarehouseRule, int, error) {
	return s.store.ListSKUWarehouseRules(ctx, query, page, pageSize)
}

func (s *Service) UpdateSKUWarehouseRule(ctx context.Context, warehouseSKU string, disabledWarehouseKeys []string) (model.SKUWarehouseRule, error) {
	warehouseSKU, disabledWarehouseKeys, err := validateSKUWarehouseRule(warehouseSKU, disabledWarehouseKeys)
	if err != nil {
		return model.SKUWarehouseRule{}, err
	}
	if err := s.store.ReplaceSKUDisabledWarehouses(ctx, warehouseSKU, disabledWarehouseKeys); err != nil {
		return model.SKUWarehouseRule{}, err
	}
	item := model.SKUWarehouseRule{
		WarehouseSKU: warehouseSKU, DisabledWarehouseKeys: disabledWarehouseKeys,
		Customized: len(disabledWarehouseKeys) > 0,
	}
	if item.Customized {
		now := time.Now()
		item.UpdatedAt = &now
	}
	return item, nil
}

func validateSKUWarehouseRule(warehouseSKU string, disabledWarehouseKeys []string) (string, []string, error) {
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return "", nil, errors.New("warehouse_sku is required")
	}
	if len(warehouseSKU) > 255 {
		return "", nil, errors.New("warehouse_sku is too long")
	}
	seen := make(map[string]bool, len(disabledWarehouseKeys))
	for _, warehouseKey := range disabledWarehouseKeys {
		warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
		if !contains(supportedOMSWarehouseKeys, warehouseKey) {
			return "", nil, fmt.Errorf("unknown OMS warehouse %q", warehouseKey)
		}
		seen[warehouseKey] = true
	}
	normalized := make([]string, 0, len(seen))
	for _, warehouseKey := range supportedOMSWarehouseKeys {
		if seen[warehouseKey] {
			normalized = append(normalized, warehouseKey)
		}
	}
	return warehouseSKU, normalized, nil
}

func (s *Service) applyShopSKUWarehouseRules(ctx context.Context, decision *inventory.DecisionResponse) error {
	warehouseSKUs := make([]string, 0, len(decision.Records))
	for _, record := range decision.Records {
		warehouseSKUs = append(warehouseSKUs, record.SKU)
	}
	disabled, err := s.store.DisabledWarehouseKeysForSKUs(ctx, warehouseSKUs)
	if err != nil {
		return err
	}
	applySKUWarehouseRestrictions(decision, disabled)
	return nil
}

func applySKUWarehouseRestrictions(decision *inventory.DecisionResponse, disabled map[string]map[string]bool) {
	for recordIndex := range decision.Records {
		record := &decision.Records[recordIndex]
		restrictions := disabled[strings.TrimSpace(record.SKU)]
		if len(restrictions) == 0 {
			continue
		}
		for regionIndex := range record.Regions {
			region := &record.Regions[regionIndex]
			recommendedSelectable := false
			for warehouseIndex := range region.Warehouses {
				warehouse := &region.Warehouses[warehouseIndex]
				warehouseKey := strings.ToUpper(strings.TrimSpace(warehouse.Key))
				if restrictions[warehouseKey] {
					warehouse.Selectable = false
					warehouse.Recommended = false
					warehouse.ShopSKUDisabled = true
					warehouse.ReasonCode = "SHOP_SKU_WAREHOUSE_DISABLED"
					warehouse.Reason = fmt.Sprintf("当前店铺已禁止 SKU %s 使用此仓库", record.SKU)
				}
				if warehouseKey == strings.ToUpper(region.RecommendedWarehouseKey) && warehouse.Selectable {
					recommendedSelectable = true
				}
			}
			if recommendedSelectable {
				continue
			}
			region.RecommendedWarehouseKey = ""
			for warehouseIndex := range region.Warehouses {
				region.Warehouses[warehouseIndex].Recommended = false
			}
			for warehouseIndex := range region.Warehouses {
				if region.Warehouses[warehouseIndex].Selectable {
					region.Warehouses[warehouseIndex].Recommended = true
					region.RecommendedWarehouseKey = region.Warehouses[warehouseIndex].Key
					break
				}
			}
			if region.RecommendedWarehouseKey == "" {
				region.DecisionCode = "SHOP_SKU_WAREHOUSE_DISABLED"
				region.Reason = "当前店铺的 SKU 发货仓库规则未保留可选仓库"
			}
		}
	}
}

func decisionHasShopSKUWarehouseRestrictions(decision inventory.DecisionResponse) bool {
	for _, record := range decision.Records {
		for _, region := range record.Regions {
			for _, warehouse := range region.Warehouses {
				if warehouse.ShopSKUDisabled {
					return true
				}
			}
		}
	}
	return false
}

func (s *Service) validateOrderWarehouseAllowed(ctx context.Context, order model.Order, warehouseKey string) error {
	warehouseSKUs := make([]string, 0, len(order.Lines))
	for _, line := range order.Lines {
		warehouseSKUs = append(warehouseSKUs, line.ExtCode)
	}
	disabled, err := s.store.DisabledWarehouseKeysForSKUs(ctx, warehouseSKUs)
	if err != nil {
		return err
	}
	return validateOrderWarehouseRestrictions(order, warehouseKey, disabled)
}

func validateOrderWarehouseRestrictions(order model.Order, warehouseKey string, disabled map[string]map[string]bool) error {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	for _, line := range order.Lines {
		warehouseSKU := strings.TrimSpace(line.ExtCode)
		if disabled[warehouseSKU][warehouseKey] {
			return fmt.Errorf("当前店铺已禁止 SKU %s 从仓库 %s 购买面单，请重新选择仓库", warehouseSKU, warehouseKey)
		}
	}
	return nil
}

func (s *Service) UpdateWarehouseSKUPackageSpec(ctx context.Context, warehouseSKU string, update inventory.PackageSpecUpdate) (inventory.WarehouseSKUSpec, error) {
	return s.inventory.UpdatePackageSpec(ctx, warehouseSKU, update)
}

type WarehouseMappingPreview struct {
	Ready         bool   `json:"ready"`
	WarehouseID   string `json:"warehouse_id,omitempty"`
	WarehouseName string `json:"warehouse_name,omitempty"`
}

type WarehouseRegionPreview struct {
	Region        string                  `json:"region"`
	RegionName    string                  `json:"region_name"`
	Ready         bool                    `json:"ready"`
	Recommended   bool                    `json:"recommended"`
	WarehouseKey  string                  `json:"warehouse_key,omitempty"`
	WarehouseName string                  `json:"warehouse_name,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
	Error         string                  `json:"error,omitempty"`
	Mapping       WarehouseMappingPreview `json:"mapping"`
}

type WarehousePreview struct {
	ParentOrderSN    string                     `json:"parent_order_sn"`
	Quantities       map[string]int             `json:"quantities"`
	Decision         inventory.DecisionResponse `json:"decision"`
	RequiresManual   bool                       `json:"requires_manual"`
	ManualReasons    []string                   `json:"manual_reasons"`
	ManualCategories []string                   `json:"manual_categories"`
	MappingRequired  bool                       `json:"mapping_required"`
	Ready            bool                       `json:"ready"`
	InventoryError   string                     `json:"inventory_error,omitempty"`
	Regions          []WarehouseRegionPreview   `json:"regions"`
}

const (
	manualReasonInventoryRule      = "inventory_rule"
	manualReasonWarehouseSKUSpec   = "warehouse_sku_spec_incomplete"
	manualReasonDeliveryAddress    = "delivery_address_unsupported"
	manualReasonSKUWarehousePolicy = "shop_sku_warehouse_restriction"
)

func (s *Service) PreviewWarehouses(ctx context.Context, parent string) (WarehousePreview, error) {
	return s.previewWarehouses(ctx, parent, "")
}

func (s *Service) PreviewFailedShipmentRecovery(ctx context.Context, shipmentID string) (WarehousePreview, error) {
	shipment, err := s.store.GetShipment(ctx, strings.TrimSpace(shipmentID))
	if err != nil {
		return WarehousePreview{}, err
	}
	if err := validateFailedShipmentRecovery(shipment); err != nil {
		return WarehousePreview{}, err
	}
	return s.previewWarehouses(ctx, shipment.ParentOrderSN, shipment.ID)
}

func (s *Service) previewWarehouses(ctx context.Context, parent, recoveryShipmentID string) (WarehousePreview, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return WarehousePreview{}, errors.New("parent order number is required")
	}
	order, err := s.store.GetOrder(ctx, parent)
	if err != nil {
		return WarehousePreview{}, err
	}
	if !order.Open || order.Status != 2 {
		return WarehousePreview{}, errOrderNoLongerAwaitingShipment
	}
	if existing, lookupErr := s.store.ShipmentForOrder(ctx, order.ParentOrderSN); lookupErr == nil {
		if recoveryShipmentID != "" && existing.ID == recoveryShipmentID {
			if err := validateFailedShipmentRecovery(existing); err != nil {
				return WarehousePreview{}, err
			}
		} else if !shipmentRetryable(existing) {
			return WarehousePreview{}, fmt.Errorf("order already has shipment %s with status %s", existing.ID, existing.Status)
		}
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return WarehousePreview{}, lookupErr
	}
	if reason := manualOrderReason(order); reason != "" && !warehouseManualReviewCanBeRechecked(order) {
		return WarehousePreview{}, errors.New(reason)
	}
	quantities := make(map[string]int)
	for _, line := range order.Lines {
		if strings.TrimSpace(line.ExtCode) == "" {
			return WarehousePreview{}, fmt.Errorf("order line %s has no extCode and requires manual SKU mapping", line.OrderSN)
		}
		quantities[line.ExtCode] += line.Quantity
	}
	decision, queryErr := s.queryInventory(ctx, quantities)
	if queryErr == nil {
		if err := s.applyShopSKUWarehouseRules(ctx, &decision); err != nil {
			return WarehousePreview{}, err
		}
	}
	preview := WarehousePreview{
		ParentOrderSN: order.ParentOrderSN, Quantities: quantities, Decision: decision,
		ManualReasons: make([]string, 0), ManualCategories: make([]string, 0), Regions: make([]WarehouseRegionPreview, 0, 4),
	}
	classification := warehouseClassificationFromDecision(order, decision, queryErr)
	if err := s.persistWarehouseClassification(ctx, order, classification); err != nil {
		return WarehousePreview{}, err
	}
	if queryErr != nil {
		preview.InventoryError = queryErr.Error()
		return preview, nil
	}
	preview.RequiresManual = classification.Status == "manual"
	preview.ManualCategories = append(preview.ManualCategories, classification.Categories...)
	preview.ManualReasons = append(preview.ManualReasons, classification.ReasonDetails...)
	for _, region := range []string{"east", "west"} {
		regionName := map[string]string{"east": "美东", "west": "美西"}[region]
		keys := []string{"DPS002", "ARP_EAST"}
		if region == "west" {
			keys = []string{"DPS004", "ARP_WEST"}
		}
		defaultSelection, defaultErr := inventory.SelectWarehouse(decision, region, quantities)
		for _, key := range keys {
			option := WarehouseRegionPreview{Region: region, RegionName: regionName, WarehouseKey: key, WarehouseName: key}
			selection, selectErr := inventory.SelectWarehouse(decision, region, quantities, key)
			if selectErr != nil {
				option.Error = selectErr.Error()
				preview.Regions = append(preview.Regions, option)
				continue
			}
			option.WarehouseName = selection.WarehouseName
			option.Reason = selection.Reason
			option.Recommended = defaultErr == nil && defaultSelection.WarehouseKey == key
			if reason := disabledOMSWarehouseReason(key); reason != "" {
				option.Error = reason
				preview.Regions = append(preview.Regions, option)
				continue
			}
			temuWarehouse, mappingErr := s.store.MappedWarehouse(ctx, key)
			switch {
			case errors.Is(mappingErr, pgx.ErrNoRows):
				preview.MappingRequired = true
				option.Error = "领星仓尚未映射到 Temu Buy Label 仓"
			case mappingErr != nil:
				return WarehousePreview{}, mappingErr
			case !temuWarehouse.EnableBuyShippingLabel:
				preview.MappingRequired = true
				option.Error = "映射的 Temu 仓不支持 Buy Label"
			default:
				option.Mapping = WarehouseMappingPreview{Ready: true, WarehouseID: temuWarehouse.ID, WarehouseName: temuWarehouse.Name}
				option.Ready = true
				preview.Ready = true
			}
			preview.Regions = append(preview.Regions, option)
		}
	}
	if classification.Status != "eligible" {
		preview.Ready = false
	}
	return preview, nil
}

type QuoteRequest struct {
	ParentOrderSN      string   `json:"parent_order_sn"`
	Region             string   `json:"region"`
	WarehouseKey       string   `json:"warehouse_key"`
	PreferredChannelID int64    `json:"preferred_channel_id,omitempty"`
	ExcludedCarriers   []string `json:"-"`
	RecoveryShipmentID string   `json:"-"`
}

type QuoteResult struct {
	Quote               model.Quote                 `json:"quote"`
	WarehouseSelection  inventory.Selection         `json:"warehouse_selection"`
	TemuWarehouse       model.Warehouse             `json:"temu_warehouse"`
	Package             model.PackageSpec           `json:"package"`
	PackageResolution   inventory.PackageResolution `json:"package_resolution"`
	AvailableChannels   []temu.ShippingChannel      `json:"available_channels"`
	UnavailableChannels []temu.ShippingChannel      `json:"unavailable_channels"`
}

type storedQuoteRequest struct {
	Package            model.PackageSpec         `json:"package"`
	ShippingRequest    map[string]any            `json:"shipping_request"`
	SelectedChannel    temu.ShippingChannel      `json:"selected_channel"`
	RecoveryShipmentID string                    `json:"recovery_shipment_id,omitempty"`
	ChoiceAnalysis     model.LabelPurchaseChoice `json:"choice_analysis,omitempty"`
}

func (s *Service) Quote(ctx context.Context, request QuoteRequest) (QuoteResult, error) {
	request.ParentOrderSN = strings.TrimSpace(request.ParentOrderSN)
	request.Region = strings.ToLower(strings.TrimSpace(request.Region))
	request.WarehouseKey = strings.ToUpper(strings.TrimSpace(request.WarehouseKey))
	if request.ParentOrderSN == "" {
		return QuoteResult{}, errors.New("parent_order_sn is required")
	}
	order, err := s.store.GetOrder(ctx, request.ParentOrderSN)
	if err != nil {
		return QuoteResult{}, err
	}
	if !order.Open || order.Status != 2 {
		return QuoteResult{}, errOrderNoLongerAwaitingShipment
	}
	if existing, lookupErr := s.store.ShipmentForOrder(ctx, order.ParentOrderSN); lookupErr == nil {
		if request.RecoveryShipmentID != "" {
			if existing.ID != request.RecoveryShipmentID {
				return QuoteResult{}, errors.New("recovery shipment does not belong to this order")
			}
			if err := validateFailedShipmentRecovery(existing); err != nil {
				return QuoteResult{}, err
			}
			request.ExcludedCarriers = append(request.ExcludedCarriers, recoveryExcludedCarrierCodes(existing)...)
		} else if !shipmentRetryable(existing) {
			return QuoteResult{}, fmt.Errorf("order already has shipment %s with status %s", existing.ID, existing.Status)
		} else {
			request.ExcludedCarriers = append(request.ExcludedCarriers, existing.FailedCarrierCodes...)
			if deliveryAddressUnsupported(existing.ErrorMessage) {
				request.ExcludedCarriers = append(request.ExcludedCarriers, carrierCode(temu.ShippingChannel{
					ShippingCompanyName: existing.ShippingCompanyName,
					ShipLogisticsType:   existing.ShipLogisticsType,
				}))
			}
		}
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return QuoteResult{}, lookupErr
	}
	if reason := manualOrderReason(order); reason != "" {
		return QuoteResult{}, errors.New(reason)
	}
	quantities := make(map[string]int)
	for _, line := range order.Lines {
		if strings.TrimSpace(line.ExtCode) == "" {
			return QuoteResult{}, fmt.Errorf("order line %s has no extCode and requires manual SKU mapping", line.OrderSN)
		}
		quantities[line.ExtCode] += line.Quantity
	}
	inventoryStarted := time.Now()
	decision, err := s.queryInventory(ctx, quantities)
	if err != nil {
		return QuoteResult{}, err
	}
	if err := s.applyShopSKUWarehouseRules(ctx, &decision); err != nil {
		return QuoteResult{}, err
	}
	s.logger.Info("Shipping quote inventory query completed", "parent_order_sn", order.ParentOrderSN, "duration", time.Since(inventoryStarted).String())
	packageSpec, err := packageSpecFromResolution(decision.PackageResolution)
	if err != nil {
		return QuoteResult{}, err
	}

	warehouseKeys, err := quoteWarehouseKeys(request.Region, request.WarehouseKey)
	if err != nil {
		return QuoteResult{}, err
	}
	jobs := make([]warehouseQuoteResult, 0, len(warehouseKeys))
	problems := make([]error, 0)
	for _, key := range warehouseKeys {
		selectionRegion := request.Region
		if selectionRegion == "auto" || selectionRegion == "" {
			selectionRegion = warehouseRegion(key)
		}
		selected, selectErr := inventory.SelectWarehouse(decision, selectionRegion, quantities, key)
		if selectErr != nil {
			problems = append(problems, fmt.Errorf("%s: %w", key, selectErr))
			continue
		}
		if reason := disabledOMSWarehouseReason(selected.WarehouseKey); reason != "" {
			problems = append(problems, errors.New(reason))
			continue
		}
		mapped, mapErr := s.store.MappedWarehouse(ctx, selected.WarehouseKey)
		if errors.Is(mapErr, pgx.ErrNoRows) {
			problems = append(problems, fmt.Errorf("warehouse mapping required for %s", selected.WarehouseKey))
			continue
		}
		if mapErr != nil {
			return QuoteResult{}, mapErr
		}
		if !mapped.EnableBuyShippingLabel {
			problems = append(problems, fmt.Errorf("Temu warehouse %s does not support Buy Label", mapped.Name))
			continue
		}
		jobs = append(jobs, warehouseQuoteResult{
			selection: selected, warehouse: mapped,
			shippingRequest: shippingServicesRequest(order, mapped.ID, packageSpec),
		})
	}
	if len(jobs) == 0 {
		return QuoteResult{}, fmt.Errorf("no eligible warehouse can be quoted: %w", errors.Join(problems...))
	}
	warehousePolicies, err := s.carrierPoliciesByWarehouse(ctx)
	if err != nil {
		return QuoteResult{}, err
	}

	results := make(chan warehouseQuoteResult, len(jobs))
	var quoteWG sync.WaitGroup
	for _, job := range jobs {
		quoteWG.Add(1)
		go func(item warehouseQuoteResult) {
			defer quoteWG.Done()
			item.channels, item.raw, item.err = s.temu.ShippingServices(ctx, item.shippingRequest)
			results <- item
		}(job)
	}
	quoteWG.Wait()
	close(results)

	quoted := make([]warehouseQuoteResult, 0, len(jobs))
	candidates := make([]autoChannelCandidate, 0)
	excludedCarriers := make(map[string]bool, len(request.ExcludedCarriers))
	for _, carrier := range request.ExcludedCarriers {
		excludedCarriers[strings.ToUpper(strings.TrimSpace(carrier))] = true
	}
	for result := range results {
		if result.err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", result.selection.WarehouseKey, result.err))
			continue
		}
		allowed, rejected := filterAutomaticChannels(result.channels.Available)
		allowed, policyRejected := filterChannelsByCarrierPolicy(allowed, result.selection.WarehouseKey, warehousePolicies[result.selection.WarehouseKey])
		rejected = append(rejected, policyRejected...)
		result.channels.Available = allowed
		result.channels.Unavailable = append(result.channels.Unavailable, rejected...)
		if len(allowed) == 0 {
			problems = append(problems, fmt.Errorf("%s: no whitelist channel without signature service", result.selection.WarehouseKey))
			continue
		}
		eligible := make([]temu.ShippingChannel, 0, len(allowed))
		for _, channel := range allowed {
			if excludedCarriers[carrierCode(channel)] {
				channel.UnavailableReason = "该承运商此前购单失败，本次恢复已自动排除"
				result.channels.Unavailable = append(result.channels.Unavailable, channel)
				continue
			}
			eligible = append(eligible, channel)
		}
		result.channels.Available = eligible
		quoted = append(quoted, result)
		index := len(quoted) - 1
		for _, channel := range eligible {
			candidates = append(candidates, autoChannelCandidate{
				warehouseIndex:  index,
				warehouseKey:    result.selection.WarehouseKey,
				temuWarehouseID: result.warehouse.ID,
				channel:         channel,
				amount:          price(channel.EstimatedAmount),
				priority:        configuredCarrierPriority(warehousePolicies[result.selection.WarehouseKey], carrierCode(channel)),
			})
		}
	}
	if len(candidates) == 0 {
		return QuoteResult{}, fmt.Errorf("no automatic shipping option is available: %w", errors.Join(problems...))
	}
	choice, reason, err := selectAutomaticChannel(candidates, request.PreferredChannelID)
	if err != nil {
		return QuoteResult{}, err
	}
	selectedResult := quoted[choice.warehouseIndex]
	selectedWarehouse := selectedResult.selection
	if request.WarehouseKey == "" {
		if hasEqualPriceARPChoice(candidates, choice) && isDPSWarehouse(choice.warehouseKey) {
			reason += "；跨仓实时运费完全相同时优先 DPS 清货"
		} else {
			reason += "；仓库选择以实时运费优先于 DPS 清货"
		}
	}
	choiceAnalysis := buildLabelPurchaseChoice(candidates, choice, reason, request.PreferredChannelID != 0)
	selectedWarehouse.Reason = reason
	for i := range selectedResult.channels.Available {
		if selectedResult.channels.Available[i].ChannelID == choice.channel.ChannelID {
			selectedResult.channels.Available[i].Selected = true
			selectedResult.channels.Available[i].SelectionReason = reason
		}
	}
	requestRecord, _ := json.Marshal(storedQuoteRequest{
		Package: packageSpec, ShippingRequest: selectedResult.shippingRequest, SelectedChannel: choice.channel,
		RecoveryShipmentID: request.RecoveryShipmentID, ChoiceAnalysis: choiceAnalysis,
	})
	responseRecord, _ := json.Marshal(map[string]any{"temu_raw": json.RawMessage(selectedResult.raw), "available": selectedResult.channels.Available, "unavailable": selectedResult.channels.Unavailable})
	selectedRegion := request.Region
	if selectedRegion == "auto" || selectedRegion == "" {
		selectedRegion = warehouseRegion(selectedWarehouse.WarehouseKey)
	}
	quote := model.Quote{ID: newID("q"), ParentOrderSN: order.ParentOrderSN,
		OMSWarehouseKey: selectedWarehouse.WarehouseKey, TemuWarehouseID: selectedResult.warehouse.ID,
		Region: selectedRegion, ChannelID: choice.channel.ChannelID, ShipCompanyID: choice.channel.ShipCompanyID,
		ShippingCompanyName: choice.channel.ShippingCompanyName, ShipLogisticsType: choice.channel.ShipLogisticsType,
		SelectionReason: reason, RequestPayload: requestRecord, ResponsePayload: responseRecord,
		ExpiresAt: time.Now().Add(s.quoteLifetime)}
	if err := s.store.SaveQuote(ctx, quote); err != nil {
		return QuoteResult{}, err
	}
	return QuoteResult{Quote: quote, WarehouseSelection: selectedWarehouse, TemuWarehouse: selectedResult.warehouse,
		Package: packageSpec, PackageResolution: decision.PackageResolution,
		AvailableChannels: selectedResult.channels.Available, UnavailableChannels: selectedResult.channels.Unavailable}, nil
}

type warehouseQuoteResult struct {
	selection       inventory.Selection
	warehouse       model.Warehouse
	shippingRequest map[string]any
	channels        temu.ShippingServicesResult
	raw             json.RawMessage
	err             error
}

type autoChannelCandidate struct {
	warehouseIndex  int
	warehouseKey    string
	temuWarehouseID string
	channel         temu.ShippingChannel
	amount          float64
	priority        int
}

var supportedAutomaticCarrierCodes = []string{"GOFO", "SWIFTX", "SPEEDX", "YANWEN", "UPS", "USPS", "FEDEX"}

var automaticCarrierWhitelist = map[string]bool{
	"GOFO": true, "SPEEDX": true, "SWIFTX": true, "YANWEN": true,
	"UPS": true, "USPS": true, "FEDEX": true,
}

func quoteWarehouseKeys(region, preferred string) ([]string, error) {
	if preferred != "" {
		return []string{preferred}, nil
	}
	switch region {
	case "east":
		return []string{"DPS002", "ARP_EAST"}, nil
	case "west":
		return []string{"DPS004", "ARP_WEST"}, nil
	case "auto", "":
		return []string{"DPS002", "ARP_EAST", "DPS004", "ARP_WEST"}, nil
	default:
		return nil, errors.New("region must be east, west, or auto")
	}
}
func warehouseRegion(key string) string {
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "DPS002", "ARP_EAST":
		return "east"
	case "DPS004", "ARP_WEST":
		return "west"
	default:
		return ""
	}
}

func carrierCode(channel temu.ShippingChannel) string {
	text := strings.ToUpper(channel.ShippingCompanyName + " " + channel.ShipLogisticsType)
	normalized := strings.NewReplacer(" ", "", "-", "", "_", "", "/", "").Replace(text)
	for _, code := range []string{"UNIUNI", "SWIFTX", "SPEEDX", "YANWEN", "FEDEX", "USPS", "UPS", "GOFO"} {
		if strings.Contains(normalized, code) {
			return code
		}
	}
	return ""
}

func channelNeedsSignature(channel temu.ShippingChannel) bool {
	for _, field := range channel.InfoNeeded {
		if strings.EqualFold(strings.TrimSpace(field), "signServiceId") {
			return true
		}
	}
	return false
}

func filterAutomaticChannels(channels []temu.ShippingChannel) ([]temu.ShippingChannel, []temu.ShippingChannel) {
	allowed := make([]temu.ShippingChannel, 0, len(channels))
	rejected := make([]temu.ShippingChannel, 0)
	for _, channel := range channels {
		code := carrierCode(channel)
		reason := ""
		switch {
		case code == "UNIUNI":
			reason = "UNIUNI 已禁用"
		case !automaticCarrierWhitelist[code]:
			reason = "不在自动发货物流白名单"
		case channelNeedsSignature(channel):
			reason = "当前一单一件自动发货强制不使用签名服务"
		case math.IsInf(price(channel.EstimatedAmount), 1):
			reason = "缺少可比较的实时运费"
		case channel.EstimatedCurrencyCode != "" && !strings.EqualFold(channel.EstimatedCurrencyCode, "USD"):
			reason = "实时运费不是 USD，不能应用 0.50 美元规则"
		}
		if reason != "" {
			channel.UnavailableReason = reason
			rejected = append(rejected, channel)
			continue
		}
		allowed = append(allowed, channel)
	}
	return allowed, rejected
}

func filterChannelsByCarrierPolicy(channels []temu.ShippingChannel, warehouseKey string, policies []model.CarrierPolicy) ([]temu.ShippingChannel, []temu.ShippingChannel) {
	byCode := make(map[string]model.CarrierPolicy, len(policies))
	for _, policy := range policies {
		byCode[policy.CarrierCode] = policy
	}
	allowed := make([]temu.ShippingChannel, 0, len(channels))
	rejected := make([]temu.ShippingChannel, 0)
	for _, channel := range channels {
		policy, configured := byCode[carrierCode(channel)]
		if configured && !policy.Enabled {
			channel.UnavailableReason = fmt.Sprintf("%s 店铺策略已在 %s 仓库禁用", policy.CarrierCode, warehouseKey)
			rejected = append(rejected, channel)
			continue
		}
		allowed = append(allowed, channel)
	}
	return allowed, rejected
}

func configuredCarrierPriority(policies []model.CarrierPolicy, code string) int {
	for _, policy := range policies {
		if policy.CarrierCode == code {
			return policy.Priority
		}
	}
	return len(supportedAutomaticCarrierCodes) + 1
}

func selectAutomaticChannel(candidates []autoChannelCandidate, preferredChannelID int64) (autoChannelCandidate, string, error) {
	if len(candidates) == 0 {
		return autoChannelCandidate{}, "", errors.New("Temu returned no allowed shipping channel")
	}
	items := append([]autoChannelCandidate(nil), candidates...)
	sort.SliceStable(items, func(i, j int) bool { return betterChannelCandidate(items[i], items[j]) })
	if preferredChannelID != 0 {
		manual := make([]autoChannelCandidate, 0)
		for _, item := range items {
			if item.channel.ChannelID == preferredChannelID {
				manual = append(manual, item)
			}
		}
		if len(manual) == 0 {
			return autoChannelCandidate{}, "", errors.New("preferred channel is not currently allowed")
		}
		sort.SliceStable(manual, func(i, j int) bool { return betterChannelCandidate(manual[i], manual[j]) })
		return manual[0], "人工选择了当前白名单内且无需签名的物流渠道", nil
	}
	minimum := items[0].amount
	withinRange := make([]autoChannelCandidate, 0, len(items))
	for _, item := range items {
		if item.amount <= minimum+0.500001 {
			withinRange = append(withinRange, item)
		}
	}
	sort.SliceStable(withinRange, func(i, j int) bool {
		leftPriority, rightPriority := effectiveCarrierPriority(withinRange[i]), effectiveCarrierPriority(withinRange[j])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return betterChannelCandidate(withinRange[i], withinRange[j])
	})
	choice := withinRange[0]
	return choice, fmt.Sprintf("%s 是 %s 的第 %d 优先快递，运费距最低价不超过 $0.50", carrierCode(choice.channel), choice.warehouseKey, effectiveCarrierPriority(choice)), nil
}

func effectiveCarrierPriority(candidate autoChannelCandidate) int {
	if candidate.priority > 0 {
		return candidate.priority
	}
	return len(supportedAutomaticCarrierCodes) + 1
}

func betterChannelCandidate(left, right autoChannelCandidate) bool {
	if math.Abs(left.amount-right.amount) > 0.000001 {
		return left.amount < right.amount
	}
	leftDPS, rightDPS := isDPSWarehouse(left.warehouseKey), isDPSWarehouse(right.warehouseKey)
	if leftDPS != rightDPS {
		return leftDPS
	}
	return left.channel.ChannelID < right.channel.ChannelID
}

func isDPSWarehouse(key string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(key)), "DPS")
}

func hasEqualPriceARPChoice(candidates []autoChannelCandidate, selected autoChannelCandidate) bool {
	for _, item := range candidates {
		if !isDPSWarehouse(item.warehouseKey) && math.Abs(item.amount-selected.amount) <= 0.000001 {
			return true
		}
	}
	return false
}
func buildLabelPurchaseChoice(candidates []autoChannelCandidate, selected autoChannelCandidate, reason string, manual bool) model.LabelPurchaseChoice {
	selectionSource := "automatic"
	if manual {
		selectionSource = "manual"
	}
	result := model.LabelPurchaseChoice{
		SelectionSource: selectionSource,
		SelectionReason: reason,
		Selected:        labelPurchaseCandidate(selected, 0),
	}
	items := append([]autoChannelCandidate(nil), candidates...)
	sort.SliceStable(items, func(i, j int) bool { return betterChannelCandidate(items[i], items[j]) })
	if len(items) > 3 {
		items = items[:3]
	}
	result.TopCandidates = make([]model.LabelPurchaseCandidate, 0, len(items))
	for index, item := range items {
		candidate := labelPurchaseCandidate(item, index+1)
		result.TopCandidates = append(result.TopCandidates, candidate)
		if sameChannelCandidate(item, selected) {
			result.Selected.PriceRank = candidate.PriceRank
		}
	}
	return result
}

func labelPurchaseCandidate(item autoChannelCandidate, rank int) model.LabelPurchaseCandidate {
	return model.LabelPurchaseCandidate{
		PriceRank: rank, OMSWarehouseKey: item.warehouseKey, TemuWarehouseID: item.temuWarehouseID,
		ChannelID: item.channel.ChannelID, ShipCompanyID: item.channel.ShipCompanyID,
		CarrierCode: carrierCode(item.channel), ShippingCompanyName: item.channel.ShippingCompanyName,
		ShipLogisticsType: item.channel.ShipLogisticsType, EstimatedAmount: fmt.Sprintf("%.4f", item.amount),
		EstimatedCurrencyCode: strings.ToUpper(strings.TrimSpace(item.channel.EstimatedCurrencyCode)),
	}
}

func sameChannelCandidate(left, right autoChannelCandidate) bool {
	return left.warehouseKey == right.warehouseKey &&
		left.channel.ChannelID == right.channel.ChannelID &&
		left.channel.ShipCompanyID == right.channel.ShipCompanyID
}

func shipmentRetryable(shipment model.Shipment) bool {
	return len(shipment.PackageSNList) == 0 && (shipment.Status == "submission_unknown" || shipment.Status == "label_failed")
}

func validateFailedShipmentRecovery(shipment model.Shipment) error {
	if shipment.Status != "label_failed" {
		return errors.New("only a failed label purchase can be reselected and resubmitted")
	}
	if strings.TrimSpace(shipment.TrackingNumber) != "" || shipment.ConfirmedAt != nil {
		return errors.New("shipment already has tracking or confirmation evidence and cannot be resubmitted")
	}
	if len(shipment.PackageSNList) > 0 {
		return errors.New("Temu already created a failed integrated-logistics package; switch the order to manual shipping in Temu before purchasing another label")
	}
	return nil
}

func recoveryExcludedCarrierCodes(shipment model.Shipment) []string {
	codes := append([]string(nil), shipment.FailedCarrierCodes...)
	current := carrierCode(temu.ShippingChannel{
		ShippingCompanyName: shipment.ShippingCompanyName,
		ShipLogisticsType:   shipment.ShipLogisticsType,
	})
	if current != "" {
		codes = append(codes, current)
	}
	return codes
}

func deliveryAddressUnsupported(message string) bool {
	return strings.Contains(strings.ToLower(message), "delivery address is not supported")
}

func automaticCarrierFallbackAllowed(shipment model.Shipment) bool {
	return len(shipment.PackageSNList) == 0 &&
		deliveryAddressUnsupported(shipment.ErrorMessage) &&
		shipment.SubmissionAttempts < maxShipmentSubmissionAttempts &&
		validateFailedShipmentRecovery(shipment) == nil
}

type PurchaseResult struct {
	Shipment  model.Shipment `json:"shipment"`
	Duplicate bool           `json:"duplicate"`
}

func (s *Service) Purchase(ctx context.Context, quoteID string) (PurchaseResult, error) {
	quote, err := s.store.GetQuote(ctx, strings.TrimSpace(quoteID))
	if err != nil {
		return PurchaseResult{}, err
	}
	if time.Now().After(quote.ExpiresAt) {
		return PurchaseResult{}, errors.New("shipping quote has expired; request a new quote")
	}
	order, err := s.store.GetOrder(ctx, quote.ParentOrderSN)
	if err != nil {
		return PurchaseResult{}, err
	}
	if !order.Open || order.Status != 2 {
		return PurchaseResult{}, errOrderNoLongerAwaitingShipment
	}
	if err := s.validateOrderWarehouseAllowed(ctx, order, quote.OMSWarehouseKey); err != nil {
		return PurchaseResult{}, err
	}
	var saved storedQuoteRequest
	if err := json.Unmarshal(quote.RequestPayload, &saved); err != nil {
		return PurchaseResult{}, errors.New("stored quote request is invalid")
	}
	request, err := shipmentCreateRequest(order, quote, saved.Package, saved.SelectedChannel)
	if err != nil {
		return PurchaseResult{}, err
	}
	requestRaw, _ := json.Marshal(request)
	shipment := model.Shipment{ID: newID("s"), QuoteID: quote.ID,
		IdempotencyKey: "buy-label:v1:" + order.ParentOrderSN, SelectionMode: "exact_channel",
		WarehouseID: quote.TemuWarehouseID, ChannelID: quote.ChannelID, ShipCompanyID: quote.ShipCompanyID,
		ShippingCompanyName: quote.ShippingCompanyName, ShipLogisticsType: quote.ShipLogisticsType,
		RequestPayload: requestRaw, ParentOrderSN: order.ParentOrderSN}
	reserved, duplicate, err := s.store.ReserveShipment(ctx, shipment, saved.ChoiceAnalysis)
	if err != nil {
		return PurchaseResult{}, err
	}
	if duplicate {
		if !shipmentRetryable(reserved) {
			return PurchaseResult{Shipment: reserved, Duplicate: true}, nil
		}
		detail, _, lookupErr := s.temu.OrderDetail(ctx, reserved.ParentOrderSN)
		if lookupErr != nil {
			return PurchaseResult{}, fmt.Errorf("重试购单前核验 Temu 包裹失败: %w", lookupErr)
		}
		if packageSNs := packageSNsFromOrderDetail(detail); len(packageSNs) > 0 {
			evidence, _ := json.Marshal(map[string]any{"source": "order_detail", "packageSnList": packageSNs})
			if err := s.store.UpdateShipmentSubmission(ctx, reserved.ID, "label_pending", packageSNs, evidence, "", ""); err != nil {
				return PurchaseResult{}, err
			}
			updated, err := s.store.GetShipment(ctx, reserved.ID)
			return PurchaseResult{Shipment: updated, Duplicate: true}, err
		}
		if err := s.store.PrepareShipmentRetry(ctx, reserved.ID, shipment, saved.ChoiceAnalysis); err != nil {
			return PurchaseResult{}, errors.New("发货记录状态已变化，请刷新后重试")
		}
		reserved, err = s.store.GetShipment(ctx, reserved.ID)
		if err != nil {
			return PurchaseResult{}, err
		}
	}
	updated, callErr := s.submitReservedShipment(ctx, reserved, request)
	return PurchaseResult{Shipment: updated, Duplicate: duplicate}, callErr
}

func (s *Service) submitReservedShipment(ctx context.Context, reserved model.Shipment, request map[string]any) (model.Shipment, error) {
	result, raw, callErr := s.temu.CreateShipment(ctx, request)
	if callErr != nil {
		if temuAlreadyShipped(callErr) || temuShipmentRequestAlreadyExists(callErr) {
			detail, _, lookupErr := s.temu.OrderDetail(ctx, reserved.ParentOrderSN)
			if lookupErr == nil {
				if packageSNs := packageSNsFromOrderDetail(detail); len(packageSNs) > 0 {
					evidence, _ := json.Marshal(map[string]any{
						"source": "order_detail_after_existing_shipment_request", "packageSnList": packageSNs,
					})
					if err := s.store.UpdateShipmentSubmission(ctx, reserved.ID, "label_pending", packageSNs, evidence, "", ""); err != nil {
						return reserved, err
					}
					recovered, refreshErr := s.RefreshShipment(ctx, reserved.ID)
					if refreshErr != nil {
						return recovered, fmt.Errorf("Temu 已有物流请求，恢复已有包裹结果失败: %w", refreshErr)
					}
					return recovered, nil
				}
			}
		}
		status := "label_failed"
		var apiErr *temu.APIError
		if !errors.As(callErr, &apiErr) || apiErr.Status == 0 || apiErr.Temporary || temuAlreadyShipped(callErr) || temuShipmentRequestAlreadyExists(callErr) {
			status = "submission_unknown"
		}
		code, message := apiErrorParts(callErr)
		_ = s.store.UpdateShipmentSubmission(context.WithoutCancel(ctx), reserved.ID, status, nil, raw, code, message)
		if status == "label_failed" && deliveryAddressUnsupported(message) {
			_ = s.store.RecordShipmentCarrierFailure(context.WithoutCancel(ctx), reserved.ID, carrierCode(temu.ShippingChannel{
				ShippingCompanyName: reserved.ShippingCompanyName,
				ShipLogisticsType:   reserved.ShipLogisticsType,
			}))
		}
		updated, _ := s.store.GetShipment(context.WithoutCancel(ctx), reserved.ID)
		return updated, callErr
	}
	status := "label_pending"
	if len(result.PackageSNList) == 0 {
		status = "submission_unknown"
	}
	if err := s.store.UpdateShipmentSubmission(ctx, reserved.ID, status, result.PackageSNList, raw, "", ""); err != nil {
		return model.Shipment{}, err
	}
	updated, err := s.store.GetShipment(ctx, reserved.ID)
	return updated, err
}

func (s *Service) PurchaseAndQueueCompletion(ctx context.Context, quoteID string) (PurchaseResult, error) {
	result, purchaseErr := s.Purchase(ctx, quoteID)
	if result.Shipment.ID == "" {
		return result, purchaseErr
	}
	queueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if queueErr := s.store.EnsureShipmentCompletionJob(queueCtx, result.Shipment.ID, result.Shipment.Status); queueErr != nil {
		wrapped := fmt.Errorf("面单已记账，但自动确认任务关联失败，请勿重复购买: %w", queueErr)
		return result, errors.Join(purchaseErr, wrapped)
	}
	return result, purchaseErr
}

func (s *Service) QuoteFailedShipmentRecovery(ctx context.Context, shipmentID string, request QuoteRequest) (QuoteResult, error) {
	shipment, err := s.store.GetShipment(ctx, strings.TrimSpace(shipmentID))
	if err != nil {
		return QuoteResult{}, err
	}
	if err := validateFailedShipmentRecovery(shipment); err != nil {
		return QuoteResult{}, err
	}
	request.ParentOrderSN = shipment.ParentOrderSN
	request.RecoveryShipmentID = shipment.ID
	return s.Quote(ctx, request)
}

func (s *Service) RecoverFailedShipment(ctx context.Context, shipmentID, quoteID string) (PurchaseResult, error) {
	shipment, err := s.store.GetShipment(ctx, strings.TrimSpace(shipmentID))
	if err != nil {
		return PurchaseResult{}, err
	}
	if err := validateFailedShipmentRecovery(shipment); err != nil {
		return PurchaseResult{}, err
	}
	if len(shipment.PackageSNList) > 0 {
		shipment, err = s.RefreshShipment(ctx, shipment.ID)
		if err != nil {
			return PurchaseResult{}, fmt.Errorf("failed to verify the existing Temu package before recovery: %w", err)
		}
		if err := validateFailedShipmentRecovery(shipment); err != nil {
			return PurchaseResult{}, fmt.Errorf("existing Temu package status changed; recovery stopped: %w", err)
		}
	} else {
		detail, _, lookupErr := s.temu.OrderDetail(ctx, shipment.ParentOrderSN)
		if lookupErr != nil {
			return PurchaseResult{}, fmt.Errorf("failed to verify Temu order detail before recovery: %w", lookupErr)
		}
		if packageSNs := packageSNsFromOrderDetail(detail); len(packageSNs) > 0 {
			evidence, _ := json.Marshal(map[string]any{"source": "recovery_order_detail", "packageSnList": packageSNs})
			if err := s.store.UpdateShipmentSubmission(ctx, shipment.ID, "label_pending", packageSNs, evidence, "", ""); err != nil {
				return PurchaseResult{}, err
			}
			return PurchaseResult{}, errors.New("Temu order detail now contains a package; recovery stopped and the existing package will be checked")
		}
	}
	quote, err := s.store.GetQuote(ctx, strings.TrimSpace(quoteID))
	if err != nil {
		return PurchaseResult{}, err
	}
	if quote.ParentOrderSN != shipment.ParentOrderSN {
		return PurchaseResult{}, errors.New("recovery quote does not belong to this shipment")
	}
	if time.Now().After(quote.ExpiresAt) {
		return PurchaseResult{}, errors.New("shipping quote has expired; request a new quote")
	}
	newCarrier := carrierCode(temu.ShippingChannel{ShippingCompanyName: quote.ShippingCompanyName, ShipLogisticsType: quote.ShipLogisticsType})
	for _, failedCarrier := range recoveryExcludedCarrierCodes(shipment) {
		if newCarrier != "" && strings.EqualFold(newCarrier, failedCarrier) {
			return PurchaseResult{}, errors.New("failed carrier cannot be selected again for this recovery")
		}
	}
	order, err := s.store.GetOrder(ctx, quote.ParentOrderSN)
	if err != nil {
		return PurchaseResult{}, err
	}
	if !order.Open || order.Status != 2 {
		return PurchaseResult{}, errOrderNoLongerAwaitingShipment
	}
	if err := s.validateOrderWarehouseAllowed(ctx, order, quote.OMSWarehouseKey); err != nil {
		return PurchaseResult{}, err
	}
	var saved storedQuoteRequest
	if err := json.Unmarshal(quote.RequestPayload, &saved); err != nil {
		return PurchaseResult{}, errors.New("stored quote request is invalid")
	}
	if saved.RecoveryShipmentID != shipment.ID {
		return PurchaseResult{}, errors.New("quote was not created for this shipment recovery")
	}
	request, err := shipmentCreateRequest(order, quote, saved.Package, saved.SelectedChannel)
	if err != nil {
		return PurchaseResult{}, err
	}
	requestRaw, _ := json.Marshal(request)
	replacement := model.Shipment{
		ID: shipment.ID, QuoteID: quote.ID, IdempotencyKey: shipment.IdempotencyKey,
		SelectionMode: "operator_recovery", WarehouseID: quote.TemuWarehouseID,
		ChannelID: quote.ChannelID, ShipCompanyID: quote.ShipCompanyID,
		ShippingCompanyName: quote.ShippingCompanyName, ShipLogisticsType: quote.ShipLogisticsType,
		RequestPayload: requestRaw, ParentOrderSN: order.ParentOrderSN,
	}
	currentCarrier := carrierCode(temu.ShippingChannel{
		ShippingCompanyName: shipment.ShippingCompanyName,
		ShipLogisticsType:   shipment.ShipLogisticsType,
	})
	if err := s.store.PrepareFailedShipmentRecovery(ctx, shipment.ID, replacement, saved.ChoiceAnalysis, currentCarrier); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PurchaseResult{}, errors.New("shipment status changed; refresh before resubmitting")
		}
		return PurchaseResult{}, err
	}
	reserved, err := s.store.GetShipment(ctx, shipment.ID)
	if err != nil {
		return PurchaseResult{}, err
	}
	updated, callErr := s.submitReservedShipment(ctx, reserved, request)
	return PurchaseResult{Shipment: updated}, callErr
}

func (s *Service) RecoverFailedShipmentAndQueueCompletion(ctx context.Context, shipmentID, quoteID string) (PurchaseResult, error) {
	result, recoveryErr := s.RecoverFailedShipment(ctx, shipmentID, quoteID)
	if result.Shipment.ID == "" {
		return result, recoveryErr
	}
	queueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if queueErr := s.store.EnsureShipmentCompletionJob(queueCtx, result.Shipment.ID, result.Shipment.Status); queueErr != nil {
		wrapped := fmt.Errorf("面单恢复已记账，但自动确认任务关联失败，请勿重复提交: %w", queueErr)
		return result, errors.Join(recoveryErr, wrapped)
	}
	return result, recoveryErr
}

func packageSNsFromOrderDetail(result temu.OrderPageItem) []string {
	seen := make(map[string]struct{})
	packageSNs := make([]string, 0)
	for _, line := range result.Lines {
		for _, info := range line.PackageSNInfo {
			packageSN := strings.TrimSpace(info.PackageSN)
			if packageSN == "" {
				continue
			}
			if _, exists := seen[packageSN]; exists {
				continue
			}
			seen[packageSN] = struct{}{}
			packageSNs = append(packageSNs, packageSN)
		}
	}
	return packageSNs
}

func (s *Service) RefreshShipment(ctx context.Context, id string) (model.Shipment, error) {
	shipment, err := s.store.GetShipment(ctx, id)
	if err != nil {
		return model.Shipment{}, err
	}
	if shipment.Status == "shipped" {
		s.reconcileConfirmedAutoFulfillment(ctx, shipment)
		return shipment, nil
	}
	if len(shipment.PackageSNList) == 0 {
		detail, _, lookupErr := s.temu.OrderDetail(ctx, shipment.ParentOrderSN)
		if lookupErr != nil {
			return shipment, fmt.Errorf("Temu 未返回 packageSn，实时核验订单详情失败，请勿重复购买: %w", lookupErr)
		}
		packageSNs := packageSNsFromOrderDetail(detail)
		if len(packageSNs) == 0 {
			message := "Temu 购单结果未返回 packageSn，实时订单详情也未发现包裹；已标记为结果待核对，请勿重复购买"
			evidence, _ := json.Marshal(map[string]any{"source": "order_detail", "packageSnList": []string{}})
			if err := s.store.UpdateShipmentSubmission(ctx, shipment.ID, "submission_unknown", []string{}, evidence, "PACKAGE_SN_MISSING", message); err != nil {
				return model.Shipment{}, err
			}
			updated, getErr := s.store.GetShipment(ctx, shipment.ID)
			if getErr != nil {
				return model.Shipment{}, getErr
			}
			return updated, errors.New(message)
		}
		evidence, _ := json.Marshal(map[string]any{"source": "order_detail", "packageSnList": packageSNs})
		if err := s.store.UpdateShipmentSubmission(ctx, shipment.ID, "label_pending", packageSNs, evidence, "", ""); err != nil {
			return model.Shipment{}, err
		}
		shipment, err = s.store.GetShipment(ctx, shipment.ID)
		if err != nil {
			return model.Shipment{}, err
		}
	}
	result, raw, err := s.temu.ShipmentResult(ctx, shipment.PackageSNList)
	if err != nil {
		return shipment, err
	}
	status, tracking, code, message := "label_ready", "", "", ""
	var resultCarrier temu.PackageResult
	for _, item := range result.Packages {
		if resultCarrier.PackageSN == "" {
			resultCarrier = item
		}
		if tracking == "" {
			tracking = item.TrackingNumber
		}
		switch item.ShippingLabelStatus {
		case 0:
			if status != "label_failed" {
				status = "label_pending"
			}
		case 2, 3:
			status = "label_failed"
			code = strconv.Itoa(item.ShippingLabelStatus)
			message = item.FailReasonText
			if message == "" {
				message = item.SolutionText
			}
		}
	}
	if len(result.Packages) == 0 {
		status = "label_pending"
	}
	if err := s.store.UpdateShipmentResult(ctx, id, status, tracking, raw, code, message); err != nil {
		return model.Shipment{}, err
	}
	if err := s.store.ReconcileShipmentResultCarrier(ctx, id, resultCarrier.ChannelID, resultCarrier.ShipCompanyID, resultCarrier.ShippingCompanyName, resultCarrier.ShipLogisticsType); err != nil {
		return model.Shipment{}, err
	}
	if status == "label_failed" && deliveryAddressUnsupported(message) {
		_ = s.store.RecordShipmentCarrierFailure(context.WithoutCancel(ctx), shipment.ID, carrierCode(temu.ShippingChannel{
			ShippingCompanyName: resultCarrier.ShippingCompanyName,
			ShipLogisticsType:   resultCarrier.ShipLogisticsType,
		}))
		if len(shipment.PackageSNList) > 0 {
			if manualErr := s.addManualReviewReason(context.WithoutCancel(ctx), shipment.ParentOrderSN, manualReasonDeliveryAddress); manualErr != nil {
				if s.logger != nil {
					s.logger.Error("failed to move unsupported delivery address to manual review", "parent_order_sn", shipment.ParentOrderSN, "error", manualErr)
				}
			}
		}
	}
	return s.confirmedShipment(ctx, id)
}

func (s *Service) LabelDocuments(ctx context.Context, id string) (temu.ShipmentDocumentResult, error) {
	shipment, err := s.store.GetShipment(ctx, id)
	if err != nil {
		return temu.ShipmentDocumentResult{}, err
	}
	if len(shipment.PackageSNList) == 0 {
		return temu.ShipmentDocumentResult{}, errors.New("shipment has no packageSn")
	}
	result, _, err := s.temu.ShipmentDocument(ctx, shipment.PackageSNList)
	return result, err
}

func (s *Service) ConfirmShipped(ctx context.Context, id string) (model.Shipment, error) {
	shipment, err := s.store.GetShipment(ctx, id)
	if err != nil {
		return model.Shipment{}, err
	}
	if shipment.Status == "shipped" {
		s.reconcileConfirmedAutoFulfillment(ctx, shipment)
		return shipment, nil
	}
	if shipment.Status != "label_ready" && shipment.Status != "confirm_failed" {
		return shipment, errors.New("shipping label is not ready")
	}
	if shipment.TrackingNumber == "" {
		return shipment, errors.New("tracking number is not available")
	}
	order, err := s.store.GetOrder(ctx, shipment.ParentOrderSN)
	if err != nil {
		return shipment, err
	}
	detail := make([]map[string]any, 0, len(order.Lines))
	for _, line := range order.Lines {
		detail = append(detail, map[string]any{"parentOrderSn": order.ParentOrderSN, "orderSn": line.OrderSN, "quantity": line.Quantity})
	}
	packages := make([]map[string]any, 0, len(shipment.PackageSNList))
	for _, packageSN := range shipment.PackageSNList {
		packages = append(packages, map[string]any{"packageSn": packageSN, "trackingNumber": shipment.TrackingNumber, "packageDetail": detail})
	}
	attempt, err := s.store.RecordShipmentConfirmationAttempt(ctx, id)
	if err != nil {
		return shipment, err
	}
	shipment.ConfirmationAttempts = attempt
	raw, err := s.temu.ConfirmShipped(ctx, packages)
	if err != nil {
		if temuAlreadyShipped(err) {
			if err := s.store.MarkShipmentConfirmed(ctx, id, raw); err != nil {
				return shipment, err
			}
			return s.store.GetShipment(ctx, id)
		}
		code, message := apiErrorParts(err)
		if updateErr := s.store.UpdateShipmentResult(context.WithoutCancel(ctx), id, "confirm_failed", shipment.TrackingNumber, raw, code, message); updateErr != nil {
			return shipment, errors.Join(err, updateErr)
		}
		updated, getErr := s.store.GetShipment(context.WithoutCancel(ctx), id)
		if getErr != nil {
			return shipment, errors.Join(err, getErr)
		}
		return updated, err
	}
	if err := s.store.MarkShipmentConfirmed(ctx, id, raw); err != nil {
		return shipment, err
	}
	return s.confirmedShipment(ctx, id)
}

func (s *Service) confirmedShipment(ctx context.Context, id string) (model.Shipment, error) {
	shipment, err := s.store.GetShipment(ctx, id)
	if err != nil {
		return model.Shipment{}, err
	}
	s.reconcileConfirmedAutoFulfillment(ctx, shipment)
	return shipment, nil
}

func (s *Service) reconcileConfirmedAutoFulfillment(ctx context.Context, shipment model.Shipment) {
	err := s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), shipment.ParentOrderSN, shipment.ID, "waiting_oms", "")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && s.logger != nil {
		s.logger.Error("failed to reconcile confirmed shipment job", "parent_order_sn", shipment.ParentOrderSN, "shipment_id", shipment.ID, "error", err)
	}
}

func (s *Service) CheckOMSSync(ctx context.Context, id string) (model.Shipment, error) {
	shipment, err := s.store.GetShipment(ctx, strings.TrimSpace(id))
	if err != nil {
		return model.Shipment{}, err
	}
	if shipment.Status != "shipped" {
		return shipment, errors.New("请先在 Temu 确认发货，再查询领星同步结果")
	}
	if shipment.TrackingNumber == "" || len(shipment.PackageSNList) == 0 {
		return shipment, errors.New("Temu 面单或跟踪号不完整，不能核对领星同步结果")
	}
	mapping, err := s.store.WarehouseMapping(ctx, shipment.OMSWarehouseKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return shipment, errors.New("当前业务仓未配置领星仓库代码")
	}
	if err != nil {
		return shipment, err
	}
	if mapping.OMSWarehouseCode == "" {
		return shipment, errors.New("当前业务仓未配置领星实际仓库代码")
	}
	current, started, err := s.store.StartOMSSync(ctx, shipment.ID, mapping, shipment.TrackingNumber)
	if err != nil {
		return shipment, err
	}
	if !started {
		if current.Status == "verified" || current.Status == "querying" {
			return s.store.GetShipment(ctx, shipment.ID)
		}
		if current.Status == "manual_required" {
			updated, refreshErr := s.store.GetShipment(ctx, shipment.ID)
			if refreshErr != nil {
				return shipment, refreshErr
			}
			return updated, fmt.Errorf("%w: %s", errOMSManualRequired, current.ErrorMessage)
		}
		return shipment, nil
	}
	return s.reconcileOMSPlatformOrder(ctx, shipment, mapping)
}

func (s *Service) failOMSSync(ctx context.Context, shipment model.Shipment, orders []oms.OutboundOrder, cause error) (model.Shipment, error) {
	numbers := make([]string, 0, len(orders))
	for _, order := range orders {
		numbers = append(numbers, order.OutboundOrderNo)
	}
	summary, _ := json.Marshal(map[string]any{"match_count": len(orders)})
	_ = s.store.UpdateOMSSync(context.WithoutCancel(ctx), shipment.ID, "failed", numbers, shipment.TrackingNumber, summary, cause.Error())
	return shipment, cause
}

func (s *Service) QueryPendingOMSSync(ctx context.Context, retryAfter time.Duration, limit int) (int, error) {
	ids, err := s.store.PendingOMSSyncShipmentIDs(ctx, time.Now().Add(-retryAfter), limit)
	if err != nil {
		return 0, err
	}
	problems := make([]error, 0)
	checked := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			problems = append(problems, ctx.Err())
			break
		}
		checked++
		if _, checkErr := s.CheckOMSSync(ctx, id); checkErr != nil {
			problems = append(problems, fmt.Errorf("shipment %s: %w", id, checkErr))
		}
	}
	return checked, errors.Join(problems...)
}
func matchingOMSOrders(orders []oms.OutboundOrder, warehouseCode string) []oms.OutboundOrder {
	seen := make(map[string]struct{}, len(orders))
	result := make([]oms.OutboundOrder, 0, len(orders))
	for _, order := range orders {
		if strings.TrimSpace(order.OutboundOrderNo) == "" {
			continue
		}
		if order.WarehouseCode != "" && !strings.EqualFold(order.WarehouseCode, warehouseCode) {
			continue
		}
		if _, exists := seen[order.OutboundOrderNo]; exists {
			continue
		}
		seen[order.OutboundOrderNo] = struct{}{}
		result = append(result, order)
	}
	return result
}

func omsTrackingVerified(order oms.OutboundOrder, tracking string) bool {
	if order.Status == 4 || order.Status == 5 || order.Status == 7 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(order.TrackingNumber), strings.TrimSpace(tracking))
}

func omsSyncSummary(mapping model.WarehouseMapping, order *oms.OutboundOrder, trackingMatch bool) json.RawMessage {
	summary := map[string]any{
		"warehouse_code": mapping.OMSWarehouseCode,
		"match_count":    0,
	}
	if order != nil {
		summary["match_count"] = 1
		summary["outbound_order_no"] = order.OutboundOrderNo
		summary["status"] = order.Status
		summary["tracking_match"] = trackingMatch
		summary["logistics_channel"] = order.LogisticsChannel
	}
	raw, _ := json.Marshal(summary)
	return raw
}

type AutoFulfillmentAcceptance struct {
	Job      model.AutoFulfillmentJob `json:"job"`
	Accepted bool                     `json:"accepted"`
}

func (s *Service) EnqueueAutoFulfillment(ctx context.Context, parentOrderSN string) (AutoFulfillmentAcceptance, error) {
	parentOrderSN = strings.TrimSpace(parentOrderSN)
	if parentOrderSN == "" {
		return AutoFulfillmentAcceptance{}, errors.New("parent order number is required")
	}
	order, err := s.store.GetOrder(ctx, parentOrderSN)
	if err != nil {
		return AutoFulfillmentAcceptance{}, err
	}
	if !order.Open || order.Status != 2 {
		return AutoFulfillmentAcceptance{}, errOrderNoLongerAwaitingShipment
	}
	if reason := manualOrderReason(order); reason != "" {
		return AutoFulfillmentAcceptance{}, errors.New(reason)
	}
	previous, previousErr := s.store.GetAutoFulfillment(ctx, parentOrderSN)
	if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
		return AutoFulfillmentAcceptance{}, previousErr
	}
	job, err := s.store.EnqueueAutoFulfillment(ctx, parentOrderSN)
	if err != nil {
		return AutoFulfillmentAcceptance{}, err
	}
	accepted := errors.Is(previousErr, pgx.ErrNoRows) || previous.Status == "failed"
	return AutoFulfillmentAcceptance{Job: job, Accepted: accepted}, nil
}

type BulkFulfillmentStart struct {
	Batch   model.BulkFulfillmentBatch `json:"batch"`
	Created bool                       `json:"created"`
}

func (s *Service) StartBulkFulfillment(ctx context.Context) (BulkFulfillmentStart, error) {
	batch, created, err := s.store.CreateBulkFulfillmentBatch(ctx, newID("bulk"))
	if err != nil {
		return BulkFulfillmentStart{}, err
	}
	return BulkFulfillmentStart{Batch: batch, Created: created}, nil
}

func (s *Service) LatestBulkFulfillment(ctx context.Context) (model.BulkFulfillmentBatch, error) {
	return s.store.LatestBulkFulfillmentBatch(ctx)
}

func (s *Service) RestartBulkFulfillment(ctx context.Context) (model.BulkFulfillmentBatch, error) {
	return s.store.RestartBulkFulfillmentBatch(ctx)
}

func (s *Service) ProcessBulkFulfillment(ctx context.Context, concurrency int) (bool, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	batch, items, err := s.store.RunningBulkFulfillmentItems(ctx, concurrency)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stop := func(item model.BulkFulfillmentItem, cause error) error {
		message := cause.Error()
		if _, finishErr := s.store.FinishBulkFulfillmentItem(context.WithoutCancel(ctx), batch.ID, item.ParentOrderSN, "failed", message); finishErr != nil {
			return errors.Join(cause, fmt.Errorf("stop bulk fulfillment batch: %w", finishErr))
		}
		return cause
	}
	processed := false
	completed := false
	for _, item := range items {
		if item.Status != "running" {
			continue
		}
		job, jobErr := s.store.GetAutoFulfillment(ctx, item.ParentOrderSN)
		if jobErr != nil {
			return true, stop(item, jobErr)
		}
		switch job.Status {
		case "waiting_oms", "completed", "skipped":
			if _, finishErr := s.store.FinishBulkFulfillmentItem(ctx, batch.ID, item.ParentOrderSN, "succeeded", ""); finishErr != nil {
				return true, finishErr
			}
			processed = true
			completed = true
			continue
		case "failed":
			order, orderErr := s.store.GetOrder(ctx, item.ParentOrderSN)
			if orderErr == nil && manualOrderReason(order) != "" {
				if _, finishErr := s.store.FinishBulkFulfillmentItem(ctx, batch.ID, item.ParentOrderSN, "succeeded", "订单已转入人工处理，自动批次已跳过"); finishErr != nil {
					return true, finishErr
				}
				processed = true
				completed = true
				continue
			}
			if orderErr != nil {
				return true, stop(item, orderErr)
			}
			message := strings.TrimSpace(job.LastError)
			if message == "" {
				message = "自动发货任务失败"
			}
			return true, stop(item, errors.New(message))
		}
	}
	if completed {
		batch, items, err = s.store.RunningBulkFulfillmentItems(ctx, concurrency)
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return true, err
		}
	}
	for _, item := range items {
		if item.Status != "pending" {
			continue
		}
		order, orderErr := s.store.GetOrder(ctx, item.ParentOrderSN)
		if orderErr != nil {
			return true, stop(item, orderErr)
		}
		if manualOrderReason(order) != "" {
			if _, finishErr := s.store.FinishBulkFulfillmentItem(ctx, batch.ID, item.ParentOrderSN, "succeeded", "订单已转入人工处理，自动批次已跳过"); finishErr != nil {
				return true, finishErr
			}
			processed = true
			continue
		}
		if _, enqueueErr := s.EnqueueAutoFulfillment(ctx, item.ParentOrderSN); errors.Is(enqueueErr, errOrderNoLongerAwaitingShipment) {
			if _, finishErr := s.store.FinishBulkFulfillmentItem(ctx, batch.ID, item.ParentOrderSN, "succeeded", "平台订单已不再待发货，批次已跳过"); finishErr != nil {
				return true, finishErr
			}
			processed = true
			continue
		} else if enqueueErr != nil {
			return true, stop(item, enqueueErr)
		}
		if markErr := s.store.MarkBulkFulfillmentItemRunning(ctx, batch.ID, item.ParentOrderSN); markErr != nil {
			return true, stop(item, markErr)
		}
		processed = true
	}
	return processed, nil
}
func (s *Service) ProcessAutoFulfillments(ctx context.Context, retryAfter time.Duration, concurrency int) (int, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	repaired, err := s.store.RepairShipmentCompletionJobs(ctx, time.Now().Add(-shipmentConfirmationStallDelay), 20)
	if err != nil {
		return 0, fmt.Errorf("repair stalled shipment confirmation jobs: %w", err)
	}
	if repaired > 0 && s.logger != nil {
		s.logger.Warn("repaired stalled shipment confirmation jobs", "count", repaired)
	}
	jobs, err := s.store.ClaimAutoFulfillments(ctx, time.Now().Add(-retryAfter), concurrency)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, len(jobs))
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			if runErr := s.runAutoFulfillment(ctx, job); runErr != nil {
				errorsCh <- fmt.Errorf("order %s: %w", job.ParentOrderSN, runErr)
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	problems := make([]error, 0, len(jobs))
	for problem := range errorsCh {
		problems = append(problems, problem)
	}
	return len(jobs), errors.Join(problems...)
}

func temporaryFulfillmentError(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, problem := range joined.Unwrap() {
			if temporaryFulfillmentError(problem) {
				return true
			}
		}
		return false
	}
	var apiErr *temu.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Temporary || temu.IsRateLimitError(apiErr) || (apiErr.Code == "7000000" && strings.EqualFold(strings.TrimSpace(apiErr.Message), "BUSINESS_SERVICE_ERROR"))
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

func fulfillmentErrorStatus(err error, retryStatus string) string {
	if temporaryFulfillmentError(err) {
		return retryStatus
	}
	return "failed"
}

func shipmentSubmissionRetryDue(shipment model.Shipment, now time.Time) bool {
	return shipment.LastSubmissionAt.IsZero() || !now.Before(shipment.LastSubmissionAt.Add(shipmentSubmissionRetryDelay))
}

func (s *Service) recoverUnknownShipment(ctx context.Context, shipment model.Shipment) (model.Shipment, error) {
	now := time.Now()
	if !shipmentSubmissionRetryDue(shipment, now) {
		return shipment, nil
	}
	verified, verifyErr := s.RefreshShipment(ctx, shipment.ID)
	shipment = verified
	if verifyErr == nil {
		return shipment, nil
	}
	if shipment.ErrorCode != "PACKAGE_SN_MISSING" {
		return shipment, verifyErr
	}
	if shipment.SubmissionAttempts >= maxShipmentSubmissionAttempts {
		return shipment, fmt.Errorf("%w: %s", errShipmentSubmissionAttemptsExhausted, shipment.ParentOrderSN)
	}
	if err := s.store.PrepareUnknownShipmentRetry(ctx, shipment.ID, maxShipmentSubmissionAttempts, now.Add(-shipmentSubmissionRetryDelay)); err != nil {
		return shipment, err
	}
	retrying, err := s.store.GetShipment(ctx, shipment.ID)
	if err != nil {
		return model.Shipment{}, err
	}
	var request map[string]any
	if err := json.Unmarshal(retrying.RequestPayload, &request); err != nil {
		return retrying, errors.New("stored shipment create request is invalid")
	}
	return s.submitReservedShipment(ctx, retrying, request)
}

func (s *Service) runAutoFulfillment(ctx context.Context, job model.AutoFulfillmentJob) error {
	shipment, err := s.store.ShipmentForOrder(ctx, job.ParentOrderSN)
	if errors.Is(err, pgx.ErrNoRows) {
		quote, quoteErr := s.Quote(ctx, QuoteRequest{ParentOrderSN: job.ParentOrderSN, Region: "auto"})
		if quoteErr != nil {
			if errors.Is(quoteErr, errOrderNoLongerAwaitingShipment) {
				return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, "", "skipped", "平台订单已不再待发货")
			}
			status := fulfillmentErrorStatus(quoteErr, "queued")
			_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, "", status, quoteErr.Error())
			return quoteErr
		}
		purchased, purchaseErr := s.Purchase(ctx, quote.Quote.ID)
		shipment = purchased.Shipment
		if shipment.ID == "" {
			message := "Temu shipment was not created"
			if purchaseErr != nil {
				message = purchaseErr.Error()
			}
			_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, "", "failed", message)
			return errors.New(message)
		}
		if purchaseErr != nil {
			nextStatus := "waiting_label"
			if shipment.Status == "label_failed" {
				nextStatus = "failed"
				if automaticCarrierFallbackAllowed(shipment) {
					nextStatus = "running"
				}
			}
			_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, shipment.ID, nextStatus, purchaseErr.Error())
			if nextStatus == "failed" {
				return purchaseErr
			}
		}
	} else if err != nil {
		_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, "", "failed", err.Error())
		return err
	}

	for step := 0; step < 4; step++ {
		switch shipment.Status {
		case "submission_unknown":
			updated, recoverErr := s.recoverUnknownShipment(ctx, shipment)
			shipment = updated
			if recoverErr != nil {
				nextStatus := fulfillmentErrorStatus(recoverErr, "waiting_label")
				if shipment.Status == "label_failed" || errors.Is(recoverErr, errShipmentSubmissionAttemptsExhausted) {
					nextStatus = "failed"
				}
				_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, shipment.ID, nextStatus, recoverErr.Error())
				return recoverErr
			}
			if shipment.Status != "submission_unknown" {
				continue
			}
			return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "waiting_label", "")
		case "submitting", "label_pending":
			updated, refreshErr := s.RefreshShipment(ctx, shipment.ID)
			shipment = updated
			if refreshErr != nil {
				nextStatus := fulfillmentErrorStatus(refreshErr, "waiting_label")
				if shipment.Status == "label_failed" {
					nextStatus = "failed"
				}
				if shipment.Status == "submission_unknown" && shipment.ErrorCode == "PACKAGE_SN_MISSING" {
					nextStatus = "waiting_label"
				}
				_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, shipment.ID, nextStatus, refreshErr.Error())
				if nextStatus == "failed" {
					return refreshErr
				}
				if shipment.Status == "submission_unknown" && nextStatus == "waiting_label" {
					return nil
				}
				return refreshErr
			}
			if shipment.Status == "label_ready" {
				continue
			}
			return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "waiting_label", "")
		case "label_ready", "confirm_failed":
			if err := s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "confirming", ""); err != nil {
				return err
			}
			updated, confirmErr := s.ConfirmShipped(ctx, shipment.ID)
			shipment = updated
			if confirmErr != nil {
				status := fulfillmentErrorStatus(confirmErr, "confirming")
				message := confirmErr.Error()
				if status == "confirming" && shipment.ConfirmationAttempts >= maxShipmentConfirmationAttempts && !temu.IsRateLimitError(confirmErr) {
					status = "failed"
					message = fmt.Sprintf("Temu 确认发货连续失败 %d 次: %s", shipment.ConfirmationAttempts, message)
				}
				_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, shipment.ID, status, message)
				return confirmErr
			}
			continue
		case "shipped":
			updated, checkErr := s.CheckOMSSync(ctx, shipment.ID)
			shipment = updated
			if checkErr != nil {
				_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, shipment.ID, autoFulfillmentOMSFailureStatus(checkErr), checkErr.Error())
				return checkErr
			}
			if shipment.OMSSync != nil && shipment.OMSSync.Status == "verified" {
				return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "completed", "")
			}
			return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "waiting_oms", "")
		case "label_failed":
			if automaticCarrierFallbackAllowed(shipment) {
				failedCarrier := carrierCode(temu.ShippingChannel{ShippingCompanyName: shipment.ShippingCompanyName, ShipLogisticsType: shipment.ShipLogisticsType})
				if err := s.store.RecordShipmentCarrierFailure(ctx, shipment.ID, failedCarrier); err != nil {
					return err
				}
				quote, quoteErr := s.QuoteFailedShipmentRecovery(ctx, shipment.ID, QuoteRequest{Region: "auto"})
				if quoteErr != nil {
					_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, shipment.ID, "failed", quoteErr.Error())
					return quoteErr
				}
				purchased, purchaseErr := s.RecoverFailedShipment(ctx, shipment.ID, quote.Quote.ID)
				shipment = purchased.Shipment
				if purchaseErr != nil {
					if automaticCarrierFallbackAllowed(shipment) {
						continue
					}
					_ = s.store.UpdateAutoFulfillment(context.WithoutCancel(ctx), job.ParentOrderSN, shipment.ID, "failed", purchaseErr.Error())
					return purchaseErr
				}
				continue
			}
			message := shipment.ErrorMessage
			if message == "" {
				message = "Temu label purchase failed"
			}
			return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "failed", message)
		default:
			return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "failed", "unsupported shipment status: "+shipment.Status)
		}
	}
	return s.store.UpdateAutoFulfillment(ctx, job.ParentOrderSN, shipment.ID, "waiting_oms", "")
}

func (s *Service) GetShipment(ctx context.Context, id string) (model.Shipment, error) {
	return s.store.GetShipment(ctx, id)
}

func (s *Service) ShipmentForOrder(ctx context.Context, parentOrderSN string) (model.Shipment, error) {
	parentOrderSN = strings.TrimSpace(parentOrderSN)
	if parentOrderSN == "" {
		return model.Shipment{}, errors.New("parentOrderSn is required")
	}
	return s.store.ShipmentForOrder(ctx, parentOrderSN)
}

func (s *Service) ShipmentsForOrders(ctx context.Context, parentOrderSNs []string) ([]model.Shipment, error) {
	parentOrderSNs, err := normalizeParentOrderSNs(parentOrderSNs)
	if err != nil {
		return nil, err
	}
	shipments := make([]model.Shipment, 0, len(parentOrderSNs))
	for _, parentOrderSN := range parentOrderSNs {
		shipment, lookupErr := s.store.ShipmentForOrder(ctx, parentOrderSN)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			continue
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("shipment for order %s: %w", parentOrderSN, lookupErr)
		}
		shipments = append(shipments, shipment)
	}
	return shipments, nil
}

func normalizeParentOrderSNs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("parent_order_sns is required")
	}
	if len(values) > 50 {
		return nil, errors.New("at most 50 parent order numbers may be queried")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 100 || strings.ContainsAny(value, "\r\n\t") {
			return nil, errors.New("invalid parent order number")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (s *Service) PackageTracking(ctx context.Context, packageSN, language string) (temu.TrackingInfoResult, error) {
	packageSN = strings.TrimSpace(packageSN)
	if packageSN == "" {
		return temu.TrackingInfoResult{}, errors.New("packageSn is required")
	}
	result, _, err := s.temu.TrackingInfo(ctx, packageSN, strings.TrimSpace(language))
	return result, err
}

type OrderTrackingResult struct {
	ParentOrderSN string                    `json:"parent_order_sn"`
	Packages      []temu.TrackingInfoResult `json:"packages"`
}

func (s *Service) OrderTracking(ctx context.Context, parentOrderSN, language string) (OrderTrackingResult, error) {
	parentOrderSN = strings.TrimSpace(parentOrderSN)
	if parentOrderSN == "" {
		return OrderTrackingResult{}, errors.New("parentOrderSn is required")
	}
	shipment, err := s.store.ShipmentForOrder(ctx, parentOrderSN)
	if err == nil {
		result, trackingErr := s.orderTrackingForShipment(ctx, shipment, language)
		if trackingErr == nil || !errors.Is(trackingErr, ErrPackageSNNotReady) {
			return result, trackingErr
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return OrderTrackingResult{}, fmt.Errorf("shipment for order %s: %w", parentOrderSN, err)
	}
	packageSNs, err := s.orderTrackingPackageSNs(ctx, parentOrderSN)
	if err != nil {
		return OrderTrackingResult{}, err
	}
	return s.orderTrackingForPackageSNs(ctx, parentOrderSN, packageSNs, language)
}

func (s *Service) orderTrackingForShipment(ctx context.Context, shipment model.Shipment, language string) (OrderTrackingResult, error) {
	return s.orderTrackingForPackageSNs(ctx, shipment.ParentOrderSN, shipment.PackageSNList, language)
}

func (s *Service) orderTrackingForPackageSNs(ctx context.Context, parentOrderSN string, packageSNs []string, language string) (OrderTrackingResult, error) {
	result := OrderTrackingResult{
		ParentOrderSN: strings.TrimSpace(parentOrderSN),
		Packages:      make([]temu.TrackingInfoResult, 0, len(packageSNs)),
	}
	seen := make(map[string]struct{}, len(packageSNs))
	for _, value := range packageSNs {
		packageSN := strings.TrimSpace(value)
		if packageSN == "" {
			continue
		}
		if _, exists := seen[packageSN]; exists {
			continue
		}
		seen[packageSN] = struct{}{}

		tracking, err := s.PackageTracking(ctx, packageSN, language)
		if err != nil {
			return OrderTrackingResult{}, fmt.Errorf("query tracking for package %d: %w", len(result.Packages)+1, err)
		}
		if strings.TrimSpace(tracking.PackageSN) == "" {
			tracking.PackageSN = packageSN
		}
		result.Packages = append(result.Packages, tracking)
	}
	if len(result.Packages) == 0 {
		return OrderTrackingResult{}, ErrPackageSNNotReady
	}
	return result, nil
}

func (s *Service) orderTrackingPackageSNs(ctx context.Context, parentOrderSN string) ([]string, error) {
	detail, detailErr := s.store.GetOrderDetail(ctx, parentOrderSN)
	if detailErr == nil {
		if packageSNs, parseErr := packageSNsFromStoredOrderDetail(detail.Raw); parseErr == nil && len(packageSNs) > 0 {
			return packageSNs, nil
		}
	} else if !errors.Is(detailErr, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get stored order detail for tracking: %w", detailErr)
	}
	result, _, err := s.temu.OrderDetail(ctx, parentOrderSN)
	if err != nil {
		return nil, fmt.Errorf("get order detail for tracking: %w", err)
	}
	packageSNs := packageSNsFromOrderDetail(result)
	if len(packageSNs) == 0 {
		return nil, ErrPackageSNNotReady
	}
	return packageSNs, nil
}

func packageSNsFromStoredOrderDetail(raw json.RawMessage) ([]string, error) {
	var envelope struct {
		Result temu.OrderPageItem `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	return packageSNsFromOrderDetail(envelope.Result), nil
}

func (s *Service) ListShipments(ctx context.Context, queue string, page, pageSize int) ([]model.Shipment, int, error) {
	items, total, err := s.store.ListShipments(ctx, queue, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	for index := range items {
		items[index].ConfirmationStalled = shipmentConfirmationStalled(items[index], now)
	}
	return items, total, nil
}

func shipmentConfirmationStalled(shipment model.Shipment, now time.Time) bool {
	if shipment.Status != "label_ready" || shipment.ConfirmedAt != nil || shipment.ConfirmationAttempts != 0 {
		return false
	}
	if len(shipment.PackageSNList) == 0 || strings.TrimSpace(shipment.TrackingNumber) == "" {
		return false
	}
	if shipment.UpdatedAt.IsZero() {
		return false
	}
	return shipment.UpdatedAt.Before(now.Add(-shipmentConfirmationStallDelay))
}
func (s *Service) ListOMSPlatformOrderStatuses(ctx context.Context, status, page, pageSize int) ([]model.OMSPlatformOrderStatus, int, map[int]int, error) {
	return s.store.ListOMSPlatformOrderStatuses(ctx, status, page, pageSize)
}
func (s *Service) ShipmentStatusCounts(ctx context.Context, queue string) (map[string]int, error) {
	return s.store.ShipmentStatusCounts(ctx, queue)
}
func (s *Service) ListShipmentPOGroups(ctx context.Context, from, before *time.Time) ([]model.ShipmentPOGroup, error) {
	return s.store.ListShipmentPOGroups(ctx, from, before)
}

func normalizeOrder(item temu.OrderPageItem) model.Order {
	parentRaw, _ := json.Marshal(item)
	labels, _ := json.Marshal(item.Parent.Labels)
	warnings, _ := json.Marshal(item.Parent.FulfillmentWarnings)
	order := model.Order{ParentOrderSN: item.Parent.ParentOrderSN, Status: item.Parent.ParentOrderStatus, RegionID: item.Parent.RegionID,
		ExpectShipLatestTime: item.Parent.ExpectShipLatestTime, UpdateTime: item.Parent.UpdateTime, Labels: labels, Warnings: warnings,
		Raw: parentRaw, Open: true, Lines: make([]model.OrderLine, 0, len(item.Lines)), BatchOrderSNs: append([]string(nil), item.Parent.BatchOrderNumberList...),
		Consolidated: item.Parent.Consolidated}
	for _, line := range item.Lines {
		extCode := ""
		for _, product := range line.Products {
			if strings.TrimSpace(product.ExtCode) != "" {
				extCode = strings.TrimSpace(product.ExtCode)
				break
			}
		}
		name := line.OriginalName
		if name == "" {
			name = line.GoodsName
		}
		spec := line.OriginalSpec
		if spec == "" {
			spec = line.Spec
		}

		raw, _ := json.Marshal(line)
		if order.FulfillmentType == "" {
			order.FulfillmentType = line.FulfillmentType
		}
		order.Lines = append(order.Lines, model.OrderLine{OrderSN: line.OrderSN, ParentOrderSN: item.Parent.ParentOrderSN, Status: line.OrderStatus, Quantity: line.Quantity, GoodsID: line.GoodsID, SKUID: line.SKUID, ExtCode: extCode, GoodsName: name, Spec: spec, Raw: raw})
	}
	order.ManualReview = classifyOrder(order)
	return order
}

func packageSpecFromResolution(resolution inventory.PackageResolution) (model.PackageSpec, error) {
	if !resolution.Complete || resolution.Package == nil {
		message := strings.TrimSpace(resolution.Error)
		if message == "" {
			message = "仓库SKU包裹规格未完整匹配"
		}
		return model.PackageSpec{}, errors.New(message)
	}
	pack := resolution.Package
	if pack.WeightUnit != "kg" || pack.DimensionUnit != "cm" {
		return model.PackageSpec{}, errors.New("warehouse SKU package spec must use kg and cm")
	}
	totalOunces := int(math.Ceil(pack.Weight * 35.27396195))
	pounds, ounces := totalOunces/16, totalOunces%16
	inches := func(value float64) string {
		converted := math.Ceil(value/2.54*100) / 100
		return strconv.FormatFloat(converted, 'f', -1, 64)
	}
	spec := model.PackageSpec{
		Weight: strconv.Itoa(pounds), WeightUnit: "lb",
		Length: inches(pack.Length), Width: inches(pack.Width), Height: inches(pack.Height), DimensionUnit: "in",
	}
	if ounces > 0 {
		spec.ExtendWeight = strconv.Itoa(ounces)
		spec.ExtendWeightUnit = "oz"
	}
	if err := validatePackage(spec); err != nil {
		return model.PackageSpec{}, fmt.Errorf("invalid warehouse SKU package spec: %w", err)
	}
	return spec, nil
}

func shippingServicesRequest(order model.Order, warehouseID string, spec model.PackageSpec) map[string]any {
	request := packageFields(spec)
	request["warehouseId"] = warehouseID
	request["shipOrderInfoList"] = orderSendInfo(order)
	request["signatureOnDelivery"] = false
	return request
}

func shipmentCreateRequest(order model.Order, quote model.Quote, spec model.PackageSpec, channel temu.ShippingChannel) (map[string]any, error) {
	if channel.ChannelID == 0 || channel.ChannelID != quote.ChannelID || channel.ShipCompanyID != quote.ShipCompanyID {
		return nil, errors.New("stored quote is missing the selected channel requirements; request a new quote")
	}
	pack := packageFields(spec)
	pack["warehouseId"] = quote.TemuWarehouseID
	pack["shipCompanyId"] = quote.ShipCompanyID
	pack["channelId"] = quote.ChannelID
	pack["orderSendInfoList"] = orderSendInfo(order)

	requiresPickup := false
	unsupported := make([]string, 0)
	for _, field := range channel.InfoNeeded {
		switch strings.TrimSpace(field) {
		case "pickupStartTime", "pickupEndTime":
			requiresPickup = true
		case "signServiceId":
			return nil, fmt.Errorf("物流渠道 %s 要求签名服务，当前一单一件自动发货强制不签名", channel.ShippingCompanyName)
		case "":
		default:
			unsupported = append(unsupported, field)
		}
	}
	if len(unsupported) > 0 {
		return nil, fmt.Errorf("物流渠道 %s 要求暂未支持的下单参数: %s", channel.ShippingCompanyName, strings.Join(unsupported, ", "))
	}
	if requiresPickup {
		now := time.Now().Unix()
		var selected *temu.PickupTimeSlot
		for i := range channel.PickupSlots {
			slot := &channel.PickupSlots[i]
			if slot.Start > 0 && slot.End > slot.Start && slot.End > now {
				selected = slot
				break
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("物流渠道 %s 需要预约揽收时间，但报价未返回仍有效的时段，请重新查询物流", channel.ShippingCompanyName)
		}
		pack["pickupStartTime"] = selected.Start
		pack["pickupEndTime"] = selected.End
	}
	return map[string]any{"sendType": 0, "sendRequestList": []any{pack}, "shipLater": true, "shipLaterLimitTime": 24}, nil
}

func packageFields(spec model.PackageSpec) map[string]any {
	result := map[string]any{"weight": spec.Weight, "weightUnit": spec.WeightUnit, "length": spec.Length, "width": spec.Width, "height": spec.Height, "dimensionUnit": spec.DimensionUnit}
	if spec.ExtendWeight != "" {
		result["extendWeight"] = spec.ExtendWeight
	}
	if spec.ExtendWeightUnit != "" {
		result["extendWeightUnit"] = spec.ExtendWeightUnit
	}
	return result
}

func orderSendInfo(order model.Order) []any {
	result := make([]any, 0, len(order.Lines))
	for _, line := range order.Lines {
		item := map[string]any{"parentOrderSn": order.ParentOrderSN, "orderSn": line.OrderSN, "quantity": line.Quantity}
		if line.GoodsID != 0 {
			item["goodsId"] = line.GoodsID
		}
		if line.SKUID != 0 {
			item["skuId"] = line.SKUID
		}
		result = append(result, item)
	}
	return result
}

func validatePackage(spec model.PackageSpec) error {
	for name, value := range map[string]string{"length": spec.Length, "width": spec.Width, "height": spec.Height} {
		number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || number <= 0 {
			return fmt.Errorf("%s must be a positive number", name)
		}
	}
	weight, weightErr := strconv.ParseFloat(strings.TrimSpace(spec.Weight), 64)
	extendWeight := 0.0
	if strings.TrimSpace(spec.ExtendWeight) != "" {
		extendWeight, _ = strconv.ParseFloat(strings.TrimSpace(spec.ExtendWeight), 64)
	}
	if weightErr != nil || weight < 0 || (weight == 0 && extendWeight <= 0) {
		return errors.New("weight must be positive")
	}
	if spec.WeightUnit != "lb" && spec.WeightUnit != "kg" {
		return errors.New("weight_unit must be lb or kg")
	}
	if spec.DimensionUnit != "in" && spec.DimensionUnit != "cm" {
		return errors.New("dimension_unit must be in or cm")
	}
	return nil
}

func inventoryUnboundSKUs(decision inventory.DecisionResponse) []string {
	result := make([]string, 0)
	for _, record := range decision.Records {
		activeWarehouses := 0
		successfulQueries := 0
		found := false
		for _, region := range record.Regions {
			for _, warehouse := range region.Warehouses {
				if !warehouse.Active {
					continue
				}
				activeWarehouses++
				if warehouse.QueryStatus == "succeeded" {
					successfulQueries++
					found = found || warehouse.SKUFound
				}
			}
		}
		if activeWarehouses > 0 && successfulQueries == activeWarehouses && !found {
			result = append(result, record.SKU)
		}
	}
	sort.Strings(result)
	return result
}

func classifyOrder(order model.Order) *model.ManualReview {
	reasons := make([]string, 0, 4)
	units := 0
	hasUnboundSKU := false
	for _, line := range order.Lines {
		units += line.Quantity
		if strings.TrimSpace(line.ExtCode) == "" {
			hasUnboundSKU = true
		}
	}
	if hasUnboundSKU {
		reasons = append(reasons, "sku_unbound")
	}
	if len(order.Lines) > 1 || units > 1 {
		reasons = append(reasons, "multi_item")
	}
	mergeOrders := append([]string{}, order.BatchOrderSNs...)
	sort.Strings(mergeOrders)
	if len(mergeOrders) > 0 {
		reasons = append(reasons, "merge_candidate")
	}
	if order.Consolidated {
		reasons = append(reasons, "platform_consolidated")
	}
	if len(reasons) == 0 {
		return nil
	}
	return &model.ManualReview{ParentOrderSN: order.ParentOrderSN, Reasons: reasons, MergeOrderSNs: mergeOrders, Status: "detected", Active: true}
}

func warehouseManualReviewCanBeRechecked(order model.Order) bool {
	review := order.ManualReview
	if review == nil || !review.Active || len(review.Reasons) == 0 {
		return false
	}
	for _, reason := range review.Reasons {
		if reason != "sku_unbound" && reason != manualReasonInventoryRule && reason != manualReasonWarehouseSKUSpec && reason != manualReasonSKUWarehousePolicy {
			return false
		}
	}
	return true
}

func hasBlockingWarehouseReason(review *model.ManualReview) bool {
	return review != nil && (contains(review.Reasons, "sku_unbound") ||
		contains(review.Reasons, manualReasonInventoryRule) ||
		contains(review.Reasons, manualReasonWarehouseSKUSpec) ||
		contains(review.Reasons, manualReasonSKUWarehousePolicy) ||
		contains(review.Reasons, manualReasonDeliveryAddress))
}

func hasActiveManualReason(order model.Order, reason string) bool {
	return order.ManualReview != nil && order.ManualReview.Active && contains(order.ManualReview.Reasons, reason)
}

func manualOrderReason(order model.Order) string {
	if review := order.ManualReview; review != nil {
		if review.Status == "resolved" && review.Outcome != "" {
			return "order manual fulfillment is already completed"
		}
		if review.Active && (review.Status != "approved" || hasBlockingWarehouseReason(review)) {
			return "order is assigned to manual review: " + strings.Join(review.Reasons, ", ")
		}
	}
	var labels []temu.NameValue
	_ = json.Unmarshal(order.Labels, &labels)
	blocked := map[string]bool{"pending_buyer_cancellation": true, "pending_buyer_address_change": true, "pending_risk_control_alert": true}
	for _, label := range labels {
		if label.Value == 1 && blocked[label.Name] {
			return "order requires manual review because of label: " + label.Name
		}
	}
	var warnings []string
	_ = json.Unmarshal(order.Warnings, &warnings)
	for _, warning := range warnings {
		if warning == "REQUIRES_CUSTOMER_PICKUP" || warning == "RESTRICT_SELF_SHIPPING" {
			return "order requires manual review because of fulfillment warning: " + warning
		}
	}
	return ""
}
func price(raw string) float64 {
	matched := numericText.FindString(strings.ReplaceAll(raw, ",", ""))
	value, err := strconv.ParseFloat(matched, 64)
	if err != nil {
		return math.Inf(1)
	}
	return value
}
func sortedKeys(values map[string]int) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
func countLines(orders []model.Order) int {
	total := 0
	for _, order := range orders {
		total += len(order.Lines)
	}
	return total
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func newID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}
func temuAlreadyShipped(err error) bool {
	var apiErr *temu.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Code == "120012004" {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	return strings.Contains(message, "order has been shipped") &&
		strings.Contains(message, "not effective for the shipped order")
}

func temuShipmentRequestAlreadyExists(err error) bool {
	var apiErr *temu.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Code == "120012013" {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	return strings.Contains(message, "already requested by temu integrated logistics") &&
		strings.Contains(message, "shipment.result.get")
}

func apiErrorParts(err error) (string, string) {
	var apiErr *temu.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code, apiErr.Message
	}
	return "", err.Error()
}
func humanDuration(seconds int64) string {
	if seconds <= 0 {
		return "已过期"
	}
	days := seconds / 86400
	if days > 0 {
		return fmt.Sprintf("%d天%d小时", days, (seconds%86400)/3600)
	}
	hours := seconds / 3600
	if hours > 0 {
		return fmt.Sprintf("%d小时%d分钟", hours, (seconds%3600)/60)
	}
	return fmt.Sprintf("%d分钟", seconds/60)
}
