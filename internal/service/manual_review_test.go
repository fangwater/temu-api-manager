package service

import (
	"testing"

	"temu-api-manager/internal/model"
)

func TestValidManualReviewOutcome(t *testing.T) {
	for _, outcome := range []string{"manually_fulfilled", "cancelled", "not_required", "other"} {
		if !validManualReviewOutcome(outcome) {
			t.Errorf("valid outcome %q was rejected", outcome)
		}
	}
	for _, outcome := range []string{"", "approved", "unknown"} {
		if validManualReviewOutcome(outcome) {
			t.Errorf("invalid outcome %q was accepted", outcome)
		}
	}
}

func TestCompletedManualReviewBlocksFurtherFulfillment(t *testing.T) {
	order := model.Order{ManualReview: &model.ManualReview{Status: "resolved", Outcome: "manually_fulfilled"}}
	if got := manualOrderReason(order); got != "order manual fulfillment is already completed" {
		t.Fatalf("manualOrderReason() = %q", got)
	}
}
