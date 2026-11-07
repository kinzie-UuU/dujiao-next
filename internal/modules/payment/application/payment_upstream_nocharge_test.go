package application

import (
	"context"
	"testing"

	"github.com/dujiao-next/internal/constants"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

// The gateway, queue and procurement port are in-process fakes. This test never
// sends a payment or a supplier purchase, and uses an isolated in-memory DB.
func TestUpstreamPaymentDispatchNoCharge(t *testing.T) {
	for _, tc := range []struct {
		name, amount, currency string
		valid                  bool
	}{
		{"paid-and-replayed", "81", "CNY", true},
		{"amount-mismatch", "80", "CNY", false},
		{"currency-mismatch", "81", "USD", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			channel := createUnderpaidChannel(t, db, svc, "Mock payment only", constants.PaymentChannelTypeWechat)
			order := createUnderpaidOrder(t, db, "MOCK-UPSTREAM", 0, 81)
			if err := db.Create(&orderdomain.OrderItem{OrderID: order.ID, ProductID: 1, SKUID: 1, Quantity: 1, TitleJSON: jsonmap.JSON{"zh-CN": "Figma mock"}, FulfillmentType: constants.FulfillmentTypeUpstream}).Error; err != nil {
				t.Fatal(err)
			}
			q := &receiptQueue{}
			procurement := &receiptProcurement{}
			svc.queue = q
			svc.SetProcurementService(procurement)
			created, err := svc.CreatePayment(CreatePaymentInput{OrderID: order.ID, ChannelID: channel.ID, Context: context.Background()})
			if err != nil {
				t.Fatal(err)
			}
			if procurement.calls.Load() != 0 || q.fulfilled.Load() != 0 {
				t.Fatal("unpaid order dispatched fulfillment")
			}
			input := PaymentCallbackInput{PaymentID: created.Payment.ID, OrderNo: order.OrderNo, ChannelID: channel.ID, Status: constants.PaymentStatusSuccess, Amount: money.FromDecimal(decimal.RequireFromString(tc.amount)), Currency: tc.currency, ProviderRef: "MOCK-RECEIPT"}
			_, err = svc.HandleCallback(input)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid callback accepted")
				}
				if procurement.calls.Load() != 0 || q.fulfilled.Load() != 0 {
					t.Fatal("invalid callback dispatched fulfillment")
				}
				if err := db.First(order, order.ID).Error; err != nil {
					t.Fatal(err)
				}
				if order.Status != constants.OrderStatusPendingPayment {
					t.Fatalf("invalid callback changed order to %s", order.Status)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if _, err := svc.HandleCallback(input); err != nil {
					t.Fatal(err)
				}
			}
			if procurement.calls.Load() != 1 {
				t.Fatalf("procurement dispatch count=%d", procurement.calls.Load())
			}
			if q.fulfilled.Load() != 0 {
				t.Fatal("upstream order incorrectly dispatched local-card fulfillment")
			}
			if err := db.First(order, order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if order.Status != constants.OrderStatusPaid || order.PaidAt == nil {
				t.Fatalf("order not paid: %s", order.Status)
			}
		})
	}
}
