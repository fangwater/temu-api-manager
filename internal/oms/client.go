package oms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL          string
	rootURL          string
	auditURL         string
	platformOrderURL string
	http             *http.Client
}

type FulfillmentAuditSnapshotOrder struct {
	PlatformOrderNo    string     `json:"platform_order_no"`
	PlatformStatus     string     `json:"platform_status"`
	PlatformStatusCode *int       `json:"platform_status_code,omitempty"`
	PlatformShippingAt *time.Time `json:"platform_shipping_at,omitempty"`
	WarehouseKey       string     `json:"warehouse_key"`
	WarehouseCode      string     `json:"wh_code"`
	TrackingNumber     string     `json:"tracking_number"`
}

type FulfillmentAuditSnapshot struct {
	Platform string                          `json:"platform"`
	ShopCode string                          `json:"shop_code"`
	ShopName string                          `json:"shop_name"`
	Orders   []FulfillmentAuditSnapshotOrder `json:"orders"`
}

type OutboundOrder struct {
	WarehouseCode    string `json:"whCode"`
	OutboundOrderNo  string `json:"outboundOrderNo"`
	ThirdOrderNo     string `json:"thirdOrderNo"`
	ReferOrderNo     string `json:"referOrderNo"`
	PlatformOrderNo  string `json:"platformOrderNo"`
	Status           int    `json:"status"`
	TrackingNumber   string `json:"logisticsTrackNo"`
	ExceptionDesc    string `json:"exceptionDesc"`
	LogisticsChannel string `json:"logisticsChannel"`
}

type PlatformOrder struct {
	OMSOrderNo         string `json:"oms_order_no"`
	PlatformOrderNo    string `json:"platform_order_no"`
	Status             int    `json:"status"`
	StatusKey          string `json:"status_key"`
	StatusText         string `json:"status_text"`
	SubStatus          int    `json:"sub_status"`
	SendWarehouseCode  string `json:"send_warehouse_code"`
	TrackingNumber     string `json:"tracking_number"`
	OrderTime          string `json:"order_time"`
	CreateTime         string `json:"create_time"`
	AuditTime          string `json:"audit_time"`
	MarkShipmentStatus int    `json:"mark_shipment_status"`
	MarkShipmentTime   string `json:"mark_shipment_time"`
}

type PlatformOrderLookup struct {
	Account         string          `json:"account"`
	PlatformOrderNo string          `json:"platform_order_no"`
	Found           bool            `json:"found"`
	MatchCount      int             `json:"match_count"`
	Orders          []PlatformOrder `json:"orders"`
	QueriedAt       time.Time       `json:"queried_at"`
}

type WarehouseAssignmentRoute struct {
	PlatformOrderNo     string `json:"platform_order_no"`
	PlatformWarehouseID string `json:"platform_warehouse_id"`
	PlatformWarehouse   string `json:"platform_warehouse_name"`
	WarehouseCode       string `json:"warehouse_code"`
	WarehouseName       string `json:"warehouse_name"`
}

type WarehouseAssignmentFailure struct {
	PlatformOrderNo string `json:"platform_order_no"`
	Error           string `json:"error"`
}

type WarehouseAssignmentCarrier struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type WarehouseAssignmentUnresolved struct {
	PlatformOrderNo string `json:"platform_order_no"`
	Reason          string `json:"reason"`
}

type WarehouseAssignmentPreview struct {
	Ready       bool                            `json:"ready"`
	Routes      []WarehouseAssignmentRoute      `json:"routes"`
	Unresolved  []WarehouseAssignmentUnresolved `json:"unresolved"`
	ChannelCode string                          `json:"channel_code"`
	ChannelName string                          `json:"channel_name"`
	Carriers    []WarehouseAssignmentCarrier    `json:"carriers"`
	QueriedAt   time.Time                       `json:"queried_at"`
}

type WarehouseAssignmentResult struct {
	Account          string                       `json:"account"`
	Total            int                          `json:"total"`
	Success          int                          `json:"success"`
	Failed           int                          `json:"failed"`
	Failures         []WarehouseAssignmentFailure `json:"failures"`
	Routes           []WarehouseAssignmentRoute   `json:"routes"`
	WarehouseCode    string                       `json:"warehouse_code"`
	WarehouseCodes   []string                     `json:"warehouse_codes"`
	ChannelCode      string                       `json:"channel_code"`
	LogisticsCarrier string                       `json:"logistics_carrier"`
	CompletedAt      time.Time                    `json:"completed_at"`
}

type GatewayError struct {
	StatusCode int
	Message    string
}

func (e *GatewayError) Error() string { return e.Message }

const (
	AutoMatchCarrier = "_AUTO_MATCH_"
	OtherCarrier     = "other"
)

type gatewayResponse struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	root := strings.TrimSuffix(baseURL, "/outbound")
	return &Client{
		baseURL: baseURL, rootURL: root, auditURL: root + "/fulfillment-audits/sync",
		platformOrderURL: root + "/temu/platform-orders", http: &http.Client{Timeout: timeout},
	}
}

func (c *Client) PreviewWarehouseAssignment(ctx context.Context, account, platformOrderNo string) (WarehouseAssignmentPreview, error) {
	account, platformOrderNo, err := validateWarehouseAssignmentTarget(account, platformOrderNo)
	if err != nil {
		return WarehouseAssignmentPreview{}, err
	}
	var result WarehouseAssignmentPreview
	err = c.postGateway(ctx, "/platform-orders/routing-preview", account, map[string]any{
		"platform_order_nos": []string{platformOrderNo},
	}, &result)
	if result.Routes == nil {
		result.Routes = []WarehouseAssignmentRoute{}
	}
	if result.Unresolved == nil {
		result.Unresolved = []WarehouseAssignmentUnresolved{}
	}
	if result.Carriers == nil {
		result.Carriers = []WarehouseAssignmentCarrier{}
	}
	return result, err
}

func (c *Client) AssignWarehouse(ctx context.Context, account, platformOrderNo, logisticsCarrier string) (WarehouseAssignmentResult, error) {
	account, platformOrderNo, err := validateWarehouseAssignmentTarget(account, platformOrderNo)
	if err != nil {
		return WarehouseAssignmentResult{}, err
	}
	logisticsCarrier = strings.TrimSpace(logisticsCarrier)
	if logisticsCarrier != AutoMatchCarrier && logisticsCarrier != OtherCarrier {
		return WarehouseAssignmentResult{}, errors.New("logistics carrier must be automatic matching or Other")
	}
	var result WarehouseAssignmentResult
	err = c.postGateway(ctx, "/platform-orders/warehouse-assignments", account, map[string]any{
		"platform_order_nos": []string{platformOrderNo},
		"logistics_carrier":  logisticsCarrier,
		"confirmation":       "CONFIRM_AND_APPROVE",
	}, &result)
	if result.Failures == nil {
		result.Failures = []WarehouseAssignmentFailure{}
	}
	if result.Routes == nil {
		result.Routes = []WarehouseAssignmentRoute{}
	}
	if result.WarehouseCodes == nil {
		result.WarehouseCodes = []string{}
	}
	return result, err
}

func validateWarehouseAssignmentTarget(account, platformOrderNo string) (string, string, error) {
	account = strings.TrimSpace(account)
	platformOrderNo = strings.TrimSpace(platformOrderNo)
	if account == "" {
		return "", "", errors.New("OMS account is required")
	}
	if platformOrderNo == "" {
		return "", "", errors.New("platform order number is required")
	}
	return account, platformOrderNo, nil
}

func (c *Client) postGateway(ctx context.Context, path, account string, payload, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rootURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-OMS-Account", account)
	response, err := c.http.Do(request)
	if err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "无法连接仓库分配服务"}
	}
	defer response.Body.Close()
	body, err = io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "无法读取仓库分配服务响应"}
	}
	var gateway gatewayResponse
	if err := json.Unmarshal(body, &gateway); err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "仓库分配服务返回了无效响应"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !gateway.Success {
		message := strings.TrimSpace(gateway.Error)
		if message == "" {
			message = fmt.Sprintf("XLWMS warehouse assignment returned HTTP %d", response.StatusCode)
		}
		return &GatewayError{StatusCode: response.StatusCode, Message: message}
	}
	if target == nil || len(gateway.Data) == 0 || string(gateway.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(gateway.Data, target); err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "仓库分配服务返回了无效数据"}
	}
	return nil
}

func (c *Client) QueryPlatformOrder(ctx context.Context, account, platformOrderNo string) (PlatformOrderLookup, error) {
	account = strings.TrimSpace(account)
	platformOrderNo = strings.TrimSpace(platformOrderNo)
	if account == "" {
		return PlatformOrderLookup{}, errors.New("OMS account is required")
	}
	if platformOrderNo == "" {
		return PlatformOrderLookup{}, errors.New("platform order number is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.platformOrderURL+"/"+url.PathEscape(platformOrderNo), nil)
	if err != nil {
		return PlatformOrderLookup{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-OMS-Account", account)
	response, err := c.http.Do(request)
	if err != nil {
		return PlatformOrderLookup{}, &GatewayError{StatusCode: http.StatusBadGateway, Message: "无法连接领星平台订单查询服务"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return PlatformOrderLookup{}, &GatewayError{StatusCode: http.StatusBadGateway, Message: "无法读取领星平台订单查询响应"}
	}
	var gateway gatewayResponse
	if err := json.Unmarshal(body, &gateway); err != nil {
		return PlatformOrderLookup{}, &GatewayError{StatusCode: http.StatusBadGateway, Message: "领星平台订单查询服务返回了无效响应"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !gateway.Success {
		message := strings.TrimSpace(gateway.Error)
		if message == "" {
			message = fmt.Sprintf("XLWMS platform order query returned HTTP %d", response.StatusCode)
		}
		return PlatformOrderLookup{}, &GatewayError{StatusCode: response.StatusCode, Message: message}
	}
	var result PlatformOrderLookup
	if err := json.Unmarshal(gateway.Data, &result); err != nil {
		return PlatformOrderLookup{}, &GatewayError{StatusCode: http.StatusBadGateway, Message: "领星平台订单查询服务返回了无效数据"}
	}
	if result.Orders == nil {
		result.Orders = []PlatformOrder{}
	}
	return result, nil
}

func (c *Client) SyncFulfillmentAudits(ctx context.Context, snapshot FulfillmentAuditSnapshot) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.auditURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("sync XLWMS fulfillment audits: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	var gateway gatewayResponse
	if err := json.Unmarshal(body, &gateway); err != nil {
		return fmt.Errorf("decode XLWMS fulfillment audit response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !gateway.Success {
		message := strings.TrimSpace(gateway.Error)
		if message == "" {
			message = fmt.Sprintf("XLWMS fulfillment audit returned HTTP %d", response.StatusCode)
		}
		return errors.New(message)
	}
	return nil
}

func (c *Client) QueryByReferences(ctx context.Context, warehouseCode string, references []string) ([]OutboundOrder, error) {
	references = cleanStrings(references)
	if len(references) == 0 {
		return nil, errors.New("OMS order reference is required")
	}
	orders, err := c.query(ctx, warehouseCode, map[string]any{"thirdOrderNoList": references})
	if err != nil || len(orders) > 0 {
		return orders, err
	}
	return c.query(ctx, warehouseCode, map[string]any{"referOrderNoList": references})
}

func (c *Client) query(ctx context.Context, warehouseCode string, data map[string]any) ([]OutboundOrder, error) {
	var orders []OutboundOrder
	if err := c.call(ctx, "parcel-detail", warehouseCode, data, &orders); err != nil {
		return nil, err
	}
	return orders, nil
}

func (c *Client) call(ctx context.Context, operation, warehouseCode string, data, target any) error {
	warehouseCode = strings.TrimSpace(warehouseCode)
	if warehouseCode == "" {
		return errors.New("OMS warehouse code is required")
	}
	payload, err := json.Marshal(map[string]any{"warehouse": warehouseCode, "data": data})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+operation, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("call XLWMS %s: %w", operation, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("XLWMS proxy returned HTTP %d", response.StatusCode)
	}
	var gateway gatewayResponse
	if err := json.Unmarshal(body, &gateway); err != nil {
		return fmt.Errorf("decode XLWMS proxy response: %w", err)
	}
	if !gateway.Success {
		message := strings.TrimSpace(gateway.Error)
		if message == "" {
			message = "XLWMS proxy request failed"
		}
		return errors.New(message)
	}
	var api apiResponse
	if err := json.Unmarshal(gateway.Data, &api); err != nil {
		return fmt.Errorf("decode XLWMS API response: %w", err)
	}
	if api.Code != http.StatusOK {
		return fmt.Errorf("XLWMS API error code=%d msg=%s", api.Code, api.Msg)
	}
	if target == nil || len(api.Data) == 0 || string(api.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(api.Data, target); err != nil {
		return fmt.Errorf("decode XLWMS response data: %w", err)
	}
	return nil
}

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
