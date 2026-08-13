package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/internal/oms"

	"github.com/jackc/pgx/v5"
)

var ErrWarehouseAssignmentUnavailable = errors.New("当前订单不可分配仓库")

type WarehouseAssignmentPreview struct {
	ParentOrderSN     string                              `json:"parent_order_sn"`
	OMSOrderNo        string                              `json:"oms_order_no"`
	OMSAccount        string                              `json:"oms_account"`
	TrackingNumber    string                              `json:"tracking_number"`
	WarehouseKey      string                              `json:"warehouse_key"`
	WarehouseCode     string                              `json:"warehouse_code"`
	TemuWarehouseID   string                              `json:"temu_warehouse_id"`
	TemuWarehouseName string                              `json:"temu_warehouse_name"`
	Ready             bool                                `json:"ready"`
	Routes            []oms.WarehouseAssignmentRoute      `json:"routes"`
	Unresolved        []oms.WarehouseAssignmentUnresolved `json:"unresolved"`
	ChannelCode       string                              `json:"channel_code"`
	ChannelName       string                              `json:"channel_name"`
	Carriers          []oms.WarehouseAssignmentCarrier    `json:"carriers"`
	QueriedAt         time.Time                           `json:"queried_at"`
}

type WarehouseAssignmentOutcome struct {
	ParentOrderSN string                        `json:"parent_order_sn"`
	OMSAccount    string                        `json:"oms_account"`
	Result        oms.WarehouseAssignmentResult `json:"result"`
}

type warehouseAssignmentTarget struct {
	shipment model.Shipment
	mapping  model.WarehouseMapping
	account  string
	order    oms.PlatformOrder
}

func (s *Service) PreviewWarehouseAssignment(ctx context.Context, parentOrderSN string) (WarehouseAssignmentPreview, error) {
	target, err := s.warehouseAssignmentTarget(ctx, parentOrderSN)
	if err != nil {
		return WarehouseAssignmentPreview{}, err
	}
	preview, err := s.oms.PreviewWarehouseAssignment(ctx, target.account, target.shipment.ParentOrderSN)
	if err != nil {
		return WarehouseAssignmentPreview{}, err
	}
	if err := validateWarehouseAssignmentPreview(target, preview); err != nil {
		return WarehouseAssignmentPreview{}, err
	}
	return warehouseAssignmentPreview(target, preview), nil
}

func (s *Service) AssignWarehouse(ctx context.Context, parentOrderSN, logisticsCarrier string) (WarehouseAssignmentOutcome, error) {
	target, err := s.warehouseAssignmentTarget(ctx, parentOrderSN)
	if err != nil {
		return WarehouseAssignmentOutcome{}, err
	}
	preview, err := s.oms.PreviewWarehouseAssignment(ctx, target.account, target.shipment.ParentOrderSN)
	if err != nil {
		return WarehouseAssignmentOutcome{}, err
	}
	if err := validateWarehouseAssignmentPreview(target, preview); err != nil {
		return WarehouseAssignmentOutcome{}, err
	}
	result, err := s.oms.AssignWarehouse(ctx, target.account, target.shipment.ParentOrderSN, logisticsCarrier)
	outcome := WarehouseAssignmentOutcome{ParentOrderSN: target.shipment.ParentOrderSN, OMSAccount: target.account, Result: result}
	if err != nil {
		return outcome, err
	}
	if result.Total != 1 || result.Success != 1 || result.Failed != 0 {
		message := "领星未确认仓库分配结果"
		if len(result.Failures) > 0 && strings.TrimSpace(result.Failures[0].Error) != "" {
			message = strings.TrimSpace(result.Failures[0].Error)
		}
		return outcome, fmt.Errorf("%w：%s", ErrWarehouseAssignmentUnavailable, message)
	}
	if _, refreshErr := s.CheckOMSSync(ctx, target.shipment.ID); refreshErr != nil && s.logger != nil {
		s.logger.Warn("refresh OMS platform order after warehouse assignment", "parent_order_sn", target.shipment.ParentOrderSN, "error", refreshErr)
	}
	return outcome, nil
}

func (s *Service) warehouseAssignmentTarget(ctx context.Context, parentOrderSN string) (warehouseAssignmentTarget, error) {
	parentOrderSN = strings.TrimSpace(parentOrderSN)
	if parentOrderSN == "" {
		return warehouseAssignmentTarget{}, errors.New("parentOrderSn is required")
	}
	if s.store == nil || s.oms == nil {
		return warehouseAssignmentTarget{}, errors.New("warehouse assignment service is not configured")
	}
	shipment, err := s.store.ShipmentForOrder(ctx, parentOrderSN)
	if errors.Is(err, pgx.ErrNoRows) {
		return warehouseAssignmentTarget{}, fmt.Errorf("%w：当前店铺没有该订单的购面单记录", ErrWarehouseAssignmentUnavailable)
	}
	if err != nil {
		return warehouseAssignmentTarget{}, err
	}
	mapping, err := s.store.WarehouseMapping(ctx, shipment.OMSWarehouseKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return warehouseAssignmentTarget{}, fmt.Errorf("%w：买单仓库映射不存在", ErrWarehouseAssignmentUnavailable)
	}
	if err != nil {
		return warehouseAssignmentTarget{}, err
	}
	account, ok := normalizeOMSAccount(mapping.OMSAccount)
	if !ok {
		return warehouseAssignmentTarget{}, fmt.Errorf("%w：买单仓库没有可用的领星账户", ErrWarehouseAssignmentUnavailable)
	}
	lookup, err := s.oms.QueryPlatformOrder(ctx, account, parentOrderSN)
	if err != nil {
		return warehouseAssignmentTarget{}, err
	}
	if err := validateWarehouseAssignmentSource(shipment, mapping, lookup); err != nil {
		return warehouseAssignmentTarget{}, err
	}
	return warehouseAssignmentTarget{shipment: shipment, mapping: mapping, account: account, order: lookup.Orders[0]}, nil
}

func validateWarehouseAssignmentSource(shipment model.Shipment, mapping model.WarehouseMapping, lookup oms.PlatformOrderLookup) error {
	if shipment.Status != "shipped" || shipment.ConfirmedAt == nil || shipment.ConfirmedAt.IsZero() ||
		strings.TrimSpace(shipment.TrackingNumber) == "" || len(shipment.PackageSNList) == 0 {
		return fmt.Errorf("%w：Temu 面单尚未购买并确认", ErrWarehouseAssignmentUnavailable)
	}
	if strings.TrimSpace(mapping.OMSWarehouseCode) == "" || strings.TrimSpace(mapping.TemuWarehouseID) == "" {
		return fmt.Errorf("%w：买单仓库映射不完整", ErrWarehouseAssignmentUnavailable)
	}
	if !strings.EqualFold(strings.TrimSpace(mapping.TemuWarehouseID), strings.TrimSpace(shipment.WarehouseID)) {
		return fmt.Errorf("%w：购单后仓库映射已变化，请人工核对", ErrWarehouseAssignmentUnavailable)
	}
	if len(lookup.Orders) != 1 || lookup.MatchCount != 1 {
		return fmt.Errorf("%w：领星未找到唯一的同号平台订单", ErrWarehouseAssignmentUnavailable)
	}
	order := lookup.Orders[0]
	if order.Status != 0 || !strings.EqualFold(strings.TrimSpace(order.PlatformOrderNo), strings.TrimSpace(shipment.ParentOrderSN)) {
		return fmt.Errorf("%w：领星平台订单已不在待处理状态", ErrWarehouseAssignmentUnavailable)
	}
	if code := strings.TrimSpace(order.SendWarehouseCode); code != "" && !strings.EqualFold(code, mapping.OMSWarehouseCode) {
		return fmt.Errorf("%w：领星已有不同的发货仓库", ErrWarehouseAssignmentUnavailable)
	}
	return nil
}

func validateWarehouseAssignmentPreview(target warehouseAssignmentTarget, preview oms.WarehouseAssignmentPreview) error {
	if !preview.Ready || len(preview.Routes) != 1 || len(preview.Unresolved) != 0 {
		reason := "无法根据已购面单匹配实际仓库"
		if len(preview.Unresolved) > 0 && strings.TrimSpace(preview.Unresolved[0].Reason) != "" {
			reason = strings.TrimSpace(preview.Unresolved[0].Reason)
		}
		return fmt.Errorf("%w：%s", ErrWarehouseAssignmentUnavailable, reason)
	}
	route := preview.Routes[0]
	if !strings.EqualFold(strings.TrimSpace(route.PlatformOrderNo), target.shipment.ParentOrderSN) ||
		!strings.EqualFold(strings.TrimSpace(route.WarehouseCode), target.mapping.OMSWarehouseCode) {
		return fmt.Errorf("%w：分仓预览与买单仓库不一致", ErrWarehouseAssignmentUnavailable)
	}
	return nil
}

func warehouseAssignmentPreview(target warehouseAssignmentTarget, preview oms.WarehouseAssignmentPreview) WarehouseAssignmentPreview {
	return WarehouseAssignmentPreview{
		ParentOrderSN: target.shipment.ParentOrderSN, OMSOrderNo: target.order.OMSOrderNo,
		OMSAccount: target.account, TrackingNumber: target.shipment.TrackingNumber,
		WarehouseKey: target.mapping.OMSKey, WarehouseCode: target.mapping.OMSWarehouseCode,
		TemuWarehouseID: target.mapping.TemuWarehouseID, TemuWarehouseName: target.mapping.TemuName,
		Ready: preview.Ready, Routes: preview.Routes, Unresolved: preview.Unresolved,
		ChannelCode: preview.ChannelCode, ChannelName: preview.ChannelName,
		Carriers: preview.Carriers, QueriedAt: preview.QueriedAt,
	}
}
