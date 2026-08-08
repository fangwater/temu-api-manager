package migrations

import (
	"strings"
	"testing"
)

func TestInitSQLUpgradesTemuProductIDsToBigint(t *testing.T) {
	for _, migration := range []string{
		"ALTER COLUMN goods_id TYPE bigint USING goods_id::bigint",
		"ALTER COLUMN sku_id TYPE bigint USING sku_id::bigint",
	} {
		if !strings.Contains(InitSQL, migration) {
			t.Fatalf("InitSQL is missing %q", migration)
		}
	}
}

func TestInitSQLIncludesOrderDetailAndManualReviewStorage(t *testing.T) {
	for _, table := range []string{"temu_order_details", "detail_payload jsonb NOT NULL", "temu_order_manual_reviews"} {
		if !strings.Contains(InitSQL, table) {
			t.Fatalf("InitSQL is missing %q", table)
		}
	}
}

func TestInitSQLIncludesWarehouseClassificationStorage(t *testing.T) {
	for _, fragment := range []string{"temu_order_warehouse_checks", "status IN ('eligible', 'manual', 'failed')", "reason_details text[]"} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}

func TestInitSQLIncludesBulkFulfillmentBatchStorage(t *testing.T) {
	for _, fragment := range []string{"temu_bulk_fulfillment_batches", "temu_bulk_fulfillment_items", "status IN ('pending', 'running', 'succeeded', 'failed')"} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}

func TestInitSQLIncludesSkippedAutoFulfillmentStatus(t *testing.T) {
	if !strings.Contains(InitSQL, "'waiting_oms', 'completed', 'skipped', 'failed'") {
		t.Fatal("InitSQL is missing the skipped auto-fulfillment terminal status")
	}
}

func TestInitSQLIncludesSafeShipmentSubmissionRetryStorage(t *testing.T) {
	for _, fragment := range []string{"submission_attempts integer", "last_submission_at timestamptz", "GREATEST(submission_attempts,1)", "confirmation_attempts integer", "last_confirmation_at timestamptz", "failed_carrier_codes text[]"} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}

func TestInitSQLIncludesSharedShopWarehouseStorage(t *testing.T) {
	for _, fragment := range []string{"public.temu_shop_warehouses", "logical_warehouse_key text", "PRIMARY KEY(shop_code,logical_warehouse_key)"} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}

func TestInitSQLIncludesShopWarehouseCarrierPolicies(t *testing.T) {
	for _, fragment := range []string{"public.temu_carrier_policies", "shop_code text NOT NULL", "oms_warehouse_key text NOT NULL", "PRIMARY KEY (shop_code, oms_warehouse_key, carrier_code)"} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}
