package consumer

import (
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	"strings"
	"testing"
)

func TestCustomerDeliveryOmitsUpstreamReference(t *testing.T) {
	f := &fulfillmentdomain.Fulfillment{Type: "manual", Payload: "credential\n上游订单：PRIVATE"}
	for _, o := range []*orderdomain.Order{{Fulfillment: f}, {Children: []orderdomain.Order{{OrderNo: "DJLOCAL", Fulfillment: f}}}} {
		got := buildOrderFulfillmentEmailPayload(o)
		if strings.Contains(got, "PRIVATE") || !strings.Contains(got, "credential") {
			t.Fatalf("customer output=%q", got)
		}
	}
	if !strings.Contains(f.Payload, "PRIVATE") {
		t.Fatal("stored receipt mutated")
	}
}
