package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/model"
	"temu-api-manager/internal/temu"
)

func TestValidateSKUWarehouseRuleUsesNegativeConfiguration(t *testing.T) {
	warehouseSKU, disabled, err := validateSKUWarehouseRule(" SKU-1 ", []string{"dps004", "DPS002", "DPS004"})
	if err != nil {
		t.Fatal(err)
	}
	if warehouseSKU != "SKU-1" {
		t.Fatalf("warehouse SKU = %q", warehouseSKU)
	}
	if !reflect.DeepEqual(disabled, []string{"DPS002", "DPS004"}) {
		t.Fatalf("disabled warehouses = %#v", disabled)
	}
	if _, disabled, err := validateSKUWarehouseRule("SKU-2", nil); err != nil || len(disabled) != 0 {
		t.Fatalf("empty configuration must restore the all-warehouse default: disabled=%v err=%v", disabled, err)
	}
	if _, _, err := validateSKUWarehouseRule("", nil); err == nil {
		t.Fatal("empty warehouse SKU must be rejected")
	}
	if _, _, err := validateSKUWarehouseRule("SKU-1", []string{"UNKNOWN"}); err == nil {
		t.Fatal("unknown warehouse must be rejected")
	}
}

func TestApplySKUWarehouseRestrictionsFallsBackToAllowedWarehouse(t *testing.T) {
	decision := skuWarehouseRuleDecision()
	applySKUWarehouseRestrictions(&decision, map[string]map[string]bool{
		"SKU-1": {"DPS002": true},
	})
	selection, err := inventory.SelectWarehouse(decision, "east", map[string]int{"SKU-1": 1})
	if err != nil {
		t.Fatal(err)
	}
	if selection.WarehouseKey != "ARP_EAST" {
		t.Fatalf("selected warehouse = %q, want ARP_EAST", selection.WarehouseKey)
	}
	disabled := decision.Records[0].Regions[0].Warehouses[0]
	if disabled.Selectable || !disabled.ShopSKUDisabled || disabled.ReasonCode != "SHOP_SKU_WAREHOUSE_DISABLED" {
		t.Fatalf("disabled warehouse was not annotated: %#v", disabled)
	}
}

func TestApplySKUWarehouseRestrictionsLeavesDefaultUnchanged(t *testing.T) {
	decision := skuWarehouseRuleDecision()
	applySKUWarehouseRestrictions(&decision, nil)
	selection, err := inventory.SelectWarehouse(decision, "east", map[string]int{"SKU-1": 1})
	if err != nil {
		t.Fatal(err)
	}
	if selection.WarehouseKey != "DPS002" {
		t.Fatalf("default warehouse = %q, want DPS002", selection.WarehouseKey)
	}
}

func TestSKUWarehouseRestrictionsBlockPurchaseAndManualizeNoCoverage(t *testing.T) {
	order := model.Order{ParentOrderSN: "PO-1", Lines: []model.OrderLine{{ExtCode: "SKU-1", Quantity: 1}}}
	if err := validateOrderWarehouseRestrictions(order, "DPS002", nil); err != nil {
		t.Fatalf("default all-warehouse policy must allow purchase: %v", err)
	}
	if err := validateOrderWarehouseRestrictions(order, "dps002", map[string]map[string]bool{
		"SKU-1": {"DPS002": true},
	}); err == nil {
		t.Fatal("disabled warehouse must block the final purchase check")
	}

	decision := skuWarehouseRuleDecision()
	applySKUWarehouseRestrictions(&decision, map[string]map[string]bool{
		"SKU-1": {"DPS002": true, "ARP_EAST": true, "DPS004": true, "ARP_WEST": true},
	})
	classification := warehouseClassificationFromDecision(order, decision, nil)
	if classification.Status != "manual" || !contains(classification.Categories, manualReasonSKUWarehousePolicy) {
		t.Fatalf("classification = %#v, want shop SKU warehouse manual review", classification)
	}
}

func skuWarehouseRuleDecision() inventory.DecisionResponse {
	return inventory.DecisionResponse{
		Complete:          true,
		PackageResolution: inventory.PackageResolution{Complete: true},
		Records: []inventory.SKUDecision{{
			SKU: "SKU-1",
			Regions: []inventory.Region{
				{Region: "east", RecommendedWarehouseKey: "DPS002", Warehouses: []inventory.Warehouse{
					{Key: "DPS002", Name: "DPS002", Region: "east", Available: 10, Selectable: true, Recommended: true},
					{Key: "ARP_EAST", Name: "ARP East", Region: "east", Available: 10, Selectable: true},
				}},
				{Region: "west", RecommendedWarehouseKey: "DPS004", Warehouses: []inventory.Warehouse{
					{Key: "DPS004", Name: "DPS004", Region: "west", Available: 10, Selectable: true, Recommended: true},
					{Key: "ARP_WEST", Name: "ARP West", Region: "west", Available: 10, Selectable: true},
				}},
			},
		}},
	}
}

func TestNormalizeParentOrderSNs(t *testing.T) {
	items, err := normalizeParentOrderSNs([]string{" PO-1 ", "PO-1", "PO-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(items, []string{"PO-1", "PO-2"}) {
		t.Fatalf("normalized orders = %#v", items)
	}
	if _, err := normalizeParentOrderSNs(nil); err == nil {
		t.Fatal("empty lookup must be rejected")
	}
	if _, err := normalizeParentOrderSNs(make([]string, 51)); err == nil {
		t.Fatal("oversized lookup must be rejected")
	}
}

func TestTemporaryFulfillmentError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "Temu transport timeout", err: &temu.APIError{Temporary: true, Message: "timeout"}, want: true},
		{name: "Temu generic business service error", err: &temu.APIError{Code: "7000000", Message: "BUSINESS_SERVICE_ERROR"}, want: true},
		{name: "Temu rate limit", err: &temu.APIError{Code: "4000004", Message: "too frequent requests"}, want: true},
		{name: "wrapped Temu timeout", err: errors.Join(errors.New("warehouse unavailable"), &temu.APIError{Temporary: true, Message: "timeout"}), want: true},
		{name: "temporary error after business error", err: errors.Join(&temu.APIError{Code: "40001", Message: "invalid request"}, &temu.APIError{Temporary: true, Message: "timeout"}), want: true},
		{name: "request deadline", err: context.DeadlineExceeded, want: true},
		{name: "Temu business rejection", err: &temu.APIError{Code: "40001", Message: "invalid request"}, want: false},
		{name: "local validation", err: errors.New("warehouse SKU is missing"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := temporaryFulfillmentError(test.err); got != test.want {
				t.Fatalf("temporaryFulfillmentError() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestResolveMissingOMSPlatformOrderValidatesTerminalInput(t *testing.T) {
	manager := &Service{}
	tests := []struct {
		name   string
		parent string
		status string
		note   string
		want   string
	}{
		{name: "missing parent", status: "cancelled", note: "已核实", want: "parent order number is required"},
		{name: "invalid status", parent: "PO-1", status: "done", note: "已核实", want: "请选择有效的人工终态"},
		{name: "missing note", parent: "PO-1", status: "cancelled", want: "人工终态备注不能为空"},
		{name: "long note", parent: "PO-1", status: "other", note: strings.Repeat("字", 501), want: "人工终态备注不能超过 500 个字符"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := manager.ResolveMissingOMSPlatformOrder(context.Background(), test.parent, test.status, test.note)
			if err == nil || err.Error() != test.want {
				t.Fatalf("got error %v, want %q", err, test.want)
			}
		})
	}
}

func TestFulfillmentErrorStatus(t *testing.T) {
	if got := fulfillmentErrorStatus(context.DeadlineExceeded, "confirming"); got != "confirming" {
		t.Fatalf("timeout must remain retryable, got %q", got)
	}
	if got := fulfillmentErrorStatus(errors.New("invalid package"), "confirming"); got != "failed" {
		t.Fatalf("business failure must stop, got %q", got)
	}
}

func TestShipmentSubmissionRetryDue(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	if shipmentSubmissionRetryDue(model.Shipment{LastSubmissionAt: now.Add(-time.Minute)}, now) {
		t.Fatal("submission must wait for the two-minute safety window")
	}
	if !shipmentSubmissionRetryDue(model.Shipment{LastSubmissionAt: now.Add(-2 * time.Minute)}, now) {
		t.Fatal("submission must become retryable at the two-minute boundary")
	}
}

func TestShipmentConfirmationStalled(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	base := model.Shipment{
		Status:         "label_ready",
		PackageSNList:  []string{"PK-1"},
		TrackingNumber: "TRACK-1",
		UpdatedAt:      now.Add(-shipmentConfirmationStallDelay - time.Second),
	}
	if !shipmentConfirmationStalled(base, now) {
		t.Fatal("complete untouched label older than five minutes must be marked stalled")
	}
	recent := base
	recent.UpdatedAt = now.Add(-shipmentConfirmationStallDelay)
	if shipmentConfirmationStalled(recent, now) {
		t.Fatal("five-minute boundary must remain in normal processing")
	}
	attempted := base
	attempted.ConfirmationAttempts = 1
	if shipmentConfirmationStalled(attempted, now) {
		t.Fatal("an attempted confirmation must use normal retry/failure handling")
	}
	missingTracking := base
	missingTracking.TrackingNumber = ""
	if shipmentConfirmationStalled(missingTracking, now) {
		t.Fatal("incomplete label evidence must not be repaired as a confirmation orphan")
	}
	shipped := base
	shipped.Status = "shipped"
	if shipmentConfirmationStalled(shipped, now) {
		t.Fatal("shipped records cannot be stalled confirmations")
	}
}

func TestDeliveryAddressUnsupported(t *testing.T) {
	if !deliveryAddressUnsupported("Order failed: the delivery address is not supported for our service") {
		t.Fatal("documented address rejection must be recognized")
	}
	if !deliveryAddressUnsupported("Please check whether the package delivery information is correct. Error reason: This ZIP code is not supported by GOFO delivery service.") {
		t.Fatal("carrier-specific ZIP rejection must be recognized")
	}
	if !deliveryAddressUnsupported("This postal code is unsupported by the carrier") {
		t.Fatal("carrier-specific postal rejection must be recognized")
	}
	if deliveryAddressUnsupported("address field is missing") {
		t.Fatal("unrelated address validation errors must not trigger carrier fallback")
	}
	if deliveryAddressUnsupported("The ZIP code is invalid") {
		t.Fatal("an invalid address must not be treated as a carrier coverage failure")
	}
}

func TestTemuShipmentRequestAlreadyExists(t *testing.T) {
	if !temuShipmentRequestAlreadyExists(&temu.APIError{Code: "120012013", Message: "already requested"}) {
		t.Fatal("documented existing integrated-logistics request code must be recognized")
	}
	if temuShipmentRequestAlreadyExists(&temu.APIError{Code: "2", Message: "address unsupported"}) {
		t.Fatal("unrelated Temu API errors must not be treated as an existing request")
	}
}

func TestAutomaticCarrierFallbackAllowed(t *testing.T) {
	base := model.Shipment{
		Status:             "label_failed",
		ErrorMessage:       "Order failed: the delivery address is not supported for our service. Address category: {Remote Area}.",
		PackageSNList:      []string{"PK-failed"},
		SubmissionAttempts: 1,
	}
	if automaticCarrierFallbackAllowed(base) {
		t.Fatal("an existing failed package must block automatic carrier fallback")
	}
	withoutPackage := base
	withoutPackage.PackageSNList = nil
	if !automaticCarrierFallbackAllowed(withoutPackage) {
		t.Fatal("an address rejection without package evidence must allow carrier fallback")
	}
	withTracking := base
	withTracking.TrackingNumber = "TRACK"
	if automaticCarrierFallbackAllowed(withTracking) {
		t.Fatal("tracking evidence must block carrier fallback")
	}
	exhausted := base
	exhausted.SubmissionAttempts = maxShipmentSubmissionAttempts
	if automaticCarrierFallbackAllowed(exhausted) {
		t.Fatal("the submission attempt limit must block carrier fallback")
	}
}

func TestCarrierCoverageNeedsManualWhenTemuCreatedFailedPackage(t *testing.T) {
	base := model.Shipment{
		Status:              "label_failed",
		ErrorMessage:        "This ZIP code is not supported by GOFO delivery service.",
		PackageSNList:       []string{"PK-failed"},
		ShippingCompanyName: "GOFO",
	}
	if !carrierCoverageNeedsManual(base) {
		t.Fatal("a carrier coverage failure with a Temu package must move to manual review")
	}
	base.TrackingNumber = "TRACK-1"
	if carrierCoverageNeedsManual(base) {
		t.Fatal("tracking evidence must block manual rerouting")
	}
}

func TestValidateFailedShipmentRecovery(t *testing.T) {
	confirmedAt := time.Now()
	tests := []struct {
		name     string
		shipment model.Shipment
		wantErr  bool
	}{
		{name: "failed without shipping evidence", shipment: model.Shipment{Status: "label_failed"}},
		{name: "failed integrated package exists", shipment: model.Shipment{Status: "label_failed", PackageSNList: []string{"PK-old"}}, wantErr: true},
		{name: "wrong status", shipment: model.Shipment{Status: "label_pending"}, wantErr: true},
		{name: "tracking already exists", shipment: model.Shipment{Status: "label_failed", TrackingNumber: "TRACK"}, wantErr: true},
		{name: "already confirmed", shipment: model.Shipment{Status: "label_failed", ConfirmedAt: &confirmedAt}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateFailedShipmentRecovery(test.shipment); (err != nil) != test.wantErr {
				t.Fatalf("validateFailedShipmentRecovery() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestRecoveryExcludedCarrierCodesIncludesCurrentCarrier(t *testing.T) {
	shipment := model.Shipment{
		FailedCarrierCodes:  []string{"SPEEDX"},
		ShippingCompanyName: "GOFO",
		ShipLogisticsType:   "Standard",
	}
	if got := recoveryExcludedCarrierCodes(shipment); !reflect.DeepEqual(got, []string{"SPEEDX", "GOFO"}) {
		t.Fatalf("recoveryExcludedCarrierCodes() = %v", got)
	}
}

func candidate(id int64, warehouseKey, carrier, amount string) autoChannelCandidate {
	item := autoChannelCandidate{
		warehouseKey: warehouseKey, temuWarehouseID: "temu-" + warehouseKey, amount: price(amount),
		channel: temu.ShippingChannel{ChannelID: id, ShipCompanyID: id + 100, ShippingCompanyName: carrier, EstimatedAmount: amount, EstimatedCurrencyCode: "USD"},
	}
	item.priority = configuredCarrierPriority(defaultCarrierPolicies(warehouseKey), carrierCode(item.channel))
	return item
}

func TestSelectAutomaticChannelPrefersGOFOWithinFiftyCents(t *testing.T) {
	selected, _, err := selectAutomaticChannel([]autoChannelCandidate{
		candidate(1, "DPS002", "SpeedX", "$10.00"),
		candidate(2, "ARP_EAST", "SwiftX", "$10.20"),
		candidate(3, "ARP_EAST", "GOFO", "$10.50"),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if carrierCode(selected.channel) != "GOFO" {
		t.Fatalf("expected GOFO, got %#v", selected.channel)
	}
}

func TestSelectAutomaticChannelUsesLowestWhenGOFOExceedsFiftyCents(t *testing.T) {
	selected, _, err := selectAutomaticChannel([]autoChannelCandidate{
		candidate(1, "DPS002", "SpeedX", "$10.00"),
		candidate(2, "ARP_EAST", "GOFO", "$10.51"),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.channel.ChannelID != 1 {
		t.Fatalf("expected lowest price SpeedX, got %#v", selected)
	}
}

func TestSelectAutomaticChannelPrefersSwiftXWhenGOFOOutsideRange(t *testing.T) {
	selected, _, err := selectAutomaticChannel([]autoChannelCandidate{
		candidate(1, "DPS002", "SpeedX", "$10.00"),
		candidate(2, "ARP_EAST", "SwiftX", "$10.35"),
		candidate(3, "ARP_EAST", "GOFO", "$10.75"),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if carrierCode(selected.channel) != "SWIFTX" {
		t.Fatalf("expected SwiftX, got %#v", selected.channel)
	}
}

func TestSelectAutomaticChannelPriceBeatsDPSClearing(t *testing.T) {
	selected, _, err := selectAutomaticChannel([]autoChannelCandidate{
		candidate(1, "DPS002", "UPS", "$9.50"),
		candidate(2, "ARP_EAST", "UPS", "$9.00"),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.warehouseKey != "ARP_EAST" {
		t.Fatalf("lower-price ARP must win, got %#v", selected)
	}
}

func TestSelectAutomaticChannelUsesDPSOnlyOnEqualPrice(t *testing.T) {
	selected, _, err := selectAutomaticChannel([]autoChannelCandidate{
		candidate(1, "ARP_EAST", "UPS", "$9.00"),
		candidate(2, "DPS002", "UPS", "$9.00"),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.warehouseKey != "DPS002" {
		t.Fatalf("equal-price selection must prefer DPS, got %#v", selected)
	}
}

func TestSelectAutomaticChannelUsesWarehouseSpecificPriority(t *testing.T) {
	dps := candidate(1, "DPS002", "GOFO", "$10.00")
	dps.priority = 3
	arp := candidate(2, "ARP_EAST", "SpeedX", "$10.25")
	arp.priority = 1
	selected, _, err := selectAutomaticChannel([]autoChannelCandidate{dps, arp}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.warehouseKey != "ARP_EAST" || carrierCode(selected.channel) != "SPEEDX" {
		t.Fatalf("warehouse-specific priority was not applied: %#v", selected)
	}
}

func TestBuildLabelPurchaseChoiceStoresTopThreeLowestPricesAndFinalSelection(t *testing.T) {
	candidates := []autoChannelCandidate{
		candidate(1, "ARP_EAST", "UPS", "$9.00"),
		candidate(2, "DPS002", "SpeedX", "$9.00"),
		candidate(3, "DPS004", "USPS", "$9.20"),
		candidate(4, "ARP_WEST", "GOFO", "$9.40"),
	}
	selected, reason, err := selectAutomaticChannel(candidates, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.channel.ChannelID != 4 {
		t.Fatalf("expected priority rule to select GOFO outside the price Top 3, got %#v", selected)
	}

	choice := buildLabelPurchaseChoice(candidates, selected, reason, false)
	if choice.SelectionSource != "automatic" || choice.Selected.ChannelID != 4 || choice.Selected.PriceRank != 0 {
		t.Fatalf("unexpected selected choice: %#v", choice)
	}
	if len(choice.TopCandidates) != 3 {
		t.Fatalf("TopCandidates length = %d, want 3", len(choice.TopCandidates))
	}
	wantIDs := []int64{2, 1, 3}
	for index, candidate := range choice.TopCandidates {
		if candidate.PriceRank != index+1 || candidate.ChannelID != wantIDs[index] {
			t.Fatalf("candidate %d = %#v, want channel %d at rank %d", index, candidate, wantIDs[index], index+1)
		}
	}
}

func TestBuildLabelPurchaseChoiceMarksManualSelectionRank(t *testing.T) {
	candidates := []autoChannelCandidate{
		candidate(1, "ARP_EAST", "UPS", "$9.00"),
		candidate(2, "DPS002", "SpeedX", "$9.10"),
	}
	choice := buildLabelPurchaseChoice(candidates, candidates[1], "manual", true)
	if choice.SelectionSource != "manual" || choice.Selected.PriceRank != 2 {
		t.Fatalf("unexpected manual choice: %#v", choice)
	}
}

func TestFilterChannelsByCarrierPolicyDisablesWarehouseCarrier(t *testing.T) {
	policies := defaultCarrierPolicies("DPS002")
	policies[0].Enabled = false
	allowed, rejected := filterChannelsByCarrierPolicy([]temu.ShippingChannel{
		{ChannelID: 1, ShippingCompanyName: "GOFO"},
		{ChannelID: 2, ShippingCompanyName: "UPS"},
	}, "DPS002", policies)
	if len(allowed) != 1 || allowed[0].ChannelID != 2 {
		t.Fatalf("unexpected allowed channels: %#v", allowed)
	}
	if len(rejected) != 1 || rejected[0].ChannelID != 1 || !strings.Contains(rejected[0].UnavailableReason, "DPS002") {
		t.Fatalf("unexpected rejected channels: %#v", rejected)
	}
}

func TestValidateCarrierPoliciesRequiresCompleteUniqueOrder(t *testing.T) {
	policies := defaultCarrierPolicies("DPS002")
	policies[1].Priority = policies[0].Priority
	if _, err := validateCarrierPolicies("DPS002", policies); err == nil {
		t.Fatal("duplicate priorities must fail")
	}
}

func TestFilterAutomaticChannelsEnforcesWhitelistAndNoSignature(t *testing.T) {
	allowed, rejected := filterAutomaticChannels([]temu.ShippingChannel{
		{ChannelID: 1, ShippingCompanyName: "UPS", EstimatedAmount: "$8.00", EstimatedCurrencyCode: "USD"},
		{ChannelID: 2, ShippingCompanyName: "UniUni", EstimatedAmount: "$7.00", EstimatedCurrencyCode: "USD"},
		{ChannelID: 3, ShippingCompanyName: "Other", EstimatedAmount: "$6.00", EstimatedCurrencyCode: "USD"},
		{ChannelID: 4, ShippingCompanyName: "GOFO", EstimatedAmount: "$8.10", EstimatedCurrencyCode: "USD", InfoNeeded: []string{"signServiceId"}},
	})
	if len(allowed) != 1 || allowed[0].ChannelID != 1 {
		t.Fatalf("unexpected allowed channels: %#v", allowed)
	}
	if len(rejected) != 3 {
		t.Fatalf("unexpected rejected channels: %#v", rejected)
	}
}

func TestShippingRequestsForceNoSignature(t *testing.T) {
	order := model.Order{ParentOrderSN: "PO-1"}
	spec := model.PackageSpec{Weight: "1", WeightUnit: "lb", Length: "2", Width: "3", Height: "4", DimensionUnit: "in"}
	request := shippingServicesRequest(order, "WH-1", spec)
	if signature, ok := request["signatureOnDelivery"].(bool); !ok || signature {
		t.Fatalf("signatureOnDelivery must be false: %#v", request)
	}
	quote := model.Quote{ChannelID: 10, ShipCompanyID: 20, TemuWarehouseID: "WH-1"}
	channel := temu.ShippingChannel{ChannelID: 10, ShipCompanyID: 20, ShippingCompanyName: "GOFO", InfoNeeded: []string{"signServiceId"}}
	if _, err := shipmentCreateRequest(order, quote, spec, channel); err == nil {
		t.Fatal("signature-required channel must never be submitted")
	}
}
func TestPackageSpecFromResolutionConvertsMetricToUSImperial(t *testing.T) {
	got, err := packageSpecFromResolution(inventory.PackageResolution{
		Complete: true,
		Package: &inventory.PackageSpec{
			WarehouseSKU:  "PH+H-12Pcs-Black-42cm",
			Weight:        1.27,
			WeightUnit:    "kg",
			Length:        41,
			Width:         32.5,
			Height:        7,
			DimensionUnit: "cm",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Weight != "2" || got.ExtendWeight != "13" || got.WeightUnit != "lb" || got.ExtendWeightUnit != "oz" {
		t.Fatalf("unexpected weight: %#v", got)
	}
	if got.Length != "16.15" || got.Width != "12.8" || got.Height != "2.76" || got.DimensionUnit != "in" {
		t.Fatalf("unexpected dimensions: %#v", got)
	}
}

func TestPackageSpecFromResolutionRejectsIncompleteMatch(t *testing.T) {
	_, err := packageSpecFromResolution(inventory.PackageResolution{Error: "仓库SKU规格缺失"})
	if err == nil {
		t.Fatal("incomplete warehouse SKU resolution must fail")
	}
}

func TestPackageSNsFromOrderDetailDeduplicatesPackages(t *testing.T) {
	result := temu.OrderPageItem{Lines: []temu.OrderLine{
		{PackageSNInfo: []temu.PackageInfo{{PackageSN: " PK-1 "}, {PackageSN: ""}}},
		{PackageSNInfo: []temu.PackageInfo{{PackageSN: "PK-1"}, {PackageSN: "PK-2"}}},
	}}
	got := packageSNsFromOrderDetail(result)
	if len(got) != 2 || got[0] != "PK-1" || got[1] != "PK-2" {
		t.Fatalf("unexpected package SNs: %#v", got)
	}
}

func TestShipmentCreateRequestIncludesRequiredPickupSlot(t *testing.T) {
	order := model.Order{ParentOrderSN: "PO-1", Lines: []model.OrderLine{{OrderSN: "O-1", Quantity: 1, GoodsID: 2, SKUID: 3}}}
	quote := model.Quote{ChannelID: 10, ShipCompanyID: 20, TemuWarehouseID: "WH-1"}
	spec := model.PackageSpec{Weight: "1", WeightUnit: "lb", Length: "2", Width: "3", Height: "4", DimensionUnit: "in"}
	channel := temu.ShippingChannel{ChannelID: 10, ShipCompanyID: 20, ShippingCompanyName: "SpeedX", InfoNeeded: []string{"pickupStartTime", "pickupEndTime"}, PickupSlots: []temu.PickupTimeSlot{{Start: 4102444800, End: 4102466400}}}
	request, err := shipmentCreateRequest(order, quote, spec, channel)
	if err != nil {
		t.Fatal(err)
	}
	if limit, ok := request["shipLaterLimitTime"].(int); !ok || limit != 24 {
		t.Fatalf("shipLaterLimitTime must be numeric 24: %#v", request["shipLaterLimitTime"])
	}
	packs := request["sendRequestList"].([]any)
	pack := packs[0].(map[string]any)
	if pack["pickupStartTime"] != int64(4102444800) || pack["pickupEndTime"] != int64(4102466400) {
		t.Fatalf("pickup slot missing from request: %#v", pack)
	}
	if _, exists := pack["autoConfirmAfterPickup"]; exists {
		t.Fatal("autoConfirmAfterPickup is not a shipment create send-item field")
	}
}

func TestShipmentCreateRequestRejectsMissingRequiredPickupSlot(t *testing.T) {
	order := model.Order{ParentOrderSN: "PO-1"}
	quote := model.Quote{ChannelID: 10, ShipCompanyID: 20, TemuWarehouseID: "WH-1"}
	spec := model.PackageSpec{Weight: "1", WeightUnit: "lb", Length: "2", Width: "3", Height: "4", DimensionUnit: "in"}
	channel := temu.ShippingChannel{ChannelID: 10, ShipCompanyID: 20, ShippingCompanyName: "SpeedX", InfoNeeded: []string{"pickupStartTime", "pickupEndTime"}}
	if _, err := shipmentCreateRequest(order, quote, spec, channel); err == nil {
		t.Fatal("required pickup slot must block shipment submission")
	}
}

func TestQuoteWarehouseKeysAutoIncludesAllBusinessWarehousesInOrder(t *testing.T) {
	got, err := quoteWarehouseKeys("auto", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DPS002", "ARP_EAST", "DPS004", "ARP_WEST"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWarehouseRegionMapsAutomaticCandidates(t *testing.T) {
	cases := map[string]string{"DPS002": "east", "ARP_EAST": "east", "DPS004": "west", "ARP_WEST": "west"}
	for warehouse, want := range cases {
		if got := warehouseRegion(warehouse); got != want {
			t.Fatalf("warehouseRegion(%q)=%q, want %q", warehouse, got, want)
		}
	}
}
