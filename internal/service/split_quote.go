package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
)

const (
	DefaultSplitPackageCount = 3
	maxSplitPackageCount     = 20
	maxSplitOrderUnits       = 500
)

var splitCarrierCodes = []string{"GOFO", "USPS", "FEDEX"}
var splitWarehouseKeys = []string{"ARP_EAST", "DPS002", "DPS004"}

type SplitPlanRequest struct {
	ParentOrderSN string `json:"parent_order_sn"`
	PackageCount  int    `json:"package_count"`
	WarehouseKey  string `json:"warehouse_key,omitempty"`
}

type SplitWarehouseOption struct {
	WarehouseKey     string `json:"warehouse_key"`
	WarehouseID      string `json:"warehouse_id"`
	WarehouseName    string `json:"warehouse_name"`
	OMSWarehouseCode string `json:"oms_warehouse_code"`
}

type SplitPackageItem struct {
	OrderSN      string `json:"order_sn"`
	WarehouseSKU string `json:"warehouse_sku"`
	ProductName  string `json:"product_name,omitempty"`
	Quantity     int    `json:"quantity"`
	GoodsID      int64  `json:"goods_id,omitempty"`
	SKUID        int64  `json:"sku_id,omitempty"`
}

type SplitPackage struct {
	Number           int                `json:"number"`
	Items            []SplitPackageItem `json:"items"`
	WeightKG         float64            `json:"weight_kg"`
	LengthCM         float64            `json:"length_cm"`
	WidthCM          float64            `json:"width_cm"`
	HeightCM         float64            `json:"height_cm"`
	Estimated        bool               `json:"estimated"`
	NeedsMeasurement bool               `json:"needs_measurement"`
}

type SplitOrderSummary struct {
	ParentOrderSN string            `json:"parent_order_sn"`
	Status        int               `json:"status"`
	TotalUnits    int               `json:"total_units"`
	Lines         []model.OrderLine `json:"lines"`
}

type SplitPlanResult struct {
	Order                SplitOrderSummary      `json:"order"`
	PackageCount         int                    `json:"package_count"`
	Packages             []SplitPackage         `json:"packages"`
	Warehouses           []SplitWarehouseOption `json:"warehouses"`
	SelectedWarehouseKey string                 `json:"selected_warehouse_key"`
	Warnings             []string               `json:"warnings"`
}

type SplitQuoteRequest struct {
	ParentOrderSN string         `json:"parent_order_sn"`
	WarehouseKey  string         `json:"warehouse_key"`
	Carriers      []string       `json:"carriers"`
	Packages      []SplitPackage `json:"packages"`
}

type SplitCarrierQuote struct {
	CarrierCode             string  `json:"carrier_code"`
	Available               bool    `json:"available"`
	ChannelID               int64   `json:"channel_id,omitempty"`
	ShipCompanyID           int64   `json:"ship_company_id,omitempty"`
	ShippingCompanyName     string  `json:"shipping_company_name,omitempty"`
	ShipLogisticsType       string  `json:"ship_logistics_type,omitempty"`
	Amount                  float64 `json:"amount,omitempty"`
	Currency                string  `json:"currency,omitempty"`
	EstimatedText           string  `json:"estimated_text,omitempty"`
	SignatureRequired       bool    `json:"signature_required"`
	SignServiceID           int64   `json:"sign_service_id,omitempty"`
	SignServiceName         string  `json:"sign_service_name,omitempty"`
	ProofOfDeliveryIncluded bool    `json:"proof_of_delivery_included"`
	UnavailableReason       string  `json:"unavailable_reason,omitempty"`
}

type SplitPackageQuote struct {
	Package            SplitPackage        `json:"package"`
	Carriers           []SplitCarrierQuote `json:"carriers"`
	RecommendedCarrier string              `json:"recommended_carrier,omitempty"`
	RecommendedAmount  *float64            `json:"recommended_amount,omitempty"`
}

type SplitCarrierTotal struct {
	CarrierCode         string   `json:"carrier_code"`
	Available           bool     `json:"available"`
	Amount              *float64 `json:"amount,omitempty"`
	Currency            string   `json:"currency,omitempty"`
	SignatureRequired   bool     `json:"signature_required"`
	UnavailablePackages []int    `json:"unavailable_packages,omitempty"`
}

type SplitQuoteResult struct {
	ParentOrderSN string               `json:"parent_order_sn"`
	Warehouse     SplitWarehouseOption `json:"warehouse"`
	Packages      []SplitPackageQuote  `json:"packages"`
	CarrierTotals []SplitCarrierTotal  `json:"carrier_totals"`
	MixedTotal    *float64             `json:"mixed_total,omitempty"`
	Currency      string               `json:"currency"`
	QueriedAt     time.Time            `json:"queried_at"`
}

func (s *Service) PrepareSplitPlan(ctx context.Context, request SplitPlanRequest) (SplitPlanResult, error) {
	order, err := s.liveSplitOrder(ctx, request.ParentOrderSN)
	if err != nil {
		return SplitPlanResult{}, err
	}
	packageCount := request.PackageCount
	if packageCount == 0 {
		packageCount = DefaultSplitPackageCount
	}
	totalUnits := splitOrderUnits(order)
	if packageCount < 2 || packageCount > maxSplitPackageCount {
		return SplitPlanResult{}, fmt.Errorf("package_count must be between 2 and %d", maxSplitPackageCount)
	}
	if packageCount > totalUnits {
		return SplitPlanResult{}, errors.New("package_count cannot exceed the total order quantity")
	}
	if totalUnits > maxSplitOrderUnits {
		return SplitPlanResult{}, fmt.Errorf("orders over %d units require a manual split plan", maxSplitOrderUnits)
	}

	resolution, warnings, err := s.resolveSplitPackageSpecs(ctx, order)
	if err != nil {
		return SplitPlanResult{}, err
	}
	packages, packageWarnings, err := buildSplitPackages(order, resolution, packageCount)
	if err != nil {
		return SplitPlanResult{}, err
	}
	warnings = append(warnings, packageWarnings...)
	warehouses, selected, err := s.splitWarehouseOptions(ctx, order, request.WarehouseKey)
	if err != nil {
		return SplitPlanResult{}, err
	}
	return SplitPlanResult{
		Order:        SplitOrderSummary{ParentOrderSN: order.ParentOrderSN, Status: order.Status, TotalUnits: totalUnits, Lines: order.Lines},
		PackageCount: packageCount, Packages: packages, Warehouses: warehouses,
		SelectedWarehouseKey: selected, Warnings: warnings,
	}, nil
}

func (s *Service) QuoteSplitPlan(ctx context.Context, request SplitQuoteRequest) (SplitQuoteResult, error) {
	order, err := s.liveSplitOrder(ctx, request.ParentOrderSN)
	if err != nil {
		return SplitQuoteResult{}, err
	}
	packages, err := validateSplitPackages(order, request.Packages)
	if err != nil {
		return SplitQuoteResult{}, err
	}
	carriers, err := normalizeSplitCarriers(request.Carriers)
	if err != nil {
		return SplitQuoteResult{}, err
	}
	warehouseKey := strings.ToUpper(strings.TrimSpace(request.WarehouseKey))
	if warehouseKey == "" {
		return SplitQuoteResult{}, errors.New("warehouse_key is required")
	}
	if err := s.validateOrderWarehouseAllowed(ctx, order, warehouseKey); err != nil {
		return SplitQuoteResult{}, err
	}
	warehouse, err := s.store.MappedWarehouse(ctx, warehouseKey)
	if err != nil {
		return SplitQuoteResult{}, err
	}
	if !warehouse.EnableBuyShippingLabel {
		return SplitQuoteResult{}, fmt.Errorf("Temu warehouse %s does not support Buy Label", warehouse.Name)
	}
	mapping, err := s.store.WarehouseMapping(ctx, warehouseKey)
	if err != nil {
		return SplitQuoteResult{}, err
	}

	results, err := s.quoteSplitPackages(ctx, order, warehouse.ID, packages, carriers)
	if err != nil {
		return SplitQuoteResult{}, err
	}
	carrierTotals := splitCarrierTotals(results, carriers)
	mixedTotal := splitMixedTotal(results)
	return SplitQuoteResult{
		ParentOrderSN: order.ParentOrderSN,
		Warehouse:     SplitWarehouseOption{WarehouseKey: warehouseKey, WarehouseID: warehouse.ID, WarehouseName: warehouse.Name, OMSWarehouseCode: mapping.OMSWarehouseCode},
		Packages:      results, CarrierTotals: carrierTotals, MixedTotal: mixedTotal,
		Currency: "USD", QueriedAt: time.Now(),
	}, nil
}

func (s *Service) liveSplitOrder(ctx context.Context, parentOrderSN string) (model.Order, error) {
	parentOrderSN = strings.TrimSpace(parentOrderSN)
	if parentOrderSN == "" {
		return model.Order{}, errors.New("parent_order_sn is required")
	}
	result, _, err := s.temu.OrderDetail(ctx, parentOrderSN)
	if err != nil {
		return model.Order{}, err
	}
	order := normalizeOrder(result)
	if strings.TrimSpace(order.ParentOrderSN) == "" {
		return model.Order{}, errors.New("Temu returned an empty parent order")
	}
	if order.ParentOrderSN != parentOrderSN {
		return model.Order{}, errors.New("Temu returned a different parent order")
	}
	if order.Status != 2 {
		return model.Order{}, errOrderNoLongerAwaitingShipment
	}
	if len(order.Lines) == 0 || splitOrderUnits(order) == 0 {
		return model.Order{}, errors.New("order has no shippable items")
	}
	return order, nil
}

func (s *Service) resolveSplitPackageSpecs(ctx context.Context, order model.Order) (inventory.PackageResolution, []string, error) {
	quantities := make(map[string]int)
	warnings := make([]string, 0)
	for _, line := range order.Lines {
		sku := strings.TrimSpace(line.ExtCode)
		if sku == "" {
			warnings = append(warnings, fmt.Sprintf("子订单 %s 缺少仓库 SKU，必须人工填写包裹重量和尺寸", line.OrderSN))
			continue
		}
		quantities[sku] += line.Quantity
	}
	if len(quantities) == 0 {
		return inventory.PackageResolution{}, warnings, nil
	}
	skus := make([]string, 0, len(quantities))
	for sku := range quantities {
		skus = append(skus, sku)
	}
	sort.Strings(skus)
	items := make([]inventory.PackageSpecResolveRequest, 0, len(skus))
	for _, sku := range skus {
		items = append(items, inventory.PackageSpecResolveRequest{WarehouseSKU: sku, Quantity: quantities[sku]})
	}
	resolution, err := s.inventory.ResolvePackageSpecs(ctx, items)
	if err != nil {
		return inventory.PackageResolution{}, nil, err
	}
	if len(resolution.MissingSKUs) > 0 {
		warnings = append(warnings, "以下仓库 SKU 缺少完整规格，相关包裹必须人工测量: "+strings.Join(resolution.MissingSKUs, "、"))
	}
	return resolution, warnings, nil
}

func (s *Service) splitWarehouseOptions(ctx context.Context, order model.Order, requested string) ([]SplitWarehouseOption, string, error) {
	warehouses, mappings, err := s.store.ListWarehouses(ctx)
	if err != nil {
		return nil, "", err
	}
	byID := make(map[string]model.Warehouse, len(warehouses))
	for _, warehouse := range warehouses {
		byID[warehouse.ID] = warehouse
	}
	byKey := make(map[string]model.WarehouseMapping, len(mappings))
	for _, mapping := range mappings {
		byKey[strings.ToUpper(mapping.OMSKey)] = mapping
	}
	skus := make([]string, 0, len(order.Lines))
	for _, line := range order.Lines {
		if sku := strings.TrimSpace(line.ExtCode); sku != "" {
			skus = append(skus, sku)
		}
	}
	disabled, err := s.inventory.DisabledWarehouseKeys(ctx, "temu", skus)
	if err != nil {
		return nil, "", err
	}
	options := make([]SplitWarehouseOption, 0, len(splitWarehouseKeys))
	for _, key := range splitWarehouseKeys {
		mapping, ok := byKey[key]
		if !ok {
			continue
		}
		warehouse, ok := byID[mapping.TemuWarehouseID]
		if !ok || !warehouse.EnableBuyShippingLabel || splitWarehouseDisabledForOrder(key, order, disabled) {
			continue
		}
		options = append(options, SplitWarehouseOption{
			WarehouseKey: key, WarehouseID: warehouse.ID, WarehouseName: warehouse.Name, OMSWarehouseCode: mapping.OMSWarehouseCode,
		})
	}
	if len(options) == 0 {
		return nil, "", errors.New("no mapped Temu Buy Label warehouse is available for every order SKU")
	}
	requested = strings.ToUpper(strings.TrimSpace(requested))
	if requested == "" {
		return options, options[0].WarehouseKey, nil
	}
	for _, option := range options {
		if option.WarehouseKey == requested {
			return options, requested, nil
		}
	}
	return nil, "", fmt.Errorf("warehouse %s is not available for this split plan", requested)
}

func splitWarehouseDisabledForOrder(key string, order model.Order, disabled map[string]map[string]bool) bool {
	for _, line := range order.Lines {
		if disabled[strings.TrimSpace(line.ExtCode)][key] {
			return true
		}
	}
	return false
}

type splitUnit struct {
	line     model.OrderLine
	spec     inventory.PackageResolutionItem
	complete bool
}

type splitPackageBuild struct {
	packageValue SplitPackage
	units        int
	missing      bool
}

func buildSplitPackages(order model.Order, resolution inventory.PackageResolution, packageCount int) ([]SplitPackage, []string, error) {
	if packageCount < 2 || packageCount > maxSplitPackageCount {
		return nil, nil, fmt.Errorf("package count must be between 2 and %d", maxSplitPackageCount)
	}
	specs := make(map[string]inventory.PackageResolutionItem, len(resolution.Items))
	for _, item := range resolution.Items {
		specs[item.WarehouseSKU] = item
	}
	units := make([]splitUnit, 0, splitOrderUnits(order))
	for _, line := range order.Lines {
		spec := specs[strings.TrimSpace(line.ExtCode)]
		complete := spec.Complete && spec.WeightKG != nil && spec.LengthCM != nil && spec.WidthCM != nil && spec.HeightCM != nil
		for quantity := 0; quantity < line.Quantity; quantity++ {
			units = append(units, splitUnit{line: line, spec: spec, complete: complete})
		}
	}
	if packageCount > len(units) {
		return nil, nil, errors.New("package count cannot exceed order quantity")
	}
	sort.SliceStable(units, func(i, j int) bool {
		if units[i].complete != units[j].complete {
			return units[i].complete
		}
		leftWeight, rightWeight := 0.0, 0.0
		if units[i].complete {
			leftWeight = *units[i].spec.WeightKG
		}
		if units[j].complete {
			rightWeight = *units[j].spec.WeightKG
		}
		if leftWeight != rightWeight {
			return leftWeight > rightWeight
		}
		return units[i].line.OrderSN < units[j].line.OrderSN
	})
	builds := make([]splitPackageBuild, packageCount)
	for index := range builds {
		builds[index].packageValue = SplitPackage{Number: index + 1, Items: []SplitPackageItem{}, Estimated: true}
	}
	for _, unit := range units {
		target := 0
		for index := 1; index < len(builds); index++ {
			left, right := builds[index], builds[target]
			if left.packageValue.WeightKG < right.packageValue.WeightKG ||
				(left.packageValue.WeightKG == right.packageValue.WeightKG && left.units < right.units) {
				target = index
			}
		}
		addSplitUnit(&builds[target], unit)
	}
	warnings := make([]string, 0, 1)
	packages := make([]SplitPackage, 0, len(builds))
	for _, build := range builds {
		item := build.packageValue
		if build.missing {
			item.WeightKG, item.LengthCM, item.WidthCM, item.HeightCM = 0, 0, 0, 0
			item.NeedsMeasurement = true
		} else {
			item.WeightKG = roundTo(item.WeightKG, 3)
			item.LengthCM = roundTo(item.LengthCM, 2)
			item.WidthCM = roundTo(item.WidthCM, 2)
			item.HeightCM = roundTo(item.HeightCM, 2)
		}
		packages = append(packages, item)
	}
	warnings = append(warnings, "自动尺寸按单件包装短边叠放估算；询价前请按实际装箱毛重和三边尺寸修正")
	return packages, warnings, nil
}

func addSplitUnit(build *splitPackageBuild, unit splitUnit) {
	itemIndex := -1
	for index := range build.packageValue.Items {
		if build.packageValue.Items[index].OrderSN == unit.line.OrderSN {
			itemIndex = index
			break
		}
	}
	if itemIndex < 0 {
		build.packageValue.Items = append(build.packageValue.Items, SplitPackageItem{
			OrderSN: unit.line.OrderSN, WarehouseSKU: unit.line.ExtCode, ProductName: unit.line.GoodsName,
			GoodsID: unit.line.GoodsID, SKUID: unit.line.SKUID,
		})
		itemIndex = len(build.packageValue.Items) - 1
	}
	build.packageValue.Items[itemIndex].Quantity++
	build.units++
	if !unit.complete {
		build.missing = true
		return
	}
	dimensions := []float64{*unit.spec.LengthCM, *unit.spec.WidthCM, *unit.spec.HeightCM}
	sort.Sort(sort.Reverse(sort.Float64Slice(dimensions)))
	build.packageValue.WeightKG += *unit.spec.WeightKG
	build.packageValue.LengthCM = math.Max(build.packageValue.LengthCM, dimensions[0])
	build.packageValue.WidthCM = math.Max(build.packageValue.WidthCM, dimensions[1])
	build.packageValue.HeightCM += dimensions[2]
}

func splitOrderUnits(order model.Order) int {
	total := 0
	for _, line := range order.Lines {
		if line.Quantity > 0 {
			total += line.Quantity
		}
	}
	return total
}

func roundTo(value float64, decimals int) float64 {
	factor := math.Pow10(decimals)
	return math.Ceil(value*factor) / factor
}

func roundCurrency(value float64) float64 {
	return math.Round(value*100) / 100
}

func validateSplitPackages(order model.Order, requested []SplitPackage) ([]SplitPackage, error) {
	if len(requested) < 2 || len(requested) > maxSplitPackageCount {
		return nil, fmt.Errorf("packages must contain between 2 and %d packages", maxSplitPackageCount)
	}
	lines := make(map[string]model.OrderLine, len(order.Lines))
	expected := make(map[string]int, len(order.Lines))
	for _, line := range order.Lines {
		lines[line.OrderSN] = line
		expected[line.OrderSN] = line.Quantity
	}
	actual := make(map[string]int, len(lines))
	result := make([]SplitPackage, 0, len(requested))
	for index, raw := range requested {
		if err := validateSplitMetricPackage(raw); err != nil {
			return nil, fmt.Errorf("package %d: %w", index+1, err)
		}
		if len(raw.Items) == 0 {
			return nil, fmt.Errorf("package %d has no order items", index+1)
		}
		itemSeen := make(map[string]bool)
		item := SplitPackage{Number: index + 1, WeightKG: raw.WeightKG, LengthCM: raw.LengthCM, WidthCM: raw.WidthCM, HeightCM: raw.HeightCM, Items: make([]SplitPackageItem, 0, len(raw.Items))}
		for _, input := range raw.Items {
			orderSN := strings.TrimSpace(input.OrderSN)
			line, ok := lines[orderSN]
			if !ok {
				return nil, fmt.Errorf("package %d contains unknown order_sn %s", index+1, orderSN)
			}
			if input.Quantity <= 0 {
				return nil, fmt.Errorf("package %d item quantity must be positive", index+1)
			}
			if itemSeen[orderSN] {
				return nil, fmt.Errorf("package %d repeats order_sn %s", index+1, orderSN)
			}
			itemSeen[orderSN] = true
			actual[orderSN] += input.Quantity
			item.Items = append(item.Items, SplitPackageItem{OrderSN: orderSN, WarehouseSKU: line.ExtCode, ProductName: line.GoodsName, Quantity: input.Quantity, GoodsID: line.GoodsID, SKUID: line.SKUID})
		}
		result = append(result, item)
	}
	for orderSN, quantity := range expected {
		if actual[orderSN] != quantity {
			return nil, fmt.Errorf("order_sn %s allocates %d units; expected %d", orderSN, actual[orderSN], quantity)
		}
	}
	return result, nil
}

func validateSplitMetricPackage(item SplitPackage) error {
	for name, value := range map[string]float64{"weight_kg": item.WeightKG, "length_cm": item.LengthCM, "width_cm": item.WidthCM, "height_cm": item.HeightCM} {
		if value <= 0 || value > 10_000 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s must be a positive finite value no greater than 10000", name)
		}
	}
	return nil
}

func normalizeSplitCarriers(values []string) ([]string, error) {
	if len(values) == 0 {
		values = splitCarrierCodes
	}
	allowed := map[string]bool{"GOFO": true, "USPS": true, "FEDEX": true}
	seen := make(map[string]bool)
	for _, raw := range values {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if !allowed[value] {
			return nil, fmt.Errorf("unsupported split quote carrier %s", raw)
		}
		seen[value] = true
	}
	result := make([]string, 0, len(seen))
	for _, code := range splitCarrierCodes {
		if seen[code] {
			result = append(result, code)
		}
	}
	return result, nil
}

type splitQuoteJob struct {
	packageIndex int
	signature    bool
}
type splitQuoteJobResult struct {
	packageIndex int
	signature    bool
	channels     temu.ShippingServicesResult
	err          error
}

func (s *Service) quoteSplitPackages(ctx context.Context, order model.Order, warehouseID string, packages []SplitPackage, carriers []string) ([]SplitPackageQuote, error) {
	unsigned, signed := false, false
	for _, code := range carriers {
		if code == "FEDEX" {
			signed = true
		} else {
			unsigned = true
		}
	}
	jobs := make([]splitQuoteJob, 0, len(packages)*2)
	for index := range packages {
		if unsigned {
			jobs = append(jobs, splitQuoteJob{packageIndex: index})
		}
		if signed {
			jobs = append(jobs, splitQuoteJob{packageIndex: index, signature: true})
		}
	}
	completed := make(chan splitQuoteJobResult, len(jobs))
	var group sync.WaitGroup
	for _, job := range jobs {
		group.Add(1)
		go func(job splitQuoteJob) {
			defer group.Done()
			spec, err := metricPackageSpec(packages[job.packageIndex])
			if err != nil {
				completed <- splitQuoteJobResult{packageIndex: job.packageIndex, signature: job.signature, err: err}
				return
			}
			request := splitShippingServicesRequest(order.ParentOrderSN, warehouseID, spec, packages[job.packageIndex].Items, job.signature)
			channels, _, err := s.temu.ShippingServices(ctx, request)
			completed <- splitQuoteJobResult{packageIndex: job.packageIndex, signature: job.signature, channels: channels, err: err}
		}(job)
	}
	group.Wait()
	close(completed)
	unsignedResults := make(map[int]temu.ShippingServicesResult)
	signedResults := make(map[int]temu.ShippingServicesResult)
	errorsFound := make([]error, 0)
	for item := range completed {
		if item.err != nil {
			errorsFound = append(errorsFound, item.err)
			continue
		}
		if item.signature {
			signedResults[item.packageIndex] = item.channels
		} else {
			unsignedResults[item.packageIndex] = item.channels
		}
	}
	if len(errorsFound) > 0 {
		return nil, errors.Join(errorsFound...)
	}
	results := make([]SplitPackageQuote, 0, len(packages))
	for index, item := range packages {
		packageResult := SplitPackageQuote{Package: item, Carriers: make([]SplitCarrierQuote, 0, len(carriers))}
		for _, code := range carriers {
			channels := unsignedResults[index]
			if code == "FEDEX" {
				channels = signedResults[index]
			}
			quote := selectSplitCarrierQuote(code, channels)
			packageResult.Carriers = append(packageResult.Carriers, quote)
			if quote.Available && (packageResult.RecommendedAmount == nil || quote.Amount < *packageResult.RecommendedAmount) {
				amount := quote.Amount
				packageResult.RecommendedAmount = &amount
				packageResult.RecommendedCarrier = quote.CarrierCode
			}
		}
		results = append(results, packageResult)
	}
	return results, nil
}

func metricPackageSpec(item SplitPackage) (model.PackageSpec, error) {
	if err := validateSplitMetricPackage(item); err != nil {
		return model.PackageSpec{}, err
	}
	totalOunces := int(math.Ceil(item.WeightKG * 35.27396195))
	pounds, ounces := totalOunces/16, totalOunces%16
	inches := func(value float64) string { return strconv.FormatFloat(math.Ceil(value/2.54*100)/100, 'f', -1, 64) }
	spec := model.PackageSpec{Weight: strconv.Itoa(pounds), WeightUnit: "lb", Length: inches(item.LengthCM), Width: inches(item.WidthCM), Height: inches(item.HeightCM), DimensionUnit: "in"}
	if ounces > 0 {
		spec.ExtendWeight, spec.ExtendWeightUnit = strconv.Itoa(ounces), "oz"
	}
	if err := validatePackage(spec); err != nil {
		return model.PackageSpec{}, err
	}
	return spec, nil
}

func splitShippingServicesRequest(parentOrderSN, warehouseID string, spec model.PackageSpec, items []SplitPackageItem, signature bool) map[string]any {
	request := packageFields(spec)
	request["warehouseId"] = warehouseID
	request["signatureOnDelivery"] = signature
	orderItems := make([]any, 0, len(items))
	for _, item := range items {
		value := map[string]any{"parentOrderSn": parentOrderSN, "orderSn": item.OrderSN, "quantity": item.Quantity}
		if item.GoodsID != 0 {
			value["goodsId"] = item.GoodsID
		}
		if item.SKUID != 0 {
			value["skuId"] = item.SKUID
		}
		orderItems = append(orderItems, value)
	}
	request["shipOrderInfoList"] = orderItems
	return request
}

func selectSplitCarrierQuote(code string, channels temu.ShippingServicesResult) SplitCarrierQuote {
	result := SplitCarrierQuote{CarrierCode: code, SignatureRequired: code == "FEDEX", ProofOfDeliveryIncluded: code == "GOFO"}
	bestAmount := math.Inf(1)
	for _, channel := range channels.Available {
		if carrierCode(channel) != code {
			continue
		}
		if code != "FEDEX" && channelNeedsSignature(channel) {
			continue
		}
		if channel.EstimatedCurrencyCode != "" && !strings.EqualFold(channel.EstimatedCurrencyCode, "USD") {
			continue
		}
		amount := price(channel.EstimatedAmount)
		if math.IsInf(amount, 1) || amount >= bestAmount {
			continue
		}
		bestAmount = amount
		result.Available = true
		result.ChannelID, result.ShipCompanyID = channel.ChannelID, channel.ShipCompanyID
		result.ShippingCompanyName, result.ShipLogisticsType = channel.ShippingCompanyName, channel.ShipLogisticsType
		result.Amount, result.Currency, result.EstimatedText = amount, "USD", channel.EstimatedText
		result.SignServiceID, result.SignServiceName = channel.SignServiceID, channel.SignServiceName
	}
	if result.Available {
		return result
	}
	for _, channel := range channels.Unavailable {
		if carrierCode(channel) == code && strings.TrimSpace(channel.UnavailableReason) != "" {
			result.UnavailableReason = strings.TrimSpace(channel.UnavailableReason)
			break
		}
	}
	if result.UnavailableReason == "" {
		if code == "FEDEX" {
			result.UnavailableReason = "当前包裹没有可用的 FEDEX 签名渠道"
		} else {
			result.UnavailableReason = "当前包裹尺寸或重量不支持该快递"
		}
	}
	return result
}

func splitCarrierTotals(packages []SplitPackageQuote, carriers []string) []SplitCarrierTotal {
	results := make([]SplitCarrierTotal, 0, len(carriers))
	for _, code := range carriers {
		total := 0.0
		missing := make([]int, 0)
		for _, item := range packages {
			found := false
			for _, quote := range item.Carriers {
				if quote.CarrierCode != code {
					continue
				}
				found = true
				if quote.Available {
					total += quote.Amount
				} else {
					missing = append(missing, item.Package.Number)
				}
				break
			}
			if !found {
				missing = append(missing, item.Package.Number)
			}
		}
		result := SplitCarrierTotal{
			CarrierCode:         code,
			Available:           len(missing) == 0,
			Currency:            "USD",
			SignatureRequired:   code == "FEDEX",
			UnavailablePackages: missing,
		}
		if result.Available {
			amount := roundCurrency(total)
			result.Amount = &amount
		}
		results = append(results, result)
	}
	return results
}

func splitMixedTotal(packages []SplitPackageQuote) *float64 {
	if len(packages) == 0 {
		return nil
	}
	total := 0.0
	for _, item := range packages {
		if item.RecommendedAmount == nil {
			return nil
		}
		total += *item.RecommendedAmount
	}
	amount := roundCurrency(total)
	return &amount
}
