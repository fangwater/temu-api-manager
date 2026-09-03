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
	for _, table := range []string{
		"temu_order_details",
		"detail_payload jsonb NOT NULL",
		"temu_order_manual_reviews",
		"outcome IN ('', 'manually_fulfilled', 'cancelled', 'not_required', 'other')",
		"note text NOT NULL",
		"resolved_at timestamptz",
	} {
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

func TestInitSQLIncludesOMSManualRequiredStatus(t *testing.T) {
	if !strings.Contains(InitSQL, "'failed', 'manual_required', 'terminal'") {
		t.Fatal("InitSQL is missing the OMS manual-required terminal status")
	}
}

func TestInitSQLIncludesFulfillmentModesAndOMSTerminalFields(t *testing.T) {
	for _, fragment := range []string{
		"fulfillment_mode IN ('direct', 'substitution')",
		"temu_bulk_fulfillment_batches_one_running_mode_idx",
		"terminal_status text NOT NULL DEFAULT ''",
		"'manually_fulfilled', 'cancelled', 'not_required', 'other'",
	} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}

func TestInitSQLIncludesAtomicBulkInventoryReservations(t *testing.T) {
	for _, fragment := range []string{
		"temu_bulk_inventory_budgets",
		"reserved_quantity integer NOT NULL DEFAULT 0",
		"reserved_quantity <= capacity",
		"temu_bulk_inventory_reservations",
		"PRIMARY KEY(batch_id, parent_order_sn, warehouse_sku)",
	} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}

func TestInitSQLMovesOMSAccountOwnershipToQuoteSnapshot(t *testing.T) {
	for _, fragment := range []string{
		"DROP COLUMN IF EXISTS oms_account",
		"oms_account text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS oms_account text NOT NULL DEFAULT ''",
	} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
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

func TestInitSQLIncludesConfigurableWarehouseMappingState(t *testing.T) {
	if !strings.Contains(InitSQL, "ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true") {
		t.Fatal("InitSQL is missing configurable warehouse mapping state")
	}
}

func TestInitSQLDoesNotRecreateMovedFulfillmentPolicies(t *testing.T) {
	for _, fragment := range []string{"public.temu_carrier_policies", "public.temu_sku_disabled_warehouses"} {
		if strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL still owns moved table %q", fragment)
		}
	}
}

func TestInitSQLIncludesLabelPurchaseChoiceAnalysis(t *testing.T) {
	for _, fragment := range []string{
		"temu_label_purchase_choices",
		"selected_price_rank smallint",
		"temu_label_purchase_candidates",
		"price_rank BETWEEN 1 AND 3",
		"is_selected boolean",
	} {
		if !strings.Contains(InitSQL, fragment) {
			t.Fatalf("InitSQL is missing %q", fragment)
		}
	}
}
