package temu

import (
	"context"
	"encoding/json"
)

const (
	TokenInfoAPI        = "bg.open.accesstoken.info.get"
	OrderListAPI        = "bg.order.list.v2.get"
	OrderDetailAPI      = "bg.order.detail.v2.get"
	CombinedShipmentAPI = "bg.order.combinedshipment.list.get"
	WarehouseListAPI    = "bg.logistics.warehouse.list.get"
	ShippingServicesAPI = "bg.logistics.shippingservices.get"
	ShipmentCreateAPI   = "bg.logistics.shipment.create"
	ShipmentResultAPI   = "bg.logistics.shipment.result.get"
	ShipmentDocumentAPI = "bg.logistics.shipment.document.get"
	ShipmentConfirmAPI  = "bg.logistics.shipped.package.confirm"
	TrackingInfoAPI     = "temu.track.trackinginfo.get"
)

type TokenInfo struct {
	ExpiredTime  int64    `json:"expiredTime"`
	MallID       int64    `json:"mallId"`
	MallType     int      `json:"mallType"`
	SemiUniqueID string   `json:"semiUniqueId"`
	RegionID     int64    `json:"regionId"`
	APIScopes    []string `json:"apiScopeList"`
}

type NameValue struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type ProductRef struct {
	ProductID    int64  `json:"productId"`
	ProductSKUID int64  `json:"productSkuId"`
	SoldFactor   int64  `json:"soldFactor"`
	ExtCode      string `json:"extCode"`
}

type PackageInfo struct {
	PackageSN           string `json:"packageSn"`
	PackageDeliveryType int    `json:"packageDeliveryType"`
	CallSuccess         bool   `json:"callSuccess"`
}
type OrderLine struct {
	OrderSN         string          `json:"orderSn"`
	Quantity        int             `json:"quantity"`
	GoodsID         int64           `json:"goodsId"`
	SKUID           int64           `json:"skuId"`
	GoodsName       string          `json:"goodsName"`
	OriginalName    string          `json:"originalGoodsName"`
	Spec            string          `json:"spec"`
	OriginalSpec    string          `json:"originalSpecName"`
	OrderStatus     int             `json:"orderStatus"`
	FulfillmentType string          `json:"fulfillmentType"`
	Products        []ProductRef    `json:"productList"`
	PackageSNInfo   []PackageInfo   `json:"packageSnInfo"`
	Raw             json.RawMessage `json:"-"`
}

type ParentOrder struct {
	ParentOrderSN        string      `json:"parentOrderSn"`
	ParentOrderStatus    int         `json:"parentOrderStatus"`
	ExpectShipLatestTime int64       `json:"expectShipLatestTime"`
	RegionID             int64       `json:"regionId"`
	UpdateTime           int64       `json:"updateTime"`
	ParentShippingTime   int64       `json:"parentShippingTime"`
	Labels               []NameValue `json:"parentOrderLabel"`
	FulfillmentWarnings  []string    `json:"fulfillmentWarning"`
	BatchOrderNumberList []string    `json:"batchOrderNumberList"`
	Consolidated         bool        `json:"isShipmentConsolidatedByMainMall"`
	RegionName1          string      `json:"regionName1"`
	RegionName2          string      `json:"regionName2"`
	RegionName3          string      `json:"regionName3"`
}

type OrderPageItem struct {
	Parent ParentOrder `json:"parentOrderMap"`
	Lines  []OrderLine `json:"orderList"`
}

type OrderListResult struct {
	PageItems    []OrderPageItem `json:"pageItems"`
	TotalItemNum int             `json:"totalItemNum"`
}

type CombinedShipmentOrder struct {
	ParentOrderSN     string `json:"parentOrderSn"`
	ParentOrderStatus int    `json:"parentOrderStatus"`
	ParentOrderTime   int64  `json:"parentOrderTime"`
	MallID            int64  `json:"mallId"`
	SemiUniqueID      string `json:"semiUniqueId"`
}

type CombinedShipmentGroup struct {
	Orders []CombinedShipmentOrder `json:"combinedShippingGroup"`
}

type CombinedShipmentResult struct {
	Groups []CombinedShipmentGroup `json:"combinedShippingGroups"`
}

type Warehouse struct {
	ID                     string          `json:"warehouseId"`
	Name                   string          `json:"warehouseName"`
	RegionID               int64           `json:"regionId1"`
	Default                bool            `json:"defaultWarehouse"`
	ManagementType         int             `json:"warehouseManagementType"`
	EnableBuyShippingLabel bool            `json:"enableBuyShippingLabel"`
	Raw                    json.RawMessage `json:"-"`
}

type WarehouseListResult struct {
	Warehouses []Warehouse `json:"warehouseList"`
}

type PickupTimeSlot struct {
	Start int64 `json:"pickupStartTime"`
	End   int64 `json:"pickupEndTime"`
}

type ShippingChannel struct {
	ChannelID             int64            `json:"channelId"`
	ShipCompanyID         int64            `json:"shipCompanyId"`
	ShippingCompanyName   string           `json:"shippingCompanyName"`
	ShipLogisticsType     string           `json:"shipLogisticsType"`
	EstimatedText         string           `json:"estimatedText"`
	EstimatedCurrencyCode string           `json:"estimatedCurrencyCode"`
	EstimatedAmount       string           `json:"estimatedAmount"`
	SignServiceID         int64            `json:"signServiceId"`
	SignServiceName       string           `json:"signServiceName"`
	InfoNeeded            []string         `json:"infoNeeded"`
	PayWayCode            int              `json:"payWayCode"`
	PickupRules           string           `json:"pickupRules"`
	ChannelRules          string           `json:"channelRules"`
	PickupSlots           []PickupTimeSlot `json:"availablePickupTimeSlotList"`
	UnavailableReason     string           `json:"unavailableReason,omitempty"`
	Selected              bool             `json:"selected,omitempty"`
	SelectionReason       string           `json:"selection_reason,omitempty"`
}

type ShippingServicesResult struct {
	Available   []ShippingChannel `json:"onlineChannelDtoList"`
	Unavailable []ShippingChannel `json:"unavailableChannelDtoList"`
}

type ShipmentCreateResult struct {
	PackageSNList      []string `json:"packageSnList"`
	ShipLaterLimitTime string   `json:"shipLaterLimitTime"`
	WarningMessage     []string `json:"warningMessage"`
}

type PackageResult struct {
	PackageSN           string   `json:"packageSn"`
	ShippingLabelStatus int      `json:"shippingLabelStatus"`
	FailReasonText      string   `json:"failReasonText"`
	SolutionText        string   `json:"solutionText"`
	TrackingNumber      string   `json:"trackingNumber"`
	ShippingCompanyName string   `json:"shippingCompanyName"`
	ShipLogisticsType   string   `json:"shipLogisticsType"`
	ChannelID           int64    `json:"channelId"`
	ShipCompanyID       int64    `json:"shipCompanyId"`
	CanChangeToManual   bool     `json:"canChangeToManualSend"`
	WarningMessage      []string `json:"warningMessage"`
}

type ShipmentResult struct {
	Packages []PackageResult `json:"packageInfoResultList"`
}

type ShippingLabelURL struct {
	PackageSN    string `json:"packageSn"`
	URL          string `json:"url"`
	DocumentType string `json:"documentType"`
}

type ShipmentDocumentResult struct {
	Labels   []ShippingLabelURL `json:"shippingLabelUrlList"`
	Warnings []string           `json:"warningMessage"`
}

type TrackingEvent struct {
	LogisticsUpdatedAt string `json:"logisticsUpdatedAt"`
	LogisticsStatus    string `json:"logisticsStatus"`
	StatusText         string `json:"statusText"`
}

type TrackingInfoResult struct {
	PackageSN    string          `json:"packageSn"`
	TrackingNum  string          `json:"trackingNum"`
	TrackingInfo []TrackingEvent `json:"trackingInfo"`
}

func (c *Client) TokenInfo(ctx context.Context) (TokenInfo, json.RawMessage, error) {
	var result TokenInfo
	raw, err := c.Call(ctx, TokenInfoAPI, nil, &result)
	return result, raw, err
}

func (c *Client) OrderPage(ctx context.Context, page, pageSize int) (OrderListResult, json.RawMessage, error) {
	return c.OrderPageByStatus(ctx, 2, page, pageSize)
}

func (c *Client) OrderPageByStatus(ctx context.Context, status, page, pageSize int) (OrderListResult, json.RawMessage, error) {
	var result OrderListResult
	raw, err := c.Call(ctx, OrderListAPI, map[string]any{
		"parentOrderStatus":   status,
		"fulfillmentTypeList": []string{"fulfillBySeller"},
		"sortby":              "updateTime",
		"pageNumber":          page,
		"pageSize":            pageSize,
	}, &result)
	return result, raw, err
}

func (c *Client) OrderDetail(ctx context.Context, parentOrderSN string) (OrderPageItem, json.RawMessage, error) {
	var result OrderPageItem
	raw, err := c.Call(ctx, OrderDetailAPI, map[string]any{
		"parentOrderSn":       parentOrderSN,
		"fulfillmentTypeList": []string{"fulfillBySeller"},
	}, &result)
	return result, raw, err
}

func (c *Client) CombinedShipments(ctx context.Context) (CombinedShipmentResult, json.RawMessage, error) {
	var result CombinedShipmentResult
	raw, err := c.Call(ctx, CombinedShipmentAPI, nil, &result)
	return result, raw, err
}

func (c *Client) Warehouses(ctx context.Context) (WarehouseListResult, json.RawMessage, error) {
	var result WarehouseListResult
	raw, err := c.Call(ctx, WarehouseListAPI, map[string]any{"returnEnableBuyShippingLabelOnly": false}, &result)
	return result, raw, err
}

func (c *Client) ShippingServices(ctx context.Context, request map[string]any) (ShippingServicesResult, json.RawMessage, error) {
	var result ShippingServicesResult
	raw, err := c.Call(ctx, ShippingServicesAPI, request, &result)
	return result, raw, err
}

func (c *Client) CreateShipment(ctx context.Context, request map[string]any) (ShipmentCreateResult, json.RawMessage, error) {
	release, err := c.acquireShipmentCreate(ctx)
	if err != nil {
		return ShipmentCreateResult{}, nil, err
	}
	defer release()
	var result ShipmentCreateResult
	raw, err := c.Call(ctx, ShipmentCreateAPI, request, &result)
	return result, raw, err
}

func (c *Client) ShipmentResult(ctx context.Context, packageSNs []string) (ShipmentResult, json.RawMessage, error) {
	var result ShipmentResult
	raw, err := c.Call(ctx, ShipmentResultAPI, map[string]any{"packageSnList": packageSNs}, &result)
	return result, raw, err
}

func (c *Client) ShipmentDocument(ctx context.Context, packageSNs []string) (ShipmentDocumentResult, json.RawMessage, error) {
	var result ShipmentDocumentResult
	raw, err := c.Call(ctx, ShipmentDocumentAPI, map[string]any{
		"documentType": "SHIPPING_LABEL_PDF", "packageSnList": packageSNs,
	}, &result)
	return result, raw, err
}

func (c *Client) ConfirmShipped(ctx context.Context, packageSendInfoList []map[string]any) (json.RawMessage, error) {
	return c.Call(ctx, ShipmentConfirmAPI, map[string]any{"packageSendInfoList": packageSendInfoList}, nil)
}

func (c *Client) TrackingInfo(ctx context.Context, packageSN, language string) (TrackingInfoResult, json.RawMessage, error) {
	parameters := map[string]any{"packageSn": packageSN}
	if language != "" {
		parameters["language"] = language
	}
	var result TrackingInfoResult
	raw, err := c.Call(ctx, TrackingInfoAPI, parameters, &result)
	return result, raw, err
}
