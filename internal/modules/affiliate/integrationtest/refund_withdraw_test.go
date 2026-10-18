package integrationtest

import (
	"errors"
	"testing"

	"github.com/dujiao-next/internal/constants"
	affiliateapp "github.com/dujiao-next/internal/modules/affiliate/application"
	affiliatecontract "github.com/dujiao-next/internal/modules/affiliate/contract"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

func TestRefundDuringPendingWithdraw(t *testing.T) {
	for _, refund := range []string{"0", "25", "100"} {
		t.Run(refund, func(t *testing.T) {
			svc, db, store := setupAffiliateServiceTest(t)
			if err := db.AutoMigrate(&affiliatedomain.Commission{}, &affiliatedomain.WithdrawRequest{}); err != nil {
				t.Fatal(err)
			}
			user := createAffiliateTestUser(t, db, "refund@example.com")
			profile := createAffiliateTestProfile(t, db, user.ID, "REFUND01", constants.AffiliateProfileStatusActive)
			amount := func(s string) money.Amount { return money.FromDecimal(decimal.RequireFromString(s)) }
			row := affiliatedomain.Commission{AffiliateProfileID: profile.ID, OrderID: 1, CommissionType: "order", BaseAmount: amount("100"), CommissionAmount: amount("20"), RatePercent: amount("20"), Status: constants.AffiliateCommissionStatusAvailable}
			if err := store.CreateCommission(&row); err != nil {
				t.Fatal(err)
			}
			// A partial withdrawal splits the commission into bound and unbound rows.
			req, err := svc.ApplyWithdraw(user.ID, affiliateapp.WithdrawApplyInput{Amount: decimal.NewFromInt(10), Channel: "manual", Account: "test"})
			if err != nil {
				t.Fatal(err)
			}
			order := &orderdomain.Order{ID: 1, TotalAmount: amount("100")}
			if err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
				return svc.HandleOrderRefunded(tx, order, decimal.RequireFromString(refund), decimal.Zero, "refund")
			}); err != nil {
				t.Fatal(err)
			}
			_, err = svc.ReviewWithdraw(1, req.ID, constants.AffiliateWithdrawActionPay, "")
			if refund == "0" {
				if err != nil {
					t.Fatalf("valid withdrawal should pay: %v", err)
				}
				return
			}
			if !errors.Is(err, affiliateapp.ErrWithdrawInsufficient) {
				t.Fatalf("stale withdrawal should be blocked, got %v", err)
			}
			current, err := store.GetWithdrawByID(req.ID)
			if err != nil || current.Status != constants.AffiliateWithdrawStatusPendingReview || current.ProcessedAt != nil {
				t.Fatalf("failed payment mutated request: %+v, %v", current, err)
			}
			if _, err := svc.ReviewWithdraw(1, req.ID, constants.AffiliateWithdrawActionReject, "order refunded"); err != nil {
				t.Fatal(err)
			}
			rows, err := store.ListCommissionsByOrder(1, nil)
			if err != nil {
				t.Fatal(err)
			}
			total := decimal.Zero
			for _, c := range rows {
				if c.WithdrawRequestID != nil {
					t.Fatal("rejection left commission bound")
				}
				if refund == "100" && c.Status != constants.AffiliateCommissionStatusRejected {
					t.Fatal("refunded commission revived")
				}
				total = total.Add(c.CommissionAmount.Decimal)
			}
			want := decimal.NewFromInt(20).Mul(decimal.NewFromInt(100).Sub(decimal.RequireFromString(refund))).Div(decimal.NewFromInt(100))
			if !total.Equal(want) {
				t.Fatalf("remaining commission = %s, want %s", total, want)
			}
			_, err = svc.ApplyWithdraw(user.ID, affiliateapp.WithdrawApplyInput{Amount: decimal.NewFromInt(20), Channel: "manual", Account: "test"})
			if !errors.Is(err, affiliateapp.ErrWithdrawInsufficient) {
				t.Fatalf("refunded amount can be reapplied: %v", err)
			}
			if want.IsPositive() {
				next, err := svc.ApplyWithdraw(user.ID, affiliateapp.WithdrawApplyInput{Amount: want, Channel: "manual", Account: "test"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.ReviewWithdraw(1, next.ID, constants.AffiliateWithdrawActionPay, ""); err != nil {
					t.Fatalf("remaining valid commission cannot pay: %v", err)
				}
			}
		})
	}
}
