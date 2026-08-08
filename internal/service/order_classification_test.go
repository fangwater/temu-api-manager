package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
)

func TestClassifyOrder(t *testing.T) {
	tests := []struct {
		name    string
		order   model.Order
		reasons []string
		merge   []string
	}{
		{name: "single unit", order: model.Order{ParentOrderSN: "single", Lines: []model.OrderLine{{ExtCode: "SKU-1", Quantity: 1}}}},
		{name: "unbound SKU", order: model.Order{ParentOrderSN: "unbound", Lines: []model.OrderLine{{Quantity: 1}}}, reasons: []string{"sku_unbound"}, merge: []string{}},
		{name: "quantity greater than one", order: model.Order{ParentOrderSN: "multi", Lines: []model.OrderLine{{ExtCode: "SKU-1", Quantity: 2}}}, reasons: []string{"multi_item"}, merge: []string{}},
		{name: "multiple lines", order: model.Order{ParentOrderSN: "lines", Lines: []model.OrderLine{{ExtCode: "SKU-1", Quantity: 1}, {ExtCode: "SKU-2", Quantity: 1}}}, reasons: []string{"multi_item"}, merge: []string{}},
		{name: "merge and consolidated", order: model.Order{ParentOrderSN: "merge", Lines: []model.OrderLine{{ExtCode: "SKU-1", Quantity: 1}}, BatchOrderSNs: []string{"B2", "B1"}, Consolidated: true}, reasons: []string{"merge_candidate", "platform_consolidated"}, merge: []string{"B1", "B2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			review := classifyOrder(test.order)
			if len(test.reasons) == 0 {
				if review != nil {
					t.Fatalf("expected no classification, got %#v", review)
				}
				return
			}
			if review == nil {
				t.Fatal("expected manual classification")
			}
			if !reflect.DeepEqual(review.Reasons, test.reasons) {
				t.Fatalf("reasons = %#v, want %#v", review.Reasons, test.reasons)
			}
			if !reflect.DeepEqual(review.MergeOrderSNs, test.merge) {
				t.Fatalf("merge orders = %#v, want %#v", review.MergeOrderSNs, test.merge)
			}
		})
	}
}

func TestInventoryUnboundSKUs(t *testing.T) {
	decision := inventory.DecisionResponse{Records: []inventory.SKUDecision{
		{SKU: "BOUND", Regions: []inventory.Region{{Warehouses: []inventory.Warehouse{
			{Active: true, QueryStatus: "succeeded", SKUFound: true},
			{Active: true, QueryStatus: "succeeded", SKUFound: false},
		}}}},
		{SKU: "UNBOUND", Regions: []inventory.Region{{Warehouses: []inventory.Warehouse{
			{Active: true, QueryStatus: "succeeded", SKUFound: false},
			{Active: true, QueryStatus: "succeeded", SKUFound: false},
		}}}},
		{SKU: "QUERY-FAILED", Regions: []inventory.Region{{Warehouses: []inventory.Warehouse{
			{Active: true, QueryStatus: "succeeded", SKUFound: false},
			{Active: true, QueryStatus: "failed", SKUFound: false},
		}}}},
	}}
	if got, want := inventoryUnboundSKUs(decision), []string{"UNBOUND"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unbound SKUs = %#v, want %#v", got, want)
	}
}

func TestManualOrderReasonHonorsPersistedReview(t *testing.T) {
	order := model.Order{ManualReview: &model.ManualReview{Active: true, Status: "manual_pending", Reasons: []string{"multi_item"}}}
	if reason := manualOrderReason(order); !strings.Contains(reason, "manual review") {
		t.Fatalf("expected manual review block, got %q", reason)
	}
	order.ManualReview.Status = "approved"
	if reason := manualOrderReason(order); reason != "" {
		t.Fatalf("approved order should pass manual guard, got %q", reason)
	}
	order.ManualReview.Reasons = []string{"sku_unbound"}
	if reason := manualOrderReason(order); !strings.Contains(reason, "sku_unbound") {
		t.Fatalf("approved unbound SKU must remain blocked, got %q", reason)
	}
}

func TestInventoryManualReviewRequiresRecheck(t *testing.T) {
	order := model.Order{ManualReview: &model.ManualReview{Active: true, Status: "approved", Reasons: []string{manualReasonInventoryRule}}}
	if reason := manualOrderReason(order); !strings.Contains(reason, manualReasonInventoryRule) {
		t.Fatalf("approved inventory-rule order must remain blocked, got %q", reason)
	}
	if !warehouseManualReviewCanBeRechecked(order) {
		t.Fatal("inventory-only manual review should allow warehouse recheck")
	}
	order.ManualReview.Reasons = []string{manualReasonInventoryRule, "multi_item"}
	if warehouseManualReviewCanBeRechecked(order) {
		t.Fatal("review with a static manual reason must not bypass the manual guard")
	}
}

func TestWarehouseClassificationFromDecision(t *testing.T) {
	order := model.Order{ParentOrderSN: "PO-1", UpdateTime: 42}
	completePackage := inventory.PackageResolution{Complete: true}
	tests := []struct {
		name       string
		decision   inventory.DecisionResponse
		queryErr   error
		status     string
		categories []string
	}{
		{name: "eligible", decision: inventory.DecisionResponse{PackageResolution: completePackage}, status: "eligible", categories: []string{}},
		{name: "inventory manual", decision: inventory.DecisionResponse{
			PackageResolution: completePackage,
			Records:           []inventory.SKUDecision{{SKU: "SKU-LOW", RequiresManual: true, Reason: "美东或美西库存低于安全线"}},
		}, status: "manual", categories: []string{manualReasonInventoryRule}},
		{name: "unbound SKU", decision: inventory.DecisionResponse{
			PackageResolution: completePackage,
			Records: []inventory.SKUDecision{{SKU: "SKU-MISSING", Regions: []inventory.Region{{Warehouses: []inventory.Warehouse{
				{Active: true, QueryStatus: "succeeded", SKUFound: false},
			}}}}},
		}, status: "manual", categories: []string{"sku_unbound"}},
		{name: "package incomplete", decision: inventory.DecisionResponse{
			PackageResolution: inventory.PackageResolution{Complete: false, Error: "尺寸缺失"},
		}, status: "manual", categories: []string{manualReasonWarehouseSKUSpec}},
		{name: "query failure is retried instead of manual", queryErr: errors.New("timeout"), status: "failed", categories: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := warehouseClassificationFromDecision(order, test.decision, test.queryErr)
			if got.Status != test.status {
				t.Fatalf("status = %q, want %q", got.Status, test.status)
			}
			if !reflect.DeepEqual(got.Categories, test.categories) {
				t.Fatalf("categories = %#v, want %#v", got.Categories, test.categories)
			}
			if got.SourceUpdateAt != order.UpdateTime {
				t.Fatalf("source update time = %d, want %d", got.SourceUpdateAt, order.UpdateTime)
			}
		})
	}
}
