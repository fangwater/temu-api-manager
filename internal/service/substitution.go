package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"

	"github.com/jackc/pgx/v5"
)

type SubstitutionOrderMatch struct {
	WarehouseSKU string                   `json:"warehouse_sku"`
	Quantity     int                      `json:"quantity"`
	Combination  inventory.SKUCombination `json:"combination"`
}

type SubstitutionOrderCandidate struct {
	Order   model.Order              `json:"order"`
	Matches []SubstitutionOrderMatch `json:"matches"`
}

type SubstitutionPriceQuote struct {
	WarehouseKey      string  `json:"warehouse_key"`
	WarehouseName     string  `json:"warehouse_name"`
	TemuWarehouseID   string  `json:"temu_warehouse_id"`
	TemuWarehouseName string  `json:"temu_warehouse_name"`
	ShippingCompany   string  `json:"shipping_company_name"`
	ShipLogisticsType string  `json:"ship_logistics_type"`
	ChannelID         int64   `json:"channel_id"`
	ShipCompanyID     int64   `json:"ship_company_id"`
	Amount            float64 `json:"amount"`
	Currency          string  `json:"currency"`
}

type substitutionPriceCandidate struct {
	quote     SubstitutionPriceQuote
	candidate autoChannelCandidate
}

type SubstitutionPriceOption struct {
	Kind      string                                `json:"kind"`
	Items     []inventory.PackageSpecResolveRequest `json:"items"`
	Available bool                                  `json:"available"`
	Reason    string                                `json:"reason,omitempty"`
	Package   *model.PackageSpec                    `json:"package,omitempty"`
	BestQuote *SubstitutionPriceQuote               `json:"best_quote,omitempty"`
	Quotes    []SubstitutionPriceQuote              `json:"quotes"`
	Problems  []string                              `json:"unavailable_reasons,omitempty"`
}

type SubstitutionPurchaseRequest struct {
	ParentOrderSN      string `json:"-"`
	WarehouseKey       string `json:"warehouse_key"`
	PreferredChannelID int64  `json:"channel_id,omitempty"`
}

type storedSubstitutionQuote struct {
	CombinationID int64                          `json:"combination_id"`
	PlatformSKU   string                         `json:"platform_sku"`
	Items         []inventory.SKUCombinationItem `json:"items"`
	Quantities    map[string]int                 `json:"quantities"`
}

type SubstitutionPriceComparison struct {
	ParentOrderSN string                   `json:"parent_order_sn"`
	Combination   inventory.SKUCombination `json:"combination"`
	Direct        SubstitutionPriceOption  `json:"direct"`
	Replacement   SubstitutionPriceOption  `json:"replacement"`
	QueriedAt     time.Time                `json:"queried_at"`
}

func (s *Service) ListSubstitutionOrders(ctx context.Context, query string, page, pageSize int) ([]SubstitutionOrderCandidate, int, int, error) {
	combinations, bySKU, err := s.activeSubstitutionCatalog(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	targets := make([]string, 0, len(bySKU))
	for sku := range bySKU {
		targets = append(targets, sku)
	}
	sort.Strings(targets)
	parents, total, err := s.store.ListSubstitutionOrderSNs(ctx, targets, query, page, pageSize)
	if err != nil {
		return nil, 0, 0, err
	}
	result := make([]SubstitutionOrderCandidate, 0, len(parents))
	for _, parent := range parents {
		order, orderErr := s.store.GetOrder(ctx, parent)
		if orderErr != nil {
			return nil, 0, 0, orderErr
		}
		job, jobErr := s.store.GetAutoFulfillment(ctx, parent)
		if jobErr == nil {
			order.AutoFulfillment = &job
		} else if !errors.Is(jobErr, pgx.ErrNoRows) {
			return nil, 0, 0, jobErr
		}
		match, ok := singleUnitSubstitutionMatch(order, bySKU)
		if !ok {
			continue
		}
		result = append(result, SubstitutionOrderCandidate{Order: order, Matches: []SubstitutionOrderMatch{match}})
	}
	return result, total, len(combinations), nil
}

func singleUnitSubstitutionMatch(order model.Order, bySKU map[string]inventory.SKUCombination) (SubstitutionOrderMatch, bool) {
	if len(order.Lines) != 1 || order.Lines[0].Quantity != 1 {
		return SubstitutionOrderMatch{}, false
	}
	sku := strings.TrimSpace(order.Lines[0].ExtCode)
	combination, ok := bySKU[sku]
	if sku == "" || !ok {
		return SubstitutionOrderMatch{}, false
	}
	return SubstitutionOrderMatch{WarehouseSKU: sku, Quantity: 1, Combination: combination}, true
}

func (s *Service) CompareSubstitutionPrices(ctx context.Context, parent string) (SubstitutionPriceComparison, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return SubstitutionPriceComparison{}, errors.New("parent order number is required")
	}
	order, err := s.store.GetOrder(ctx, parent)
	if err != nil {
		return SubstitutionPriceComparison{}, err
	}
	if !order.Open || order.Status != 2 {
		return SubstitutionPriceComparison{}, errOrderNoLongerAwaitingShipment
	}
	if _, err := s.store.ShipmentForOrder(ctx, parent); err == nil {
		return SubstitutionPriceComparison{}, errors.New("order already has a shipping-label record")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return SubstitutionPriceComparison{}, err
	}

	_, bySKU, err := s.activeSubstitutionCatalog(ctx)
	if err != nil {
		return SubstitutionPriceComparison{}, err
	}
	originalQuantities := orderQuantities(order)
	if len(originalQuantities) != 1 {
		return SubstitutionPriceComparison{}, errors.New("当前替代询价只支持订单内一个仓库 SKU")
	}
	var originalSKU string
	var originalQuantity int
	for sku, quantity := range originalQuantities {
		originalSKU, originalQuantity = sku, quantity
	}
	combination, ok := bySKU[originalSKU]
	if !ok {
		return SubstitutionPriceComparison{}, fmt.Errorf("SKU %s 没有启用的替代发货组合", originalSKU)
	}
	replacementQuantities := expandSubstitutionQuantities(combination, originalQuantity)
	policies, err := s.carrierPoliciesByWarehouse(ctx)
	if err != nil {
		return SubstitutionPriceComparison{}, err
	}

	comparison := SubstitutionPriceComparison{ParentOrderSN: parent, Combination: combination, QueriedAt: time.Now()}
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		comparison.Direct = s.compareDirectOption(ctx, order, originalQuantities, policies)
	}()
	go func() {
		defer group.Done()
		comparison.Replacement = s.compareReplacementOption(ctx, order, combination, originalQuantity, replacementQuantities, policies)
	}()
	group.Wait()
	return comparison, nil
}

func (s *Service) PurchaseSubstitution(ctx context.Context, request SubstitutionPurchaseRequest) (PurchaseResult, error) {
	quoted, err := s.QuoteSubstitution(ctx, request)
	if err != nil {
		return PurchaseResult{}, err
	}
	return s.PurchaseAndQueueCompletion(ctx, quoted.Quote.ID)
}

func (s *Service) QuoteSubstitution(ctx context.Context, request SubstitutionPurchaseRequest) (QuoteResult, error) {
	request.ParentOrderSN = strings.TrimSpace(request.ParentOrderSN)
	request.WarehouseKey = strings.ToUpper(strings.TrimSpace(request.WarehouseKey))
	if request.ParentOrderSN == "" {
		return QuoteResult{}, errors.New("parent order number is required")
	}
	if warehouseRegion(request.WarehouseKey) == "" {
		return QuoteResult{}, errors.New("请选择有效的替代发货仓库")
	}
	order, err := s.store.GetOrder(ctx, request.ParentOrderSN)
	if err != nil {
		return QuoteResult{}, err
	}
	if !order.Open || order.Status != 2 {
		return QuoteResult{}, errOrderNoLongerAwaitingShipment
	}
	if _, err := s.store.ShipmentForOrder(ctx, order.ParentOrderSN); err == nil {
		return QuoteResult{}, errors.New("订单已经存在面单记录，禁止重复购买")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return QuoteResult{}, err
	}
	if reason := manualOrderReason(order); reason != "" && !warehouseManualReviewCanBeRechecked(order) {
		return QuoteResult{}, errors.New(reason)
	}

	_, bySKU, err := s.activeSubstitutionCatalog(ctx)
	if err != nil {
		return QuoteResult{}, err
	}
	originalQuantities := orderQuantities(order)
	if len(originalQuantities) != 1 {
		return QuoteResult{}, errors.New("组合替代发货只支持订单内一个平台 SKU")
	}
	var platformSKU string
	var originalQuantity int
	for sku, quantity := range originalQuantities {
		platformSKU, originalQuantity = sku, quantity
	}
	combination, ok := bySKU[platformSKU]
	if !ok {
		return QuoteResult{}, fmt.Errorf("SKU %s 没有启用的替代发货组合", platformSKU)
	}
	if err := validateSubstitutionCombination(combination); err != nil {
		return QuoteResult{}, err
	}
	if originalQuantity != 1 {
		return QuoteResult{}, errors.New("多件订单没有已确认的整单尺寸和重量，禁止组合替代购买面单")
	}
	replacementQuantities := expandSubstitutionQuantities(combination, originalQuantity)
	decision, err := s.queryInventory(ctx, replacementQuantities)
	if err != nil {
		return QuoteResult{}, fmt.Errorf("替代组合库存校验失败: %w", err)
	}
	if err := s.applyShopSKUWarehouseRules(ctx, &decision); err != nil {
		return QuoteResult{}, err
	}
	selection, err := selectWarehouseForPriceComparison(decision, replacementQuantities, request.WarehouseKey)
	if err != nil {
		return QuoteResult{}, err
	}
	if err := s.validateSubstitutionPairing(ctx, request.WarehouseKey, combination); err != nil {
		return QuoteResult{}, err
	}
	mapped, err := s.store.MappedWarehouse(ctx, request.WarehouseKey)
	if err != nil {
		return QuoteResult{}, fmt.Errorf("仓库 %s 没有启用的 Temu 仓库映射", request.WarehouseKey)
	}
	if !mapped.EnableBuyShippingLabel {
		return QuoteResult{}, fmt.Errorf("Temu 仓库 %s 不支持购买面单", mapped.Name)
	}
	packageSpec, packageResolution, err := substitutionPackageSpec(combination)
	if err != nil {
		return QuoteResult{}, err
	}
	shippingRequest := shippingServicesRequest(order, mapped.ID, packageSpec)
	channels, raw, err := s.temu.ShippingServices(ctx, shippingRequest)
	if err != nil {
		return QuoteResult{}, err
	}
	allowed, rejected := filterAutomaticChannels(channels.Available)
	policies, err := s.carrierPoliciesByWarehouse(ctx)
	if err != nil {
		return QuoteResult{}, err
	}
	allowed, policyRejected := filterChannelsByCarrierPolicy(allowed, request.WarehouseKey, policies[request.WarehouseKey])
	rejected = append(rejected, policyRejected...)
	channels.Unavailable = append(channels.Unavailable, rejected...)
	candidates := make([]autoChannelCandidate, 0, len(allowed))
	for _, channel := range allowed {
		amount := price(channel.EstimatedAmount)
		if math.IsInf(amount, 1) {
			continue
		}
		candidates = append(candidates, autoChannelCandidate{
			warehouseIndex: 0, warehouseKey: request.WarehouseKey, temuWarehouseID: mapped.ID,
			channel: channel, amount: amount,
			priority: configuredCarrierPriority(policies[request.WarehouseKey], carrierCode(channel)),
		})
	}
	if len(candidates) == 0 {
		return QuoteResult{}, fmt.Errorf("仓库 %s 没有符合规则的 Temu 面单渠道", request.WarehouseKey)
	}
	choice, carrierReason, err := selectAutomaticChannel(candidates, request.PreferredChannelID)
	if err != nil {
		return QuoteResult{}, err
	}
	reason := fmt.Sprintf("组合替代发货：平台 SKU %s 使用组合 %s；尺寸重量取自组合配置；仓库已通过替代库存和 OMS 产品配对校验；%s", platformSKU, combination.Name, carrierReason)
	selection.Reason = reason
	for index := range allowed {
		if allowed[index].ChannelID == choice.channel.ChannelID {
			allowed[index].Selected = true
			allowed[index].SelectionReason = reason
		}
	}
	choiceAnalysis := buildLabelPurchaseChoice(candidates, choice, reason, request.PreferredChannelID != 0)
	requestRecord, _ := json.Marshal(storedQuoteRequest{
		Package: packageSpec, ShippingRequest: shippingRequest, SelectedChannel: choice.channel,
		ChoiceAnalysis: choiceAnalysis,
		Substitution: &storedSubstitutionQuote{
			CombinationID: combination.ID, PlatformSKU: platformSKU,
			Items: append([]inventory.SKUCombinationItem(nil), combination.Items...), Quantities: replacementQuantities,
		},
	})
	responseRecord, _ := json.Marshal(map[string]any{"temu_raw": json.RawMessage(raw), "available": allowed, "unavailable": channels.Unavailable})
	quote := model.Quote{
		ID: newID("q"), ParentOrderSN: order.ParentOrderSN,
		OMSWarehouseKey: request.WarehouseKey, TemuWarehouseID: mapped.ID, Region: warehouseRegion(request.WarehouseKey),
		ChannelID: choice.channel.ChannelID, ShipCompanyID: choice.channel.ShipCompanyID,
		ShippingCompanyName: choice.channel.ShippingCompanyName, ShipLogisticsType: choice.channel.ShipLogisticsType,
		SelectionReason: reason, RequestPayload: requestRecord, ResponsePayload: responseRecord,
		ExpiresAt: time.Now().Add(s.quoteLifetime),
	}
	if err := s.store.SaveQuote(ctx, quote); err != nil {
		return QuoteResult{}, err
	}
	return QuoteResult{
		Quote: quote, WarehouseSelection: selection, TemuWarehouse: mapped,
		Package: packageSpec, PackageResolution: packageResolution,
		AvailableChannels: allowed, UnavailableChannels: channels.Unavailable,
	}, nil
}

func (s *Service) validateStoredSubstitutionQuote(ctx context.Context, order model.Order, quote model.Quote, saved storedQuoteRequest) error {
	metadata := saved.Substitution
	if metadata == nil {
		return errors.New("组合替代报价缺少校验信息，请重新询价")
	}
	_, bySKU, err := s.activeSubstitutionCatalog(ctx)
	if err != nil {
		return err
	}
	combination, ok := bySKU[strings.TrimSpace(metadata.PlatformSKU)]
	if !ok || combination.ID != metadata.CombinationID {
		return errors.New("替代组合已停用或发生变化，请重新询价")
	}
	if err := validateSubstitutionCombination(combination); err != nil {
		return err
	}
	if !sameSubstitutionItems(combination.Items, metadata.Items) {
		return errors.New("替代组合成员或数量已变化，请重新询价")
	}
	originalQuantities := orderQuantities(order)
	if len(originalQuantities) != 1 || originalQuantities[metadata.PlatformSKU] != 1 {
		return errors.New("订单商品或数量已变化，禁止使用原组合报价")
	}
	quantities := expandSubstitutionQuantities(combination, 1)
	if !sameSubstitutionQuantities(quantities, metadata.Quantities) {
		return errors.New("替代组合数量已变化，请重新询价")
	}
	packageSpec, _, err := substitutionPackageSpec(combination)
	if err != nil {
		return err
	}
	if packageSpec != saved.Package {
		return errors.New("替代组合尺寸或重量已变化，请重新询价")
	}
	decision, err := s.queryInventory(ctx, quantities)
	if err != nil {
		return fmt.Errorf("购单前替代库存复核失败: %w", err)
	}
	if err := s.applyShopSKUWarehouseRules(ctx, &decision); err != nil {
		return err
	}
	if _, err := selectWarehouseForPriceComparison(decision, quantities, quote.OMSWarehouseKey); err != nil {
		return err
	}
	if err := s.validateSubstitutionPairing(ctx, quote.OMSWarehouseKey, combination); err != nil {
		return err
	}
	mapped, err := s.store.MappedWarehouse(ctx, quote.OMSWarehouseKey)
	if err != nil || mapped.ID != quote.TemuWarehouseID || !mapped.EnableBuyShippingLabel {
		return errors.New("所选仓库的 Temu 面单映射已变化，请重新询价")
	}
	return nil
}

func (s *Service) validateSubstitutionPairing(ctx context.Context, warehouseKey string, combination inventory.SKUCombination) error {
	mapping, err := s.store.WarehouseMapping(ctx, warehouseKey)
	if err != nil || !mapping.Enabled || strings.TrimSpace(mapping.OMSAccount) == "" {
		return fmt.Errorf("仓库 %s 未配置可用的 OMS 账户", warehouseKey)
	}
	validation, err := s.inventory.ValidateProductPairing(ctx, substitutionOMSPairingRequest(mapping.OMSAccount, combination))
	if err != nil {
		return fmt.Errorf("仓库 %s 无法校验 OMS 产品配对: %w", warehouseKey, err)
	}
	if !validation.Ready {
		reason := strings.TrimSpace(validation.Reason)
		if reason == "" {
			reason = "OMS 产品配对不可用"
		}
		return fmt.Errorf("仓库 %s 禁止组合替代发货: %s", warehouseKey, reason)
	}
	return nil
}

func substitutionPackageSpec(combination inventory.SKUCombination) (model.PackageSpec, inventory.PackageResolution, error) {
	resolution := inventory.PackageResolution{Complete: true, Package: &inventory.PackageSpec{
		WarehouseSKU: combination.SubstituteForSKU, Weight: combination.WeightKG, WeightUnit: "kg",
		Length: combination.LengthCM, Width: combination.WidthCM, Height: combination.HeightCM, DimensionUnit: "cm",
	}}
	spec, err := packageSpecFromResolution(resolution)
	return spec, resolution, err
}

func sameSubstitutionItems(left, right []inventory.SKUCombinationItem) bool {
	toMap := func(items []inventory.SKUCombinationItem) map[string]int {
		result := make(map[string]int, len(items))
		for _, item := range items {
			result[strings.TrimSpace(item.WarehouseSKU)] += item.Quantity
		}
		return result
	}
	return sameSubstitutionQuantities(toMap(left), toMap(right))
}

func sameSubstitutionQuantities(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for sku, quantity := range left {
		if right[sku] != quantity {
			return false
		}
	}
	return true
}

func (s *Service) activeSubstitutionCatalog(ctx context.Context) ([]inventory.SKUCombination, map[string]inventory.SKUCombination, error) {
	items, err := s.inventory.ListActiveSKUCombinations(ctx)
	if err != nil {
		return nil, nil, err
	}
	active := make([]inventory.SKUCombination, 0, len(items))
	bySKU := make(map[string]inventory.SKUCombination, len(items))
	for _, item := range items {
		item.SubstituteForSKU = strings.TrimSpace(item.SubstituteForSKU)
		if !item.Enabled || item.SubstituteForSKU == "" || len(item.Items) == 0 {
			continue
		}
		active = append(active, item)
		bySKU[item.SubstituteForSKU] = item
	}
	return active, bySKU, nil
}

func orderQuantities(order model.Order) map[string]int {
	result := make(map[string]int)
	for _, line := range order.Lines {
		if sku := strings.TrimSpace(line.ExtCode); sku != "" && line.Quantity > 0 {
			result[sku] += line.Quantity
		}
	}
	return result
}

func expandSubstitutionQuantities(combination inventory.SKUCombination, multiplier int) map[string]int {
	result := make(map[string]int, len(combination.Items))
	for _, item := range combination.Items {
		if sku := strings.TrimSpace(item.WarehouseSKU); sku != "" && item.Quantity > 0 && multiplier > 0 {
			result[sku] += item.Quantity * multiplier
		}
	}
	return result
}

func quantityItems(quantities map[string]int) []inventory.PackageSpecResolveRequest {
	result := make([]inventory.PackageSpecResolveRequest, 0, len(quantities))
	for sku, quantity := range quantities {
		result = append(result, inventory.PackageSpecResolveRequest{WarehouseSKU: sku, Quantity: quantity})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].WarehouseSKU < result[j].WarehouseSKU })
	return result
}

func (s *Service) compareDirectOption(ctx context.Context, order model.Order, quantities map[string]int, policies map[string][]model.CarrierPolicy) SubstitutionPriceOption {
	option := SubstitutionPriceOption{Kind: "direct", Items: quantityItems(quantities), Quotes: []SubstitutionPriceQuote{}}
	decision, err := s.queryInventory(ctx, quantities)
	if err != nil {
		option.Reason = err.Error()
		return option
	}
	if err := s.applyShopSKUWarehouseRules(ctx, &decision); err != nil {
		option.Reason = err.Error()
		return option
	}
	packageSpec, err := packageSpecFromResolution(decision.PackageResolution)
	if err != nil {
		option.Reason = err.Error()
		return option
	}
	option.Package = &packageSpec
	return s.quoteSubstitutionOption(ctx, order, quantities, decision, packageSpec, option, policies, nil)
}

func (s *Service) compareReplacementOption(ctx context.Context, order model.Order, combination inventory.SKUCombination, originalQuantity int, quantities map[string]int, policies map[string][]model.CarrierPolicy) SubstitutionPriceOption {
	option := SubstitutionPriceOption{Kind: "replacement", Items: quantityItems(quantities), Quotes: []SubstitutionPriceQuote{}}
	if err := validateSubstitutionCombination(combination); err != nil {
		option.Reason = err.Error()
		return option
	}
	decision, err := s.queryInventory(ctx, quantities)
	if err != nil {
		option.Reason = err.Error()
		return option
	}
	if err := s.applyShopSKUWarehouseRules(ctx, &decision); err != nil {
		option.Reason = err.Error()
		return option
	}
	if originalQuantity != 1 {
		option.Reason = "多件订单缺少已确认的整单包裹规格，价格留空"
		return option
	}
	packageSpec, err := packageSpecFromResolution(inventory.PackageResolution{Complete: true, Package: &inventory.PackageSpec{
		WarehouseSKU: combination.SubstituteForSKU, Weight: combination.WeightKG, WeightUnit: "kg",
		Length: combination.LengthCM, Width: combination.WidthCM, Height: combination.HeightCM, DimensionUnit: "cm",
	}})
	if err != nil {
		option.Reason = err.Error()
		return option
	}
	option.Package = &packageSpec
	return s.quoteSubstitutionOption(ctx, order, quantities, decision, packageSpec, option, policies, &combination)
}

func (s *Service) quoteSubstitutionOption(ctx context.Context, order model.Order, quantities map[string]int, decision inventory.DecisionResponse, packageSpec model.PackageSpec, option SubstitutionPriceOption, policies map[string][]model.CarrierPolicy, combination *inventory.SKUCombination) SubstitutionPriceOption {
	type result struct {
		selection inventory.Selection
		warehouse model.Warehouse
		channels  temu.ShippingServicesResult
		err       error
	}
	results := make(chan result, len(supportedOMSWarehouseKeys))
	jobs := 0
	problems := make([]error, 0)
	candidates := make([]substitutionPriceCandidate, 0)
	pairingByAccount := make(map[string]inventory.ProductPairingValidation)
	for _, key := range supportedOMSWarehouseKeys {
		selection, err := selectWarehouseForPriceComparison(decision, quantities, key)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", key, err))
			continue
		}
		if combination != nil {
			mapping, mapErr := s.store.WarehouseMapping(ctx, key)
			if mapErr != nil || !mapping.Enabled || strings.TrimSpace(mapping.OMSAccount) == "" {
				problems = append(problems, fmt.Errorf("%s: 仓库未配置可用的 OMS 账户", key))
				continue
			}
			account := strings.TrimSpace(mapping.OMSAccount)
			validation, exists := pairingByAccount[account]
			if !exists {
				validation, mapErr = s.inventory.ValidateProductPairing(ctx, substitutionOMSPairingRequest(account, *combination))
				if mapErr != nil {
					problems = append(problems, fmt.Errorf("%s: 无法校验 OMS 产品配对: %w", key, mapErr))
					continue
				}
				pairingByAccount[account] = validation
			}
			if !validation.Ready {
				reason := strings.TrimSpace(validation.Reason)
				if reason == "" {
					reason = "OMS 产品配对不可用"
				}
				problems = append(problems, fmt.Errorf("%s: %s", key, reason))
				continue
			}
		}
		warehouse, err := s.store.MappedWarehouse(ctx, key)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: warehouse mapping unavailable", key))
			continue
		}
		if !warehouse.EnableBuyShippingLabel {
			problems = append(problems, fmt.Errorf("%s: Temu warehouse does not support Buy Label", key))
			continue
		}
		jobs++
		go func(selected inventory.Selection, mapped model.Warehouse) {
			channels, _, err := s.temu.ShippingServices(ctx, shippingServicesRequest(order, mapped.ID, packageSpec))
			results <- result{selection: selected, warehouse: mapped, channels: channels, err: err}
		}(selection, warehouse)
	}
	for range jobs {
		current := <-results
		if current.err != nil {
			problems = append(problems, fmt.Errorf("%s: 物流询价失败: %w", current.selection.WarehouseKey, current.err))
			continue
		}
		allowed, _ := filterAutomaticChannels(current.channels.Available)
		allowed, _ = filterChannelsByCarrierPolicy(allowed, current.selection.WarehouseKey, policies[current.selection.WarehouseKey])
		if len(allowed) == 0 {
			problems = append(problems, fmt.Errorf("%s: 没有符合规则的 Temu 面单渠道", current.selection.WarehouseKey))
			continue
		}
		for _, channel := range allowed {
			amount := price(channel.EstimatedAmount)
			if math.IsInf(amount, 1) {
				continue
			}
			currency := strings.ToUpper(strings.TrimSpace(channel.EstimatedCurrencyCode))
			if currency == "" {
				currency = "USD"
			}
			candidates = append(candidates, substitutionPriceCandidate{
				candidate: autoChannelCandidate{
					warehouseKey: current.selection.WarehouseKey, temuWarehouseID: current.warehouse.ID,
					channel: channel, amount: amount,
					priority: configuredCarrierPriority(policies[current.selection.WarehouseKey], carrierCode(channel)),
				},
				quote: SubstitutionPriceQuote{
					WarehouseKey: current.selection.WarehouseKey, WarehouseName: current.selection.WarehouseName,
					TemuWarehouseID: current.warehouse.ID, TemuWarehouseName: current.warehouse.Name,
					ShippingCompany: channel.ShippingCompanyName, ShipLogisticsType: channel.ShipLogisticsType,
					ChannelID: channel.ChannelID, ShipCompanyID: channel.ShipCompanyID, Amount: amount, Currency: currency,
				},
			})
		}
	}
	option.Quotes = selectSubstitutionPriceQuotes(candidates)
	option.Problems = substitutionProblemMessages(problems)
	if len(option.Quotes) == 0 {
		if len(option.Problems) > 0 {
			option.Reason = strings.Join(option.Problems, "；")
		} else {
			option.Reason = "没有仓库同时满足实际库存、启用映射和面单渠道要求"
		}
		if len(problems) > 0 && s.logger != nil {
			s.logger.Info("substitution price option unavailable", "parent_order_sn", order.ParentOrderSN, "kind", option.Kind, "error", errors.Join(problems...).Error())
		}
		return option
	}
	option.Available = true
	best := option.Quotes[0]
	option.BestQuote = &best
	return option
}

func selectSubstitutionPriceQuotes(candidates []substitutionPriceCandidate) []SubstitutionPriceQuote {
	byWarehouse := make(map[string][]substitutionPriceCandidate)
	for _, item := range candidates {
		byWarehouse[item.candidate.warehouseKey] = append(byWarehouse[item.candidate.warehouseKey], item)
	}
	warehouseKeys := make([]string, 0, len(byWarehouse))
	for warehouseKey := range byWarehouse {
		warehouseKeys = append(warehouseKeys, warehouseKey)
	}
	sort.Strings(warehouseKeys)

	warehouseChoices := make([]substitutionPriceCandidate, 0, len(warehouseKeys))
	for _, warehouseKey := range warehouseKeys {
		items := byWarehouse[warehouseKey]
		automatic := make([]autoChannelCandidate, 0, len(items))
		for _, item := range items {
			automatic = append(automatic, item.candidate)
		}
		choice, _, err := selectAutomaticChannel(automatic, 0)
		if err != nil {
			continue
		}
		for _, item := range items {
			if sameChannelCandidate(item.candidate, choice) {
				warehouseChoices = append(warehouseChoices, item)
				break
			}
		}
	}
	if len(warehouseChoices) == 0 {
		return []SubstitutionPriceQuote{}
	}

	automatic := make([]autoChannelCandidate, 0, len(warehouseChoices))
	for _, item := range warehouseChoices {
		automatic = append(automatic, item.candidate)
	}
	best, _, _ := selectAutomaticChannel(automatic, 0)
	sort.SliceStable(warehouseChoices, func(i, j int) bool {
		leftBest := sameChannelCandidate(warehouseChoices[i].candidate, best)
		rightBest := sameChannelCandidate(warehouseChoices[j].candidate, best)
		if leftBest != rightBest {
			return leftBest
		}
		return betterChannelCandidate(warehouseChoices[i].candidate, warehouseChoices[j].candidate)
	})

	quotes := make([]SubstitutionPriceQuote, 0, len(warehouseChoices))
	for _, item := range warehouseChoices {
		quotes = append(quotes, item.quote)
	}
	return quotes
}

func substitutionOMSPairingRequest(account string, combination inventory.SKUCombination) inventory.ProductPairingValidationRequest {
	quantities := expandSubstitutionQuantities(combination, 1)
	items := make([]inventory.ProductPairingValidationItem, 0, len(quantities))
	for _, sku := range sortedKeys(quantities) {
		items = append(items, inventory.ProductPairingValidationItem{SystemSKU: sku, Quantity: quantities[sku]})
	}
	return inventory.ProductPairingValidationRequest{
		Account: account, PlatformSKU: strings.TrimSpace(combination.SubstituteForSKU), Items: items,
	}
}

func substitutionProblemMessages(problems []error) []string {
	result := make([]string, 0, len(problems))
	seen := make(map[string]bool, len(problems))
	for _, problem := range problems {
		if problem == nil {
			continue
		}
		message := strings.TrimSpace(problem.Error())
		if message == "" || seen[message] {
			continue
		}
		seen[message] = true
		result = append(result, message)
	}
	return result
}

func validateSubstitutionCombination(combination inventory.SKUCombination) error {
	if !combination.Enabled || strings.TrimSpace(combination.SubstituteForSKU) == "" || len(combination.Items) == 0 {
		return errors.New("替代组合未启用或没有配置组合成员")
	}
	for name, value := range map[string]float64{
		"长度": combination.LengthCM, "宽度": combination.WidthCM,
		"高度": combination.HeightCM, "重量": combination.WeightKG,
	} {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("替代组合缺少有效的%s配置", name)
		}
	}
	for _, item := range combination.Items {
		if strings.TrimSpace(item.WarehouseSKU) == "" || item.Quantity <= 0 {
			return errors.New("替代组合包含无效的系统 SKU 或数量")
		}
	}
	return nil
}

func selectWarehouseForPriceComparison(decision inventory.DecisionResponse, quantities map[string]int, warehouseKey string) (inventory.Selection, error) {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	region := warehouseRegion(warehouseKey)
	if region == "" {
		return inventory.Selection{}, errors.New("unknown warehouse")
	}
	name := warehouseKey
	recordsBySKU := make(map[string]inventory.SKUDecision, len(decision.Records))
	for _, record := range decision.Records {
		recordsBySKU[strings.TrimSpace(record.SKU)] = record
	}
	for sku, required := range quantities {
		record, exists := recordsBySKU[sku]
		if !exists {
			return inventory.Selection{}, fmt.Errorf("库存查询没有返回组合 SKU %s，禁止从仓库 %s 发货", sku, warehouseKey)
		}
		var selected *inventory.Warehouse
		for _, currentRegion := range record.Regions {
			if currentRegion.Region != region {
				continue
			}
			for index := range currentRegion.Warehouses {
				if currentRegion.Warehouses[index].Key == warehouseKey {
					selected = &currentRegion.Warehouses[index]
					break
				}
			}
		}
		if selected == nil || !selected.Selectable || selected.Available < float64(required) {
			return inventory.Selection{}, fmt.Errorf("SKU %s 在仓库 %s 不可选或可用库存不足 %d 件", sku, warehouseKey, required)
		}
		if selected.Name != "" {
			name = selected.Name
		}
	}
	return inventory.Selection{Region: region, WarehouseKey: warehouseKey, WarehouseName: name, Decision: decision}, nil
}
