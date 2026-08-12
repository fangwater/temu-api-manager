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
		baseURL: baseURL, auditURL: root + "/fulfillment-audits/sync",
		platformOrderURL: root + "/temu/platform-orders", http: &http.Client{Timeout: timeout},
	}
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
		return PlatformOrderLookup{}, fmt.Errorf("query XLWMS platform order: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return PlatformOrderLookup{}, err
	}
	var gateway gatewayResponse
	if err := json.Unmarshal(body, &gateway); err != nil {
		return PlatformOrderLookup{}, fmt.Errorf("decode XLWMS platform order response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !gateway.Success {
		message := strings.TrimSpace(gateway.Error)
		if message == "" {
			message = fmt.Sprintf("XLWMS platform order query returned HTTP %d", response.StatusCode)
		}
		return PlatformOrderLookup{}, errors.New(message)
	}
	var result PlatformOrderLookup
	if err := json.Unmarshal(gateway.Data, &result); err != nil {
		return PlatformOrderLookup{}, fmt.Errorf("decode XLWMS platform order data: %w", err)
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
