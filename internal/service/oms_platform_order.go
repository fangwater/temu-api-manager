package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"temu-api-manager/internal/model"
	"temu-api-manager/internal/oms"
)

const omsPlatformOrderMissingGracePeriod = 30 * time.Minute

var errOMSManualRequired = errors.New("OMS manual handling required")

type omsPlatformOrderDecision struct {
	State          string
	Message        string
	Verified       bool
	ManualRequired bool
	Target         oms.PlatformOrder
}

func (s *Service) reconcileOMSPlatformOrder(ctx context.Context, shipment model.Shipment, mapping model.WarehouseMapping) (model.Shipment, error) {
	expectedAccount, oppositeAccount, ok := omsAccountSelectorsForWarehouse(mapping)
	if !ok {
		return s.failOMSPlatformOrderQuery(ctx, shipment, mapping, fmt.Errorf("unsupported OMS account ownership for warehouse %s (%s)", mapping.OMSKey, mapping.OMSWarehouseCode))
	}
	expected, err := s.oms.QueryPlatformOrder(ctx, expectedAccount, shipment.ParentOrderSN)
	if err != nil {
		return s.failOMSPlatformOrderQuery(ctx, shipment, mapping, err)
	}
	opposite, err := s.oms.QueryPlatformOrder(ctx, oppositeAccount, shipment.ParentOrderSN)
	if err != nil {
		return s.failOMSPlatformOrderQuery(ctx, shipment, mapping, err)
	}

	decision := decideOMSPlatformOrder(expected, opposite, mapping.OMSWarehouseCode, shipment.ConfirmedAt, time.Now())
	assignment := omsWarehouseAssignmentAuditFromShipment(shipment)
	if decision.State == "pending" {
		var assigned bool
		assignment, assigned, err = s.assignPendingOMSPlatformOrder(ctx, shipment, mapping, expected, assignment)
		if err != nil {
			decision.Message = "领星待处理，仓库物流自动分配失败，等待后台重试：" + err.Error()
			summary := omsPlatformOrderSummary(mapping, expected, opposite, decision, assignment)
			if updateErr := s.store.UpdateOMSSync(ctx, shipment.ID, "waiting_sync", nil, shipment.TrackingNumber, summary, decision.Message); updateErr != nil {
				return shipment, errors.Join(err, updateErr)
			}
			updated, refreshErr := s.store.GetShipment(ctx, shipment.ID)
			if refreshErr != nil {
				return shipment, errors.Join(err, refreshErr)
			}
			return updated, fmt.Errorf("自动分配领星仓库物流: %w", err)
		}
		decision.Message = "领星仓库物流已自动分配，等待状态推进"
		if assigned && s.logger != nil {
			s.logger.Info("OMS pending order warehouse and logistics assigned automatically", "shipment_id", shipment.ID, "oms_account", expectedAccount)
		}
	}
	summary := omsPlatformOrderSummary(mapping, expected, opposite, decision, assignment)
	switch {
	case decision.Verified:
		if err := s.store.UpdateOMSSync(ctx, shipment.ID, "verified", nil, shipment.TrackingNumber, summary, ""); err != nil {
			return shipment, err
		}
		return s.store.GetShipment(ctx, shipment.ID)
	case decision.ManualRequired:
		if err := s.store.UpdateOMSSync(ctx, shipment.ID, "manual_required", nil, shipment.TrackingNumber, summary, decision.Message); err != nil {
			return shipment, err
		}
		updated, refreshErr := s.store.GetShipment(ctx, shipment.ID)
		if refreshErr != nil {
			return shipment, refreshErr
		}
		return updated, fmt.Errorf("%w: %s", errOMSManualRequired, decision.Message)
	default:
		if err := s.store.UpdateOMSSync(ctx, shipment.ID, "waiting_sync", nil, shipment.TrackingNumber, summary, decision.Message); err != nil {
			return shipment, err
		}
		return s.store.GetShipment(ctx, shipment.ID)
	}
}

func decideOMSPlatformOrder(expected, opposite oms.PlatformOrderLookup, expectedWarehouseCode string, confirmedAt *time.Time, now time.Time) omsPlatformOrderDecision {
	expectedWarehouseCode = strings.TrimSpace(expectedWarehouseCode)
	for _, order := range opposite.Orders {
		if order.Status == 2 || order.Status == 3 {
			return omsManualDecision("领星跨账户重复履约风险：非买单账户 %s 存在状态 %d（%s）的订单，需人工核对防止撞库", opposite.Account, order.Status, order.StatusText)
		}
	}

	switch len(expected.Orders) {
	case 0:
		if confirmedAt != nil && !confirmedAt.IsZero() && now.Before(confirmedAt.Add(omsPlatformOrderMissingGracePeriod)) {
			return omsPlatformOrderDecision{State: "missing", Message: "领星尚未同步平台订单，等待下一次查询"}
		}
		return omsManualDecision("领星漏单：买单仓库 %s 对应账户无平台订单，需手动补件", expectedWarehouseCode)
	case 1:
	default:
		return omsManualDecision("领星买单账户返回 %d 条同号平台订单，需人工核对", len(expected.Orders))
	}

	target := expected.Orders[0]
	actualWarehouseCode := strings.TrimSpace(target.SendWarehouseCode)
	if actualWarehouseCode != "" && !strings.EqualFold(actualWarehouseCode, expectedWarehouseCode) {
		return omsManualDecision("领星仓库不一致：买单仓库为 %s，平台订单发货仓为 %s", expectedWarehouseCode, actualWarehouseCode)
	}
	if (target.Status == 2 || target.Status == 3) && actualWarehouseCode == "" {
		return omsManualDecision("领星平台订单已进入履约状态，但缺少发货仓代码，无法确认买单仓库")
	}

	decision := omsPlatformOrderDecision{State: target.StatusKey, Target: target}
	switch target.Status {
	case 0:
		decision.State = "pending"
		decision.Message = "领星待处理（待匹配）"
	case 1:
		decision.State = "awaiting_platform_label"
		decision.Message = "领星待获取平台面单"
	case 2:
		decision.State = "processing"
		decision.Message = "领星已买单-处理中"
		decision.Verified = true
	case 3:
		decision.State = "shipped"
		decision.Message = "领星已买单-已发货"
		decision.Verified = true
	case 4:
		return omsManualDecision("领星买单仓平台订单已取消，需人工处理")
	case 5:
		return omsManualDecision("领星买单仓平台订单异常，需人工处理")
	case 6:
		return omsManualDecision("领星买单仓平台订单处于待开票，Temu 自动发货无法归档")
	default:
		return omsManualDecision("领星返回未知平台订单状态 %d，需人工处理", target.Status)
	}
	return decision
}

func omsManualDecision(format string, values ...any) omsPlatformOrderDecision {
	return omsPlatformOrderDecision{
		State: "manual_required", Message: fmt.Sprintf(format, values...), ManualRequired: true,
	}
}

func omsAccountSelectorsForWarehouse(mapping model.WarehouseMapping) (string, string, bool) {
	account, ok := normalizeOMSAccount(mapping.OMSAccount)
	if !ok {
		return "", "", false
	}
	switch account {
	case "dps":
		return "dps", "arp", true
	case "arp":
		return "arp", "dps", true
	default:
		return "", "", false
	}
}

func normalizeOMSAccount(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "dps", "arp":
		return value, true
	default:
		return "", false
	}
}

func omsPlatformOrderSummary(
	mapping model.WarehouseMapping,
	expected, opposite oms.PlatformOrderLookup,
	decision omsPlatformOrderDecision,
	assignment *omsWarehouseAssignmentAudit,
) json.RawMessage {
	type orderSummary struct {
		OMSOrderNo        string `json:"oms_order_no,omitempty"`
		Status            int    `json:"status"`
		StatusKey         string `json:"status_key"`
		SendWarehouseCode string `json:"send_warehouse_code,omitempty"`
		AuditTime         string `json:"audit_time,omitempty"`
	}
	summarize := func(lookup oms.PlatformOrderLookup) []orderSummary {
		result := make([]orderSummary, 0, len(lookup.Orders))
		for _, order := range lookup.Orders {
			result = append(result, orderSummary{
				OMSOrderNo: order.OMSOrderNo, Status: order.Status, StatusKey: order.StatusKey,
				SendWarehouseCode: order.SendWarehouseCode, AuditTime: order.AuditTime,
			})
		}
		return result
	}
	summary := map[string]any{
		"source": "oms_platform_order", "decision": decision.State,
		"expected": map[string]any{
			"account": expected.Account, "warehouse_key": mapping.OMSKey,
			"warehouse_code": mapping.OMSWarehouseCode, "configured_account": mapping.OMSAccount, "found": expected.Found,
			"match_count": len(expected.Orders), "orders": summarize(expected),
		},
		"opposite": map[string]any{
			"account": opposite.Account, "found": opposite.Found,
			"match_count": len(opposite.Orders), "orders": summarize(opposite),
		},
	}
	if assignment != nil {
		summary["warehouse_assignment"] = assignment
	}
	raw, _ := json.Marshal(summary)
	return raw
}

func omsWarehouseAssignmentAuditFromShipment(shipment model.Shipment) *omsWarehouseAssignmentAudit {
	if shipment.OMSSync == nil || len(shipment.OMSSync.Summary) == 0 {
		return nil
	}
	var summary struct {
		WarehouseAssignment *omsWarehouseAssignmentAudit `json:"warehouse_assignment"`
	}
	if err := json.Unmarshal(shipment.OMSSync.Summary, &summary); err != nil {
		return nil
	}
	return summary.WarehouseAssignment
}

func (s *Service) failOMSPlatformOrderQuery(ctx context.Context, shipment model.Shipment, mapping model.WarehouseMapping, cause error) (model.Shipment, error) {
	summary := omsPlatformOrderFailureSummary(shipment, mapping)
	_ = s.store.UpdateOMSSync(context.WithoutCancel(ctx), shipment.ID, "failed", nil, shipment.TrackingNumber, summary, cause.Error())
	return shipment, cause
}

func omsPlatformOrderFailureSummary(shipment model.Shipment, mapping model.WarehouseMapping) json.RawMessage {
	summary := map[string]any{
		"source": "oms_platform_order", "warehouse_key": mapping.OMSKey,
		"warehouse_code": mapping.OMSWarehouseCode, "configured_account": mapping.OMSAccount,
	}
	if assignment := omsWarehouseAssignmentAuditFromShipment(shipment); assignment != nil {
		summary["warehouse_assignment"] = assignment
	}
	raw, _ := json.Marshal(summary)
	return raw
}

func autoFulfillmentOMSFailureStatus(err error) string {
	if errors.Is(err, errOMSManualRequired) {
		return "failed"
	}
	return "waiting_oms"
}
