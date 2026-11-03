package upstream

import (
	"context"
	"fmt"
)

// LookupOrder uses the supplier's native order reference without converting or
// truncating serial numbers. Existing Dujiao Next connections use numeric IDs.
func LookupOrder(ctx context.Context, adapter Adapter, id uint, number string) (*UpstreamOrderDetail, error) {
	if numbered, ok := adapter.(interface {
		GetOrderByNumber(context.Context, string) (*UpstreamOrderDetail, error)
	}); ok {
		if number == "" {
			return nil, fmt.Errorf("missing upstream order number")
		}
		return numbered.GetOrderByNumber(ctx, number)
	}
	if id == 0 {
		return nil, fmt.Errorf("missing upstream order ID")
	}
	return adapter.GetOrder(ctx, id)
}
