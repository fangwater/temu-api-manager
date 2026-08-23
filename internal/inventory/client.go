package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	url        string
	httpClient *http.Client
}

func NewClient(url string, timeout time.Duration) *Client {
	return &Client{url: strings.TrimSpace(url), httpClient: &http.Client{Timeout: timeout}}
}

type Warehouse struct {
	Key             string  `json:"warehouse_key"`
	Code            string  `json:"wh_code"`
	Name            string  `json:"warehouse_name"`
	Region          string  `json:"region"`
	Provider        string  `json:"provider"`
	Active          bool    `json:"active"`
	QueryStatus     string  `json:"query_status"`
	SKUFound        bool    `json:"sku_found"`
	Available       float64 `json:"available_amount"`
	Selectable      bool    `json:"selectable"`
	Recommended     bool    `json:"recommended"`
	ReasonCode      string  `json:"reason_code"`
	Reason          string  `json:"reason"`
	ShopSKUDisabled bool    `json:"shop_sku_disabled,omitempty"`
}

type Region struct {
	Region                  string      `json:"region"`
	RegionName              string      `json:"region_name"`
	Available               float64     `json:"available_amount"`
	SafetyStockThreshold    float64     `json:"safety_stock_threshold"`
	RequiresManual          bool        `json:"requires_manual"`
	RecommendedWarehouseKey string      `json:"recommended_warehouse_key"`
	DecisionCode            string      `json:"decision_code"`
	Reason                  string      `json:"reason"`
	Warehouses              []Warehouse `json:"warehouses"`
}

type InventoryThresholds struct {
	EastThreshold  float64 `json:"east_threshold"`
	WestThreshold  float64 `json:"west_threshold"`
	TotalThreshold float64 `json:"total_threshold"`
}

type SKUDecision struct {
	SKU                  string              `json:"sku"`
	RequiresManual       bool                `json:"requires_manual"`
	ManualRegions        []string            `json:"manual_regions"`
	DecisionCode         string              `json:"decision_code"`
	Reason               string              `json:"reason"`
	TotalAvailableAmount float64             `json:"total_available_amount"`
	Thresholds           InventoryThresholds `json:"thresholds"`
	Regions              []Region            `json:"regions"`
}
type PackageResolutionItem struct {
	WarehouseSKU        string   `json:"warehouse_sku"`
	MatchedWarehouseSKU string   `json:"matched_warehouse_sku,omitempty"`
	MatchType           string   `json:"match_type,omitempty"`
	MatchCandidates     []string `json:"match_candidates,omitempty"`
	Quantity            int      `json:"quantity"`
	Matched             bool     `json:"matched"`
	Enabled             bool     `json:"enabled"`
	Complete            bool     `json:"complete"`
	LengthCM            *float64 `json:"length_cm,omitempty"`
	WidthCM             *float64 `json:"width_cm,omitempty"`
	HeightCM            *float64 `json:"height_cm,omitempty"`
	WeightKG            *float64 `json:"weight_kg,omitempty"`
	MissingFields       []string `json:"missing_fields"`
}

type PackageSpec struct {
	WarehouseSKU  string  `json:"warehouse_sku"`
	Weight        float64 `json:"weight"`
	WeightUnit    string  `json:"weight_unit"`
	Length        float64 `json:"length"`
	Width         float64 `json:"width"`
	Height        float64 `json:"height"`
	DimensionUnit string  `json:"dimension_unit"`
}

type PackageSpecUpdate struct {
	LengthCM float64 `json:"length_cm"`
	WidthCM  float64 `json:"width_cm"`
	HeightCM float64 `json:"height_cm"`
	WeightKG float64 `json:"weight_kg"`
}

type PackageSpecResolveRequest struct {
	WarehouseSKU string `json:"warehouse_sku"`
	Quantity     int    `json:"quantity"`
}

type WarehouseSKUSpec struct {
	WarehouseSKU string  `json:"warehouse_sku"`
	LengthCM     float64 `json:"length_cm"`
	WidthCM      float64 `json:"width_cm"`
	HeightCM     float64 `json:"height_cm"`
	WeightKG     float64 `json:"weight_kg"`
	Enabled      bool    `json:"enabled"`
	Complete     bool    `json:"complete"`
}

type PackageResolution struct {
	Complete    bool                    `json:"complete"`
	ErrorCode   string                  `json:"error_code"`
	Error       string                  `json:"error"`
	Items       []PackageResolutionItem `json:"items"`
	MissingSKUs []string                `json:"missing_skus"`
	Package     *PackageSpec            `json:"package,omitempty"`
}

type DecisionResponse struct {
	Complete             bool                `json:"complete"`
	RuleVersion          string              `json:"rule_version"`
	SafetyStockThreshold float64             `json:"safety_stock_threshold"`
	DefaultThresholds    InventoryThresholds `json:"default_thresholds"`
	InventoryBasis       string              `json:"inventory_basis"`
	WindowStart          string              `json:"inventory_window_start"`
	WindowEnd            string              `json:"inventory_window_end"`
	QueriedAt            time.Time           `json:"queried_at"`
	Records              []SKUDecision       `json:"records"`
	PackageResolution    PackageResolution   `json:"package_resolution"`
}

type envelope struct {
	Success bool             `json:"success"`
	Data    DecisionResponse `json:"data"`
	Error   string           `json:"error"`
}

func (c *Client) Query(ctx context.Context, quantities map[string]int) (DecisionResponse, error) {
	return c.QueryForShop(ctx, "", "", quantities)
}

func (c *Client) QueryForShop(ctx context.Context, platform, shopCode string, quantities map[string]int) (DecisionResponse, error) {
	skus := make([]string, 0, len(quantities))
	for sku := range quantities {
		skus = append(skus, sku)
	}
	sort.Strings(skus)
	items := make([]map[string]any, 0, len(skus))
	for _, sku := range skus {
		items = append(items, map[string]any{"warehouse_sku": sku, "quantity": quantities[sku]})
	}
	payload := map[string]any{"items": items}
	if platform = strings.TrimSpace(platform); platform != "" {
		payload["platform"] = platform
	}
	if shopCode = strings.TrimSpace(shopCode); shopCode != "" {
		payload["shop_code"] = shopCode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return DecisionResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return DecisionResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if platform == "temu" && shopCode != "" {
		request.Header.Set("X-Temu-Shop", shopCode)
	}
	if platform == "shein" && shopCode != "" {
		request.Header.Set("X-Shein-Shop", shopCode)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return DecisionResponse{}, fmt.Errorf("query warehouse decision: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return DecisionResponse{}, err
	}
	var result envelope
	if err := json.Unmarshal(raw, &result); err != nil {
		return DecisionResponse{}, errors.New("warehouse decision returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.Success {
		return DecisionResponse{}, fmt.Errorf("warehouse decision failed: %s", result.Error)
	}
	if !result.Data.Complete {
		return result.Data, errors.New("warehouse inventory query is incomplete")
	}
	return result.Data, nil
}

func (c *Client) ResolvePackageSpecs(ctx context.Context, items []PackageSpecResolveRequest) (PackageResolution, error) {
	if len(items) == 0 {
		return PackageResolution{}, errors.New("items are required")
	}
	for _, item := range items {
		if strings.TrimSpace(item.WarehouseSKU) == "" || item.Quantity <= 0 {
			return PackageResolution{}, errors.New("warehouse_sku and a positive quantity are required")
		}
	}
	endpoint, err := c.managerEndpoint("/warehouse-sku-specs/resolve")
	if err != nil {
		return PackageResolution{}, err
	}
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return PackageResolution{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return PackageResolution{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return PackageResolution{}, fmt.Errorf("resolve warehouse SKU package specs: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return PackageResolution{}, err
	}
	var result struct {
		Success bool              `json:"success"`
		Data    PackageResolution `json:"data"`
		Error   string            `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return PackageResolution{}, errors.New("warehouse SKU package resolution returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.Success {
		return PackageResolution{}, fmt.Errorf("warehouse SKU package resolution failed: %s", result.Error)
	}
	return result.Data, nil
}

func (c *Client) UpdatePackageSpec(ctx context.Context, warehouseSKU string, update PackageSpecUpdate) (WarehouseSKUSpec, error) {
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return WarehouseSKUSpec{}, errors.New("warehouse SKU is required")
	}
	for name, value := range map[string]float64{"length_cm": update.LengthCM, "width_cm": update.WidthCM, "height_cm": update.HeightCM, "weight_kg": update.WeightKG} {
		if value <= 0 {
			return WarehouseSKUSpec{}, fmt.Errorf("%s must be positive", name)
		}
	}
	endpoint, err := c.managerEndpoint("/warehouse-sku-specs/" + url.PathEscape(warehouseSKU) + "/package")
	if err != nil {
		return WarehouseSKUSpec{}, err
	}
	body, err := json.Marshal(update)
	if err != nil {
		return WarehouseSKUSpec{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		return WarehouseSKUSpec{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return WarehouseSKUSpec{}, fmt.Errorf("update warehouse SKU package spec: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return WarehouseSKUSpec{}, err
	}
	var result struct {
		Success bool             `json:"success"`
		Data    WarehouseSKUSpec `json:"data"`
		Error   string           `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return WarehouseSKUSpec{}, errors.New("warehouse SKU package update returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.Success {
		return WarehouseSKUSpec{}, fmt.Errorf("warehouse SKU package update failed: %s", result.Error)
	}
	return result.Data, nil
}

func (c *Client) managerEndpoint(path string) (string, error) {
	const decisionSuffix = "/temu/warehouse-availability/query"
	if !strings.HasSuffix(c.url, decisionSuffix) {
		return "", errors.New("warehouse decision URL cannot resolve the warehouse manager endpoint")
	}
	return strings.TrimSuffix(c.url, decisionSuffix) + path, nil
}

type ShopInventoryThresholds struct {
	Platform       string  `json:"platform"`
	ShopCode       string  `json:"shop_code"`
	ShopName       string  `json:"shop_name"`
	Enabled        bool    `json:"enabled"`
	EastThreshold  float64 `json:"east_threshold"`
	WestThreshold  float64 `json:"west_threshold"`
	TotalThreshold float64 `json:"total_threshold"`
	Customized     bool    `json:"customized"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type SKUInventoryThreshold struct {
	WarehouseSKU   string     `json:"warehouse_sku"`
	ProductName    string     `json:"product_name"`
	EastAvailable  float64    `json:"east_available"`
	WestAvailable  float64    `json:"west_available"`
	TotalAvailable float64    `json:"total_available"`
	EastThreshold  float64    `json:"east_threshold"`
	WestThreshold  float64    `json:"west_threshold"`
	TotalThreshold float64    `json:"total_threshold"`
	Customized     bool       `json:"customized"`
	Source         string     `json:"source,omitempty"`
	InventoryAt    *time.Time `json:"inventory_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type InventoryThresholdPage struct {
	Records           []SKUInventoryThreshold `json:"records"`
	Total             int                     `json:"total"`
	Page              int                     `json:"page"`
	PageSize          int                     `json:"page_size"`
	Pages             int                     `json:"pages"`
	DefaultThresholds ShopInventoryThresholds `json:"default_thresholds"`
}

func (c *Client) ShopInventoryThresholds(ctx context.Context, platform, shopCode string) (ShopInventoryThresholds, error) {
	platform, shopCode, err := normalizeShopScope(platform, shopCode)
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	endpoint, err := c.managerEndpoint("/inventory-thresholds/defaults")
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	var result ShopInventoryThresholds
	if err := c.doJSON(ctx, http.MethodGet, appendShopQuery(endpoint, platform, shopCode), nil, platform, shopCode, &result); err != nil {
		return ShopInventoryThresholds{}, err
	}
	return result, nil
}

func (c *Client) UpdateShopInventoryThresholds(ctx context.Context, platform, shopCode string, thresholds InventoryThresholds) (ShopInventoryThresholds, error) {
	platform, shopCode, err := normalizeShopScope(platform, shopCode)
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	endpoint, err := c.managerEndpoint("/inventory-thresholds/defaults")
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	body, err := json.Marshal(thresholds)
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	var result ShopInventoryThresholds
	if err := c.doJSON(ctx, http.MethodPatch, appendShopQuery(endpoint, platform, shopCode), body, platform, shopCode, &result); err != nil {
		return ShopInventoryThresholds{}, err
	}
	return result, nil
}

func (c *Client) ResetShopInventoryThresholds(ctx context.Context, platform, shopCode string) (ShopInventoryThresholds, error) {
	platform, shopCode, err := normalizeShopScope(platform, shopCode)
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	endpoint, err := c.managerEndpoint("/inventory-thresholds/defaults/reset")
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	var result ShopInventoryThresholds
	if err := c.doJSON(ctx, http.MethodPost, appendShopQuery(endpoint, platform, shopCode), nil, platform, shopCode, &result); err != nil {
		return ShopInventoryThresholds{}, err
	}
	return result, nil
}

func (c *Client) ListShopSKUInventoryThresholds(ctx context.Context, platform, shopCode, query string, page, pageSize int) (InventoryThresholdPage, error) {
	platform, shopCode, err := normalizeShopScope(platform, shopCode)
	if err != nil {
		return InventoryThresholdPage{}, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	endpoint, err := c.managerEndpoint("/inventory-thresholds")
	if err != nil {
		return InventoryThresholdPage{}, err
	}
	values := url.Values{}
	values.Set("platform", platform)
	values.Set("shop", shopCode)
	values.Set("page", strconv.Itoa(page))
	values.Set("page_size", strconv.Itoa(pageSize))
	if query = strings.TrimSpace(query); query != "" {
		values.Set("q", query)
	}
	var result InventoryThresholdPage
	if err := c.doJSON(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil, platform, shopCode, &result); err != nil {
		return InventoryThresholdPage{}, err
	}
	return result, nil
}

func (c *Client) UpdateShopSKUInventoryThreshold(ctx context.Context, platform, shopCode, warehouseSKU string, thresholds InventoryThresholds) (SKUInventoryThreshold, error) {
	platform, shopCode, err := normalizeShopScope(platform, shopCode)
	if err != nil {
		return SKUInventoryThreshold{}, err
	}
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return SKUInventoryThreshold{}, errors.New("warehouse SKU is required")
	}
	endpoint, err := c.managerEndpoint("/inventory-thresholds/" + url.PathEscape(warehouseSKU))
	if err != nil {
		return SKUInventoryThreshold{}, err
	}
	body, err := json.Marshal(thresholds)
	if err != nil {
		return SKUInventoryThreshold{}, err
	}
	var result SKUInventoryThreshold
	if err := c.doJSON(ctx, http.MethodPatch, appendShopQuery(endpoint, platform, shopCode), body, platform, shopCode, &result); err != nil {
		return SKUInventoryThreshold{}, err
	}
	return result, nil
}

func (c *Client) ResetShopSKUInventoryThreshold(ctx context.Context, platform, shopCode, warehouseSKU string) error {
	platform, shopCode, err := normalizeShopScope(platform, shopCode)
	if err != nil {
		return err
	}
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return errors.New("warehouse SKU is required")
	}
	endpoint, err := c.managerEndpoint("/inventory-thresholds/" + url.PathEscape(warehouseSKU) + "/reset")
	if err != nil {
		return err
	}
	var result map[string]bool
	return c.doJSON(ctx, http.MethodPost, appendShopQuery(endpoint, platform, shopCode), nil, platform, shopCode, &result)
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body []byte, platform, shopCode string, destination any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if platform == "temu" && shopCode != "" {
		request.Header.Set("X-Temu-Shop", shopCode)
	}
	if platform == "shein" && shopCode != "" {
		request.Header.Set("X-Shein-Shop", shopCode)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call warehouse manager: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	var result struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return errors.New("warehouse manager returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.Success {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = fmt.Sprintf("warehouse manager returned HTTP %d", response.StatusCode)
		}
		return errors.New(message)
	}
	if destination == nil || len(result.Data) == 0 || string(result.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(result.Data, destination); err != nil {
		return errors.New("warehouse manager returned invalid data")
	}
	return nil
}

func normalizeShopScope(platform, shopCode string) (string, string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	shopCode = strings.ToLower(strings.TrimSpace(shopCode))
	if platform == "" || shopCode == "" {
		return "", "", errors.New("platform and shop code are required")
	}
	if platform != "temu" && platform != "shein" {
		return "", "", errors.New("platform must be temu or shein")
	}
	return platform, shopCode, nil
}

func appendShopQuery(endpoint, platform, shopCode string) string {
	values := url.Values{}
	values.Set("platform", platform)
	values.Set("shop", shopCode)
	if strings.Contains(endpoint, "?") {
		return endpoint + "&" + values.Encode()
	}
	return endpoint + "?" + values.Encode()
}

type Selection struct {
	Region        string           `json:"region"`
	WarehouseKey  string           `json:"warehouse_key"`
	WarehouseName string           `json:"warehouse_name"`
	Reason        string           `json:"reason"`
	Decision      DecisionResponse `json:"decision"`
}

func SelectWarehouse(decision DecisionResponse, region string, quantities map[string]int, preferredWarehouseKeys ...string) (Selection, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	if region != "east" && region != "west" {
		return Selection{}, errors.New("region must be east or west")
	}
	for _, record := range decision.Records {
		if !record.RequiresManual {
			continue
		}
		reasons := make([]string, 0, len(record.Regions))
		for _, current := range record.Regions {
			if current.RequiresManual {
				reasons = append(reasons, current.Reason)
			}
		}
		return Selection{}, fmt.Errorf("SKU %s requires manual review: %s", record.SKU, strings.Join(reasons, "；"))
	}
	preference := []string{"DPS002", "ARP_EAST"}
	if region == "west" {
		preference = []string{"DPS004", "ARP_WEST"}
	}
	manualSelection := false
	if len(preferredWarehouseKeys) > 0 && strings.TrimSpace(preferredWarehouseKeys[0]) != "" {
		requested := strings.ToUpper(strings.TrimSpace(preferredWarehouseKeys[0]))
		if requested != preference[0] && requested != preference[1] {
			return Selection{}, fmt.Errorf("warehouse %s does not belong to region %s", requested, region)
		}
		preference = []string{requested}
		manualSelection = true
	}
	eligible := make(map[string]bool, len(preference))
	for _, key := range preference {
		eligible[key] = true
	}
	names := make(map[string]string)
	for _, record := range decision.Records {
		var current *Region
		for i := range record.Regions {
			if record.Regions[i].Region == region {
				current = &record.Regions[i]
				break
			}
		}
		if current == nil {
			return Selection{}, fmt.Errorf("SKU %s has no %s inventory decision", record.SKU, region)
		}
		if current.RequiresManual {
			return Selection{}, fmt.Errorf("SKU %s requires manual review: %s", record.SKU, current.Reason)
		}
		available := make(map[string]Warehouse, len(current.Warehouses))
		for _, warehouse := range current.Warehouses {
			available[warehouse.Key] = warehouse
			names[warehouse.Key] = warehouse.Name
		}
		for _, key := range preference {
			warehouse, ok := available[key]
			if !ok || !warehouse.Selectable || warehouse.Available < float64(quantities[record.SKU]) {
				eligible[key] = false
			}
		}
	}
	for _, key := range preference {
		if eligible[key] {
			reason := "该仓库可独立覆盖订单内全部SKU"
			switch {
			case manualSelection:
				reason += "，使用人工选择的仓库"
			case strings.HasPrefix(key, "DPS"):
				reason += "，按规则默认优先选择DPS仓清理库存"
			default:
				reason += "，DPS仓不能覆盖全部SKU，默认回退ARP仓"
			}
			return Selection{Region: region, WarehouseKey: key, WarehouseName: names[key], Reason: reason, Decision: decision}, nil
		}
	}
	if manualSelection {
		return Selection{}, fmt.Errorf("选择的仓库 %s 不能覆盖订单内全部SKU", preference[0])
	}
	return Selection{}, errors.New("该区域没有一个仓库能够覆盖订单内全部SKU，转人工处理")
}
