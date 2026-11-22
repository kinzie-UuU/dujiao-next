package application

import (
	"github.com/dujiao-next/internal/constants"
	"testing"

	"github.com/dujiao-next/internal/shared/jsonmap"
)

// ── Unit tests for pure functions ──

func TestBuildUpstreamRefundRecords_SortsByCreatedAtAscAndRenumbersID(t *testing.T) {
	records := []jsonmap.JSON{
		{
			"id":         99,
			"type":       "wallet",
			"amount":     "20.00",
			"created_at": "2026-04-12T10:00:00Z",
		},
		{
			"id":         100,
			"type":       "wallet",
			"amount":     "10.00",
			"created_at": "2026-04-12T09:00:00Z",
		},
	}

	got := buildUpstreamRefundRecords(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d", len(got))
	}
	if amount, _ := got[0]["amount"].(string); amount != "10.00" {
		t.Fatalf("expected first amount 10.00, got %#v", got[0]["amount"])
	}
	if amount, _ := got[1]["amount"].(string); amount != "20.00" {
		t.Fatalf("expected second amount 20.00, got %#v", got[1]["amount"])
	}
	if id, ok := got[0]["id"].(int); !ok || id != 1 {
		t.Fatalf("expected first id 1, got %#v", got[0]["id"])
	}
	if id, ok := got[1]["id"].(int); !ok || id != 2 {
		t.Fatalf("expected second id 2, got %#v", got[1]["id"])
	}
}

func TestIsUpstreamTransitionAllowed(t *testing.T) {
	for _, tc := range []struct {
		current, upstream string
		want              bool
	}{
		{constants.ProcurementStatusAccepted, "delivered", true},
		{constants.ProcurementStatusPartiallyRefunded, "delivered", true},
		{constants.ProcurementStatusFulfilled, "delivered", false},
		{constants.ProcurementStatusRefunded, "delivered", false},
		{constants.ProcurementStatusCanceled, "delivered", false},
		{constants.ProcurementStatusFulfilled, "canceled", false},
		{constants.ProcurementStatusAccepted, "canceled", true},
		{constants.ProcurementStatusAccepted, "partially_refunded", true},
		{constants.ProcurementStatusFulfilled, "refunded", true},
	} {
		if got := isUpstreamTransitionAllowed(tc.current, tc.upstream); got != tc.want {
			t.Errorf("(%s -> %s) = %v, want %v", tc.current, tc.upstream, got, tc.want)
		}
	}
}
