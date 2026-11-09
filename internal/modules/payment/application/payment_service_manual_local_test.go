package application

import (
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

func TestManualLocalQRCreateReusesPendingAndPreservesAmounts(t *testing.T) {
	for _, kind := range []string{"alipay", "wechat"} {
		for _, balance := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/online", true: "/mixed"}[balance], func(t *testing.T) {
				svc, db := setupPaymentServiceWalletTest(t)
				svc.paymentProviderRegistry = nil
				m := func(v string) money.Amount { return money.FromDecimal(decimal.RequireFromString(v)) }
				user := userdomain.User{Email: "manual@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
				if err := db.Create(&user).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&walletdomain.Account{UserID: user.ID, Balance: m("30")}).Error; err != nil {
					t.Fatal(err)
				}
				ch := paymentdomain.PaymentChannel{ProviderType: manualQRProviderType, ChannelType: kind, InteractionMode: "qr", IsActive: true, FeeRate: m("2")}
				if err := db.Create(&ch).Error; err != nil {
					t.Fatal(err)
				}
				expiry := time.Now().Add(time.Hour)
				order := orderdomain.Order{OrderNo: "DJMANUALLOCAL", UserID: user.ID, Currency: "CNY", Status: constants.OrderStatusPendingPayment, TotalAmount: m("100"), OriginalAmount: m("100"), ExpiresAt: &expiry}
				if err := db.Create(&order).Error; err != nil {
					t.Fatal(err)
				}
				input := CreatePaymentInput{OrderID: order.ID, ChannelID: ch.ID, UseBalance: balance}
				result, err := svc.CreatePayment(input)
				if err != nil {
					t.Fatal(err)
				}
				expected, online, wallet := "102", "100", "0"
				if balance {
					expected, online, wallet = "71.4", "70", "30"
				}
				p := result.Payment
				if p == nil || p.Status != constants.PaymentStatusPending || p.PaidAt != nil || result.OrderPaid || p.Currency != "CNY" || !p.Amount.Decimal.Equal(decimal.RequireFromString(expected)) || p.QRCode != "manual://order/DJMANUALLOCAL" {
					t.Fatalf("incorrect pending intent: %+v", result)
				}
				if !result.OnlinePayAmount.Decimal.Equal(decimal.RequireFromString(online)) || !result.WalletPaidAmount.Decimal.Equal(decimal.RequireFromString(wallet)) {
					t.Fatal("incorrect principal/balance split")
				}
				again, err := svc.CreatePayment(input)
				if err != nil {
					t.Fatal(err)
				}
				if again.Payment.ID != p.ID {
					t.Fatal("retry created another payment")
				}
				var count int64
				if err := db.Model(&paymentdomain.Payment{}).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("payment count=%d err=%v", count, err)
				}
				if err := db.First(&order, order.ID).Error; err != nil {
					t.Fatal(err)
				}
				if order.Status != constants.OrderStatusPendingPayment || order.PaidAt != nil {
					t.Fatal("creation settled order")
				}
				exportManualLocalFixture(t, db, kind+map[bool]string{false: "-online", true: "-mixed"}[balance])
				if _, err := svc.ConfirmManualPayment(p.ID); err == nil {
					t.Fatal("confirmation without receipt evidence accepted")
				}
				if _, err := svc.ConfirmManualPayment(p.ID, receiptEvidence(t, db, p.ID)); err != nil {
					t.Fatal(err)
				}
				if err := db.First(&order, order.ID).Error; err != nil || order.Status != constants.OrderStatusPaid {
					t.Fatalf("verified receipt did not settle: %v %s", err, order.Status)
				}
			})
		}
	}
}

func TestManualLocalQRRechargeCreationBindsExactPayment(t *testing.T) {
	for _, kind := range []string{"alipay", "wechat"} {
		t.Run(kind, func(t *testing.T) {
			svc, db := setupPaymentServiceWalletTest(t)
			svc.userRepo = userstore.New(db)
			svc.paymentProviderRegistry = nil
			user := userdomain.User{Email: "recharge@example.com", PasswordHash: "test", Status: constants.UserStatusActive}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			ch := paymentdomain.PaymentChannel{ProviderType: manualQRProviderType, ChannelType: kind, InteractionMode: "qr", IsActive: true, FixedFee: money.FromDecimal(decimal.NewFromInt(2))}
			if err := db.Create(&ch).Error; err != nil {
				t.Fatal(err)
			}
			result, err := svc.CreateWalletRechargePayment(CreateWalletRechargePaymentInput{UserID: user.ID, ChannelID: ch.ID, Amount: money.FromDecimal(decimal.NewFromInt(100)), Currency: "CNY"})
			if err != nil {
				t.Fatal(err)
			}
			p, r := result.Payment, result.Recharge
			if p.Status != constants.PaymentStatusPending || p.OrderID != 0 || p.PaidAt != nil || p.ExpiredAt == nil || r.Status != constants.WalletRechargeStatusPending || r.PaymentID != p.ID || !p.Amount.Decimal.Equal(decimal.NewFromInt(102)) || p.QRCode != "manual://recharge/"+r.RechargeNo {
				t.Fatalf("incorrect recharge binding: %+v %+v", p, r)
			}
			var accountCount int64
			db.Model(&walletdomain.Account{}).Count(&accountCount)
			if accountCount != 0 {
				t.Fatal("creation credited wallet")
			}
			exportManualLocalFixture(t, db, kind+"-recharge")
			// A virtual order must match the stored recharge, not just order_id=0.
			for _, virtual := range []orderdomain.Order{{OrderNo: r.RechargeNo, UserID: user.ID + 1}, {OrderNo: "WROTHER", UserID: user.ID}} {
				copy := *p
				copy.Status = constants.PaymentStatusInitiated
				if err := svc.applyProviderPayment(CreatePaymentInput{ReturnBizType: "recharge"}, &virtual, &ch, &copy); err == nil {
					t.Fatal("unrelated recharge accepted")
				}
			}
			if _, err := svc.ConfirmManualPayment(p.ID); err == nil {
				t.Fatal("recharge confirmed without evidence")
			}
			proof := receiptEvidence(t, db, p.ID)
			for i := 0; i < 2; i++ {
				if _, err := svc.ConfirmManualPayment(p.ID, proof); err != nil {
					t.Fatal(err)
				}
			}
			var stored walletdomain.RechargeOrder
			if err := db.First(&stored, r.ID).Error; err != nil || stored.Status != constants.WalletRechargeStatusSuccess {
				t.Fatalf("recharge not settled: %v", err)
			}
			var account walletdomain.Account
			if err := db.Where("user_id = ?", user.ID).First(&account).Error; err != nil || !account.Balance.Decimal.Equal(decimal.NewFromInt(100)) {
				t.Fatalf("wallet credited incorrectly: %v %s", err, account.Balance.String())
			}
		})
	}
}

func TestManualLocalQRRejectsInvalidIntent(t *testing.T) {
	svc := &PaymentService{}
	ch := &paymentdomain.PaymentChannel{ID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", InteractionMode: "qr", IsActive: true}
	if err := svc.ValidateChannel(ch); err != nil {
		t.Fatal(err)
	}
	ch.InteractionMode = "redirect"
	if err := svc.ValidateChannel(ch); err == nil {
		t.Fatal("redirect manual channel accepted")
	}
	ch.InteractionMode = "qr"
	if err := svc.applyProviderPayment(CreatePaymentInput{}, &orderdomain.Order{OrderNo: "WRUNBOUND"}, ch, &paymentdomain.Payment{ID: 1, ChannelID: 1, ProviderType: manualQRProviderType, ChannelType: "wechat", InteractionMode: "qr", Currency: "CNY", Amount: money.FromDecimal(decimal.NewFromInt(100)), Status: constants.PaymentStatusInitiated}); err == nil {
		t.Fatal("unbound virtual order accepted")
	}
}

// Optional synthetic fixtures allow the independently deployed QR service to run
// its real SQLite handoff against native CreatePayment results.
func exportManualLocalFixture(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	dir := os.Getenv("PAYMENT_QR_FIXTURE_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("VACUUM INTO ?", filepath.Join(dir, name+".db")).Error; err != nil {
		t.Fatal(err)
	}
}
