package store

import "testing"

func TestBulkFulfillmentBatchStatusDoesNotStopOnFailedOrder(t *testing.T) {
	if got := bulkFulfillmentBatchStatus(3); got != "running" {
		t.Fatalf("batch status = %q, want running", got)
	}
	if got := bulkFulfillmentBatchStatus(0); got != "completed" {
		t.Fatalf("batch status = %q, want completed", got)
	}
}
