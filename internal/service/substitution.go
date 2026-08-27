package service

import (
	"context"
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
	Amount            float64 `json:"amount"`
	Currency          string  `json:"currency"`
}

type SubstitutionPriceOption struct {
	Kind      string                                `json:"kind"`
	Items     []inventory.PackageSpecResolveRequest `json:"items"`
	Available bool                                  `json:"available"`
	Reason    string                                `json:"reason,omitempty"`
	Package   *model.PackageSpec                    `json:"package,omitempty"`
	BestQuote *SubstitutionPriceQuote               `json:"best_quote,omitempty"`
	Quotes    []SubstitutionPriceQuote              `json:"quotes"`
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
		quantities := orderQuantities(order)
		matches := make([]SubstitutionOrderMatch, 0)
		for _, sku := range targets {
			if quantities[sku] > 0 {
				matches = append(matches, SubstitutionOrderMatch{WarehouseSKU: sku, Quantity: quantities[sku], Combination: bySKU[sku]})
			}
		}
		result = append(result, SubstitutionOrderCandidate{Order: order, Matches: matches})
	}
	return result, total, len(combinations), nil
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
	return s.quoteSubstitutionOption(ctx, order, quantities, decision, packageSpec, option, policies)
}

func (s *Service) compareReplacementOption(ctx context.Context, order model.Order, combination inventory.SKUCombination, originalQuantity int, quantities map[string]int, policies map[string][]model.CarrierPolicy) SubstitutionPriceOption {
	option := SubstitutionPriceOption{Kind: "replacement", Items: quantityItems(quantities), Quotes: []SubstitutionPriceQuote{}}
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
	return s.quoteSubstitutionOption(ctx, order, quantities, decision, packageSpec, option, policies)
}

func (s *Service) quoteSubstitutionOption(ctx context.Context, order model.Order, quantities map[string]int, decision inventory.DecisionResponse, packageSpec model.PackageSpec, option SubstitutionPriceOption, policies map[string][]model.CarrierPolicy) SubstitutionPriceOption {
	type result struct {
		selection inventory.Selection
		warehouse model.Warehouse
		channels  temu.ShippingServicesResult
		err       error
	}
	results := make(chan result, len(supportedOMSWarehouseKeys))
	jobs := 0
	problems := make([]error, 0)
	for _, key := range supportedOMSWarehouseKeys {
		selection, err := selectWarehouseForPriceComparison(decision, quantities, key)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", key, err))
			continue
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
			problems = append(problems, current.err)
			continue
		}
		allowed, _ := filterAutomaticChannels(current.channels.Available)
		allowed, _ = filterChannelsByCarrierPolicy(allowed, current.selection.WarehouseKey, policies[current.selection.WarehouseKey])
		for _, channel := range allowed {
			amount := price(channel.EstimatedAmount)
			if math.IsInf(amount, 1) {
				continue
			}
			currency := strings.ToUpper(strings.TrimSpace(channel.EstimatedCurrencyCode))
			if currency == "" {
				currency = "USD"
			}
			option.Quotes = append(option.Quotes, SubstitutionPriceQuote{
				WarehouseKey: current.selection.WarehouseKey, WarehouseName: current.selection.WarehouseName,
				TemuWarehouseID: current.warehouse.ID, TemuWarehouseName: current.warehouse.Name,
				ShippingCompany: channel.ShippingCompanyName, ShipLogisticsType: channel.ShipLogisticsType,
				ChannelID: channel.ChannelID, Amount: amount, Currency: currency,
			})
		}
	}
	sort.SliceStable(option.Quotes, func(i, j int) bool {
		if math.Abs(option.Quotes[i].Amount-option.Quotes[j].Amount) > 0.000001 {
			return option.Quotes[i].Amount < option.Quotes[j].Amount
		}
		return option.Quotes[i].WarehouseKey < option.Quotes[j].WarehouseKey
	})
	if len(option.Quotes) > 0 {
		cheapestByWarehouse := make([]SubstitutionPriceQuote, 0, len(supportedOMSWarehouseKeys))
		seenWarehouses := make(map[string]bool, len(supportedOMSWarehouseKeys))
		for _, quote := range option.Quotes {
			if seenWarehouses[quote.WarehouseKey] {
				continue
			}
			seenWarehouses[quote.WarehouseKey] = true
			cheapestByWarehouse = append(cheapestByWarehouse, quote)
		}
		option.Quotes = cheapestByWarehouse
	}
	if len(option.Quotes) == 0 {
		option.Reason = "没有仓库同时满足实际库存、启用映射和面单渠道要求"
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

func selectWarehouseForPriceComparison(decision inventory.DecisionResponse, quantities map[string]int, warehouseKey string) (inventory.Selection, error) {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	region := warehouseRegion(warehouseKey)
	if region == "" {
		return inventory.Selection{}, errors.New("unknown warehouse")
	}
	name := warehouseKey
	for _, record := range decision.Records {
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
		if selected == nil || !selected.Selectable || selected.Available < float64(quantities[record.SKU]) {
			return inventory.Selection{}, fmt.Errorf("SKU %s stock cannot cover quantity %d", record.SKU, quantities[record.SKU])
		}
		if selected.Name != "" {
			name = selected.Name
		}
	}
	return inventory.Selection{Region: region, WarehouseKey: warehouseKey, WarehouseName: name, Decision: decision}, nil
}
