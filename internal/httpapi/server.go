package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/oms"
	"temu-api-manager/internal/service"

	"github.com/jackc/pgx/v5"
)

type Server struct {
	service      *service.Service
	operationKey string
	storeCode    string
	storeName    string
	staticRoot   string
	timeout      time.Duration
	logger       *slog.Logger
}

type response struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Meta    any    `json:"meta,omitempty"`
}

func New(service *service.Service, operationKey, storeCode, storeName, staticRoot string, timeout time.Duration, logger *slog.Logger) http.Handler {
	s := &Server{service: service, operationKey: operationKey, storeCode: storeCode, storeName: storeName, staticRoot: staticRoot, timeout: timeout, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/system/token-status", s.tokenStatus)
	mux.HandleFunc("GET /api/system/store", s.storeIdentity)
	mux.HandleFunc("GET /api/orders", s.listOrders)
	mux.HandleFunc("GET /api/substitution-orders", s.listSubstitutionOrders)
	mux.HandleFunc("POST /api/substitution-orders/{parentOrderSN}/quotes", s.compareSubstitutionPrices)
	mux.HandleFunc("POST /api/substitution-orders/{parentOrderSN}/purchase", s.purchaseSubstitution)
	mux.HandleFunc("GET /api/combined-shipment-candidates", s.combinedShipmentCandidates)
	mux.HandleFunc("GET /api/orders/history", s.listOrderHistory)
	mux.HandleFunc("GET /api/orders/{parentOrderSN}/detail", s.getOrderDetail)
	mux.HandleFunc("GET /api/orders/{parentOrderSN}/shipment", s.getOrderShipment)
	mux.HandleFunc("GET /api/orders/{parentOrderSN}/tracking", s.orderTracking)
	mux.HandleFunc("GET /api/orders/{parentOrderSN}", s.getOrder)
	mux.HandleFunc("POST /api/orders/sync", s.syncOrders)
	mux.HandleFunc("POST /api/orders/details/sync", s.syncOrderDetails)
	mux.HandleFunc("POST /api/pickup-audits/sync", s.syncPendingPickupAudits)
	mux.HandleFunc("GET /api/manual-orders", s.listManualOrders)
	mux.HandleFunc("GET /api/manual-orders/export.csv", s.exportManualOrdersCSV)
	mux.HandleFunc("PUT /api/orders/{parentOrderSN}/manual-review", s.requireOperationKey(s.updateManualOrder))
	mux.HandleFunc("GET /api/warehouses", s.listWarehouses)
	mux.HandleFunc("POST /api/warehouses/sync", s.syncWarehouses)
	mux.HandleFunc("PUT /api/warehouse-mappings/{omsKey}", s.requireOperationKey(s.setWarehouseMapping))
	mux.HandleFunc("DELETE /api/warehouse-mappings/{omsKey}", s.requireOperationKey(s.deleteWarehouseMapping))
	mux.HandleFunc("GET /api/carrier-policies", fulfillmentPoliciesMoved)
	mux.HandleFunc("PUT /api/carrier-policies/{warehouseKey}", fulfillmentPoliciesMoved)
	mux.HandleFunc("GET /api/sku-warehouse-rules", fulfillmentPoliciesMoved)
	mux.HandleFunc("PUT /api/sku-warehouse-rules", fulfillmentPoliciesMoved)
	mux.HandleFunc("GET /api/inventory-thresholds", s.listInventoryThresholds)
	mux.HandleFunc("GET /api/inventory-thresholds/defaults", s.inventoryThresholdDefaults)
	mux.HandleFunc("PATCH /api/inventory-thresholds/defaults", s.updateInventoryThresholdDefaults)
	mux.HandleFunc("POST /api/inventory-thresholds/defaults/reset", inventoryThresholdDefaultResetGone)
	mux.HandleFunc("PATCH /api/inventory-thresholds/{warehouseSKU}", s.updateSKUInventoryThreshold)
	mux.HandleFunc("POST /api/inventory-thresholds/{warehouseSKU}/reset", s.resetSKUInventoryThreshold)
	mux.HandleFunc("POST /api/orders/{parentOrderSN}/warehouse-preview", s.previewWarehouses)
	mux.HandleFunc("PATCH /api/warehouse-sku-specs/{warehouseSKU}/package", s.updateWarehouseSKUPackageSpec)
	mux.HandleFunc("POST /api/shipping/quotes", s.createQuote)
	mux.HandleFunc("POST /api/shipping/split-plan", s.prepareSplitPlan)
	mux.HandleFunc("POST /api/shipping/split-quotes", s.quoteSplitPlan)
	mux.HandleFunc("POST /api/shipping/purchase", s.purchase)
	mux.HandleFunc("POST /api/auto-fulfillment/batches", s.startBulkFulfillment)
	mux.HandleFunc("POST /api/auto-fulfillment/batches/restart", s.restartBulkFulfillment)
	mux.HandleFunc("GET /api/auto-fulfillment/batches/latest", s.latestBulkFulfillment)
	mux.HandleFunc("POST /api/substitution-fulfillment/batches", s.startSubstitutionBulkFulfillment)
	mux.HandleFunc("POST /api/substitution-fulfillment/batches/restart", s.restartSubstitutionBulkFulfillment)
	mux.HandleFunc("GET /api/substitution-fulfillment/batches/latest", s.latestSubstitutionBulkFulfillment)
	mux.HandleFunc("GET /api/oms-platform-orders", s.listOMSPlatformOrders)
	mux.HandleFunc("POST /api/oms-platform-orders/{parentOrderSN}/terminal", s.requireOperationKey(s.resolveOMSPlatformOrderTerminal))
	mux.HandleFunc("GET /api/oms-platform-orders/{parentOrderSN}/warehouse-assignment-preview", s.previewOMSPlatformOrderWarehouseAssignment)
	mux.HandleFunc("POST /api/oms-platform-orders/{parentOrderSN}/warehouse-assignment", s.assignOMSPlatformOrderWarehouse)
	mux.HandleFunc("GET /api/shipments", s.listShipments)
	mux.HandleFunc("POST /api/shipments/lookup", s.lookupOrderShipments)
	mux.HandleFunc("GET /api/shipments/export-po.zip", s.exportShipmentPOZIP)
	mux.HandleFunc("GET /api/shipments/export-po.csv", s.exportShipmentPOZIP)
	mux.HandleFunc("GET /api/packages/{packageSn}/tracking", s.packageTracking)
	mux.HandleFunc("GET /api/shipments/{id}", s.getShipment)
	mux.HandleFunc("POST /api/shipments/{id}/refresh", s.refreshShipment)
	mux.HandleFunc("POST /api/shipments/{id}/recovery/warehouse-preview", s.previewFailedShipmentRecovery)
	mux.HandleFunc("POST /api/shipments/{id}/recovery/quotes", s.quoteFailedShipmentRecovery)
	mux.HandleFunc("POST /api/shipments/{id}/recovery/resubmit", s.resubmitFailedShipment)
	mux.HandleFunc("GET /api/shipments/{id}/documents", s.shipmentDocuments)
	mux.HandleFunc("GET /api/shipments/{id}/label", s.downloadLabel)
	mux.HandleFunc("POST /api/shipments/{id}/confirm", s.confirmShipment)
	mux.HandleFunc("POST /api/shipments/{id}/oms-sync-check", s.checkOMSSync)
	mux.Handle("/", s.staticHandler())
	return stripTemuPrefix(securityHeaders(mux))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]any{"status": "ok", "service": "temu-go", "store_code": s.storeCode, "store_name": s.storeName}})
}

func inventoryThresholdDefaultResetGone(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusGone, response{Success: false, Error: "platform inventory thresholds have no parent default"})
}

func (s *Server) storeIdentity(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]string{"code": s.storeCode, "name": s.storeName}})
}

func (s *Server) tokenStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.TokenStatus(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) syncOrders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.SyncOrders(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	page, pageSize := pagination(r)
	ctx, cancel := s.context(r)
	defer cancel()
	items, total, err := s.service.ListOrders(ctx, r.URL.Query().Get("q"), r.URL.Query().Get("queue") == "pending", page, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	syncStatus, _ := s.service.LatestSync(ctx)
	writeJSON(w, http.StatusOK, response{Success: true, Data: items, Meta: map[string]any{"page": page, "page_size": pageSize, "total": total, "sync": syncStatus}})
}

func (s *Server) combinedShipmentCandidates(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.CombinedShipmentCandidates(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.GetOrder(ctx, r.PathValue("parentOrderSN"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) syncOrderDetails(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 20 {
		limit = 10
	}
	ctx, cancel := s.context(r)
	defer cancel()
	completed, err := s.service.SyncOrderDetails(ctx, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]int{"completed": completed}})
}

func (s *Server) syncPendingPickupAudits(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout*10)
	defer cancel()
	result, err := s.service.SyncPendingPickupAudits(ctx, s.storeCode, s.storeName)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: result})
}

func (s *Server) getOrderDetail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.RefreshOrderDetail(ctx, r.PathValue("parentOrderSN"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) listOrderHistory(w http.ResponseWriter, r *http.Request) {
	page, pageSize := pagination(r)
	ctx, cancel := s.context(r)
	defer cancel()
	items, total, err := s.service.ListOrderHistory(ctx, page, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: items,
		Meta: map[string]any{"page": page, "page_size": pageSize, "total": total}})
}

func (s *Server) listManualOrders(w http.ResponseWriter, r *http.Request) {
	page, pageSize := pagination(r)
	ctx, cancel := s.context(r)
	defer cancel()
	items, total, err := s.service.ListManualReviews(ctx, r.URL.Query().Get("status"), r.URL.Query().Get("q"), page, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: items,
		Meta: map[string]any{"page": page, "page_size": pageSize, "total": total}})
}

func (s *Server) exportManualOrdersCSV(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	items, err := s.service.ListAllManualReviews(ctx, r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var body bytes.Buffer
	if err := writeManualOrdersCSV(&body, s.storeName, items); err != nil {
		s.fail(w, err)
		return
	}
	filename := "temu-manual-orders-" + strings.ToLower(sanitizeWarehouseFilename(s.storeCode)) + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}

func writeManualOrdersCSV(destination io.Writer, shopName string, items []model.ManualReview) error {
	if _, err := io.WriteString(destination, "\xEF\xBB\xBF"); err != nil {
		return err
	}
	writer := csv.NewWriter(destination)
	if err := writer.Write([]string{"店铺", "PO单号", "子订单号", "人工状态", "处理结果", "备注", "分类", "原因详情", "仓库SKU", "数量", "商品名称", "规格", "合并订单", "识别时间", "完成时间", "更新时间"}); err != nil {
		return err
	}
	for _, item := range items {
		lines := item.Lines
		if len(lines) == 0 {
			lines = []model.OrderLine{{}}
		}
		reasons := make([]string, 0, len(item.Reasons))
		for _, reason := range item.Reasons {
			reasons = append(reasons, manualReasonExportText(reason))
		}
		for _, line := range lines {
			resolvedAt := ""
			if item.ResolvedAt != nil {
				resolvedAt = item.ResolvedAt.In(time.Local).Format("2006-01-02 15:04:05")
			}
			row := []string{
				shopName,
				item.ParentOrderSN,
				line.OrderSN,
				manualStatusExportText(item.Status),
				manualOutcomeExportText(item.Outcome),
				item.Note,
				strings.Join(reasons, "；"),
				strings.Join(item.Details, "；"),
				line.ExtCode,
				strconv.Itoa(line.Quantity),
				line.GoodsName,
				line.Spec,
				strings.Join(item.MergeOrderSNs, "；"),
				item.DetectedAt.In(time.Local).Format("2006-01-02 15:04:05"),
				resolvedAt,
				item.UpdatedAt.In(time.Local).Format("2006-01-02 15:04:05"),
			}
			if err := writer.Write(row); err != nil {
				return err
			}
		}
	}
	writer.Flush()
	return writer.Error()
}

func manualReasonExportText(reason string) string {
	labels := map[string]string{
		"sku_unbound":                        "SKU 未绑定",
		"inventory_rule":                     "库存安全线不足",
		"warehouse_sku_spec_incomplete":      "仓库 SKU 包裹数据缺失",
		"platform_sku_warehouse_restriction": "平台 SKU 发货仓库受限",
		"shop_sku_warehouse_restriction":     "店铺 SKU 发货仓库受限",
		"delivery_address_unsupported":       "偏远地址物流不支持",
		"multi_item":                         "一单多件",
		"merge_candidate":                    "合并候选",
		"platform_consolidated":              "Temu 已合并",
	}
	if label := labels[reason]; label != "" {
		return label
	}
	return reason
}

func manualStatusExportText(status string) string {
	labels := map[string]string{
		"detected":       "待转人工",
		"manual_pending": "人工处理中",
		"approved":       "已批准自动发货",
		"resolved":       "人工履约完成",
	}
	if label := labels[status]; label != "" {
		return label
	}
	return status
}

func manualOutcomeExportText(outcome string) string {
	labels := map[string]string{
		"manually_fulfilled": "已人工发货",
		"cancelled":          "订单已取消",
		"not_required":       "无需履约",
		"other":              "其他已处理",
	}
	if label := labels[outcome]; label != "" {
		return label
	}
	return outcome
}

func (s *Server) updateManualOrder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status  string `json:"status"`
		Outcome string `json:"outcome"`
		Note    string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.UpdateManualReview(ctx, r.PathValue("parentOrderSN"), input.Status, input.Outcome, input.Note)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}
func (s *Server) syncWarehouses(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	warehouses, mappings, err := s.service.SyncWarehouses(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]any{"warehouses": warehouses, "mappings": mappings, "shared": true}})
}

func (s *Server) listWarehouses(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	warehouses, mappings, err := s.service.ListWarehouses(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]any{"warehouses": warehouses, "mappings": mappings, "shared": true}})
}

func (s *Server) setWarehouseMapping(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TemuWarehouseID  string `json:"temu_warehouse_id"`
		OMSWarehouseCode string `json:"oms_warehouse_code"`
		Enabled          *bool  `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	item, err := s.service.SetWarehouseMapping(
		ctx, r.PathValue("omsKey"), input.TemuWarehouseID,
		input.OMSWarehouseCode, enabled,
	)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) deleteWarehouseMapping(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	if err := s.service.DeleteWarehouseMapping(ctx, r.PathValue("omsKey")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]any{"deleted": true}})
}

func fulfillmentPoliciesMoved(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusGone, response{Success: false, Error: "发货策略已迁移到 XLWMS 仓库运营中台"})
}

func (s *Server) listInventoryThresholds(w http.ResponseWriter, r *http.Request) {
	page, pageSize := pagination(r)
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.ListPlatformSKUInventoryThresholds(ctx, r.URL.Query().Get("q"), page, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item.Records, Meta: map[string]any{
		"page": item.Page, "page_size": item.PageSize, "total": item.Total, "pages": item.Pages,
		"default_thresholds": item.DefaultThresholds,
	}})
}

func (s *Server) inventoryThresholdDefaults(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PlatformInventoryThresholds(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) updateInventoryThresholdDefaults(w http.ResponseWriter, r *http.Request) {
	var input inventory.InventoryThresholds
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.UpdatePlatformInventoryThresholds(ctx, input)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) updateSKUInventoryThreshold(w http.ResponseWriter, r *http.Request) {
	var input inventory.InventoryThresholds
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.UpdatePlatformSKUInventoryThreshold(ctx, r.PathValue("warehouseSKU"), input)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) resetSKUInventoryThreshold(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	if err := s.service.ResetPlatformSKUInventoryThreshold(ctx, r.PathValue("warehouseSKU")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]bool{"deleted": true}})
}

func (s *Server) startBulkFulfillment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.StartBulkFulfillment(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response{Success: true, Data: item})
}

func (s *Server) restartBulkFulfillment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.RestartBulkFulfillment(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response{Success: true, Data: item})
}

func (s *Server) latestBulkFulfillment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.LatestBulkFulfillment(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]any{}})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) startSubstitutionBulkFulfillment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.StartSubstitutionBulkFulfillment(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response{Success: true, Data: item})
}

func (s *Server) restartSubstitutionBulkFulfillment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.RestartSubstitutionBulkFulfillment(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response{Success: true, Data: item})
}

func (s *Server) latestSubstitutionBulkFulfillment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.LatestSubstitutionBulkFulfillment(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]any{}})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) autoShip(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.EnqueueAutoFulfillment(ctx, r.PathValue("parentOrderSN"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response{Success: true, Data: item})
}

func (s *Server) previewWarehouses(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PreviewWarehouses(ctx, r.PathValue("parentOrderSN"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) updateWarehouseSKUPackageSpec(w http.ResponseWriter, r *http.Request) {
	var input inventory.PackageSpecUpdate
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.UpdateWarehouseSKUPackageSpec(ctx, r.PathValue("warehouseSKU"), input)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) createQuote(w http.ResponseWriter, r *http.Request) {
	var input service.QuoteRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.Quote(ctx, input)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response{Success: true, Data: item})
}

func (s *Server) purchase(w http.ResponseWriter, r *http.Request) {
	var input struct {
		QuoteID string `json:"quote_id"`
		Confirm bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PurchaseAndQueueCompletion(ctx, input.QuoteID)
	if err != nil {
		s.fail(w, err)
		return
	}
	status := http.StatusCreated
	if item.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, response{Success: true, Data: item})
}

func (s *Server) listShipments(w http.ResponseWriter, r *http.Request) {
	page, pageSize := pagination(r)
	queue := r.URL.Query().Get("queue")
	ctx, cancel := s.context(r)
	defer cancel()
	items, total, err := s.service.ListShipments(ctx, queue, page, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts, err := s.service.ShipmentStatusCounts(ctx, queue)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: items, Meta: map[string]any{"page": page, "page_size": pageSize, "total": total, "status_counts": counts}})
}

func (s *Server) listOMSPlatformOrders(w http.ResponseWriter, r *http.Request) {
	status, err := parseOMSPlatformOrderStatus(r.URL.Query().Get("status"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	page, pageSize := pagination(r)
	ctx, cancel := s.context(r)
	defer cancel()
	items, total, counts, err := s.service.ListOMSPlatformOrderStatuses(ctx, status, page, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: items, Meta: map[string]any{
		"page": page, "page_size": pageSize, "total": total, "status": status, "status_counts": counts,
	}})
}

func (s *Server) previewOMSPlatformOrderWarehouseAssignment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PreviewWarehouseAssignment(ctx, r.PathValue("parentOrderSN"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) assignOMSPlatformOrderWarehouse(w http.ResponseWriter, r *http.Request) {
	var input struct {
		LogisticsCarrier string `json:"logistics_carrier"`
		Confirm          bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.AssignWarehouse(ctx, r.PathValue("parentOrderSN"), input.LogisticsCarrier)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) resolveOMSPlatformOrderTerminal(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TerminalStatus string `json:"terminal_status"`
		TerminalNote   string `json:"terminal_note"`
		Confirm        bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.ResolveMissingOMSPlatformOrder(ctx, r.PathValue("parentOrderSN"), input.TerminalStatus, input.TerminalNote)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func parseOMSPlatformOrderStatus(value string) (int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "missing" {
		return -1, nil
	}
	if value == "terminal" {
		return -2, nil
	}
	status, err := strconv.Atoi(value)
	if err != nil || status < 0 || status > 3 {
		return 0, errors.New("status must be terminal, missing, 0, 1, 2, or 3")
	}
	return status, nil
}

func (s *Server) exportShipmentPOZIP(w http.ResponseWriter, r *http.Request) {
	from, before, err := shipmentExportRange(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	groups, err := s.service.ListShipmentPOGroups(ctx, from, before)
	if err != nil {
		s.fail(w, err)
		return
	}
	var body bytes.Buffer
	zipWriter := zip.NewWriter(&body)
	if len(groups) == 0 {
		groups = append(groups, model.ShipmentPOGroup{WarehouseKey: "NO_DATA", PONumbers: []string{}})
	}
	for _, group := range groups {
		entry, createErr := zipWriter.Create(sanitizeWarehouseFilename(group.WarehouseKey) + ".csv")
		if createErr != nil {
			_ = zipWriter.Close()
			s.fail(w, createErr)
			return
		}
		_, _ = entry.Write([]byte("\xEF\xBB\xBF"))
		writer := csv.NewWriter(entry)
		_ = writer.Write([]string{"PO单号"})
		for _, parentOrderSN := range group.PONumbers {
			_ = writer.Write([]string{parentOrderSN})
		}
		writer.Flush()
		if writerErr := writer.Error(); writerErr != nil {
			_ = zipWriter.Close()
			s.fail(w, writerErr)
			return
		}
	}
	if err := zipWriter.Close(); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="temu-auto-shipment-po-by-warehouse.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}

func shipmentExportRange(r *http.Request) (*time.Time, *time.Time, error) {
	parse := func(name string) (*time.Time, error) {
		value := strings.TrimSpace(r.URL.Query().Get(name))
		if value == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, fmt.Errorf("%s must be RFC3339 with timezone", name)
		}
		parsed = parsed.Truncate(time.Minute)
		return &parsed, nil
	}
	from, err := parse("from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parse("to")
	if err != nil {
		return nil, nil, err
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, errors.New("from must not be later than to")
	}
	if to != nil {
		inclusiveMinuteEnd := to.Add(time.Minute)
		to = &inclusiveMinuteEnd
	}
	return from, to, nil
}

func sanitizeWarehouseFilename(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "UNMAPPED"
	}
	return result
}

func (s *Server) getShipment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.GetShipment(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) getOrderShipment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.ShipmentForOrder(ctx, r.PathValue("parentOrderSN"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) lookupOrderShipments(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ParentOrderSNs []string `json:"parent_order_sns"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	items, err := s.service.ShipmentsForOrders(ctx, input.ParentOrderSNs)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: items})
}

func (s *Server) packageTracking(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PackageTracking(ctx, r.PathValue("packageSn"), r.URL.Query().Get("language"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) orderTracking(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.OrderTracking(ctx, r.PathValue("parentOrderSN"), r.URL.Query().Get("language"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: struct {
		StoreCode string `json:"store_code"`
		StoreName string `json:"store_name"`
		service.OrderTrackingResult
	}{StoreCode: s.storeCode, StoreName: s.storeName, OrderTrackingResult: item}})
}

func (s *Server) refreshShipment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.RefreshShipment(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) previewFailedShipmentRecovery(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PreviewFailedShipmentRecovery(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) quoteFailedShipmentRecovery(w http.ResponseWriter, r *http.Request) {
	var input service.QuoteRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.QuoteFailedShipmentRecovery(ctx, r.PathValue("id"), input)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response{Success: true, Data: item})
}

func (s *Server) resubmitFailedShipment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		QuoteID string `json:"quote_id"`
		Confirm bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.RecoverFailedShipmentAndQueueCompletion(ctx, r.PathValue("id"), input.QuoteID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response{Success: true, Data: item})
}

func (s *Server) shipmentDocuments(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.LabelDocuments(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) downloadLabel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	body, contentType, filename, err := s.service.DownloadLabel(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)

}

func (s *Server) confirmShipment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.ConfirmShipped(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) checkOMSSync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.CheckOMSSync(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) requireOperationKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := []byte(r.Header.Get("X-Temu-Operation-Key"))
		expected := []byte(s.operationKey)
		if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
			writeJSON(w, http.StatusUnauthorized, response{Success: false, Error: "操作密钥无效"})
			return
		}
		next(w, r)
	}
}

func (s *Server) context(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.timeout)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, pgx.ErrNoRows) {
		status = http.StatusNotFound
	} else if errors.Is(err, service.ErrPackageSNNotReady) {
		status = http.StatusConflict
	} else if errors.Is(err, service.ErrWarehouseAssignmentUnavailable) {
		status = http.StatusConflict
	} else if strings.Contains(err.Error(), "already has shipment") || strings.Contains(err.Error(), "manual") || strings.Contains(err.Error(), "mapping required") {
		status = http.StatusConflict
	} else if strings.Contains(err.Error(), "connect to PostgreSQL") || strings.Contains(err.Error(), "invalid JSON") {
		status = http.StatusBadGateway
	} else if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
		status = http.StatusGatewayTimeout
		err = errors.New("上游查询超时，请重试")
	} else {
		var gatewayErr *oms.GatewayError
		if errors.As(err, &gatewayErr) && gatewayErr.StatusCode == http.StatusConflict {
			status = http.StatusConflict
		} else if errors.As(err, &gatewayErr) {
			status = http.StatusBadGateway
		}
	}
	if status >= 500 {
		s.logger.Error("Temu API request failed", "error", err)
	}
	writeJSON(w, status, response{Success: false, Error: err.Error()})
}

func (s *Server) staticHandler() http.Handler {
	root := os.DirFS(s.staticRoot)
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, response{Success: false, Error: "API endpoint not found"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeJSON(w, http.StatusMethodNotAllowed, response{Success: false, Error: "method not allowed"})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "dashboard.html"
		}
		if _, err := fs.Stat(root, path); err != nil {
			path = "dashboard.html"
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/" + filepath.ToSlash(path)
		files.ServeHTTP(w, clone)
	})
}

func stripTemuPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/temu" {
			r.URL.Path = "/"
		} else if strings.HasPrefix(r.URL.Path, "/temu/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/temu")
		}
		next.ServeHTTP(w, r)
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "请求JSON无效: " + err.Error()})
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, payload response) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func pagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	return page, pageSize
}
