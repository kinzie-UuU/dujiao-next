package integrationtest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	affiliateapp "github.com/dujiao-next/internal/modules/affiliate/application"
	affiliatecontract "github.com/dujiao-next/internal/modules/affiliate/contract"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	affiliatehttp "github.com/dujiao-next/internal/modules/affiliate/transport/http"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	settingsstore "github.com/dujiao-next/internal/modules/settings/infrastructure/gormstore"
	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	walletstore "github.com/dujiao-next/internal/modules/wallet/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type rewardChannelUser uint

func (u rewardChannelUser) ProvisionUserID(_ affiliatehttp.ChannelIdentity) (uint, error) {
	return uint(u), nil
}

func TestGPTRewardWalletCreditSpendRefundAndRecharge(t *testing.T) {
	svc, db, store := setupAffiliateServiceTest(t)
	if err := db.AutoMigrate(&settingsstore.SettingRecord{}, &affiliatedomain.Commission{}, &affiliatedomain.WithdrawRequest{}, &walletdomain.Account{}, &walletdomain.Transaction{}); err != nil {
		t.Fatal(err)
	}
	settings := settingsapp.NewService(settingsstore.New(db))
	svc = affiliateapp.NewService(store, userstore.New(db), nil, nil, settings)
	if _, err := settings.UpdateAffiliateSetting(settingsintegration.AffiliateSetting{Enabled: true, RewardMode: "gpt_random_wallet", EligibleProductIDs: []uint{1}}); err != nil {
		t.Fatal(err)
	}
	user := createAffiliateTestUser(t, db, "wallet-reward@example.com")
	profile := createAffiliateTestProfile(t, db, user.ID, "WALLET01", constants.AffiliateProfileStatusActive)
	amount := func(n int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(n)) }
	reward := affiliatedomain.Commission{AffiliateProfileID: profile.ID, OrderID: 1, CommissionType: "gpt_reward", BaseAmount: amount(100), CommissionAmount: amount(4), Status: constants.AffiliateCommissionStatusAvailable}
	if err := store.CreateCommission(&reward); err != nil {
		t.Fatal(err)
	}
	legacy := reward
	legacy.ID = 0
	legacy.OrderID = 2
	legacy.CommissionType = "order"
	if err := store.CreateCommission(&legacy); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Hour)
	reward.Status = constants.AffiliateCommissionStatusPendingConfirm
	reward.ConfirmAt = &due
	if err := store.UpdateCommission(&reward); err != nil {
		t.Fatal(err)
	}
	beforeCredit, err := svc.GetUserDashboard(user.ID)
	if err != nil || !beforeCredit.AvailableCommission.Decimal.Equal(decimal.NewFromInt(4)) || !beforeCredit.PendingCommission.Decimal.IsZero() {
		t.Fatalf("dashboard did not mature due reward: %+v err=%v", beforeCredit, err)
	}
	credited, err := svc.CreditRewardsToWallet(user.ID)
	if err != nil || !credited.Decimal.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("credit=%v err=%v", credited, err)
	}
	dashboard, err := svc.GetUserDashboard(user.ID)
	if err != nil || !dashboard.WalletCreditedCommission.Decimal.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("dashboard=%+v err=%v", dashboard, err)
	}
	if !dashboard.AvailableCommission.Decimal.IsZero() {
		t.Fatal("legacy commission leaked into transferable dashboard amount")
	}
	router := gin.New()
	userRoutes := router.Group("/user", func(c *gin.Context) { c.Set("user_id", user.ID); c.Next() })
	affiliatehttp.RegisterUserRoutes(userRoutes, affiliatehttp.NewHandler(svc))
	affiliatehttp.RegisterChannelRoutes(router.Group("/channel"), affiliatehttp.NewChannelHandler(svc, rewardChannelUser(user.ID), settings))
	for _, path := range []string{"/user/affiliate/wallet-credit", "/channel/affiliate/wallet-credit"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"channel_user_id":"123"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		var payload struct {
			Data struct {
				CreditedAmount string `json:"credited_amount"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || payload.Data.CreditedAmount == "" || !decimal.RequireFromString(payload.Data.CreditedAmount).IsZero() {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
	credited, err = svc.CreditRewardsToWallet(user.ID)
	if err != nil || !credited.Decimal.IsZero() {
		t.Fatalf("repeat credit=%v err=%v", credited, err)
	}
	legacyAfter, err := store.GetCommissionByOrderAndProfile(2, profile.ID, "order")
	if err != nil || legacyAfter.Status != constants.AffiliateCommissionStatusAvailable {
		t.Fatal("legacy commission was converted")
	}
	if _, err := svc.ApplyWithdraw(user.ID, affiliateapp.WithdrawApplyInput{Amount: decimal.NewFromInt(1), Channel: "manual", Account: "test"}); !errors.Is(err, affiliateapp.ErrDisabled) {
		t.Fatalf("withdraw should be disabled: %v", err)
	}
	request := affiliatedomain.WithdrawRequest{AffiliateProfileID: profile.ID, Amount: amount(4), Channel: "manual", Account: "test", Status: constants.AffiliateWithdrawStatusPendingReview}
	if err := store.CreateWithdraw(&request); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReviewWithdraw(1, request.ID, constants.AffiliateWithdrawActionPay, ""); !errors.Is(err, affiliateapp.ErrDisabled) {
		t.Fatalf("pay should be disabled: %v", err)
	}
	if _, err := svc.ReviewWithdraw(1, request.ID, constants.AffiliateWithdrawActionReject, "disabled"); err != nil {
		t.Fatal(err)
	}
	wallets := walletstore.New(db)
	wallet := walletapp.NewService(walletapp.Options{Repository: wallets, Transactions: wallets})
	if err := wallets.WithinTransaction(func(tx walletcontract.Transaction) error {
		paid, err := wallet.ApplyOrderBalance(tx, walletcontract.OrderBalanceInput{UserID: user.ID, OrderID: 99, TotalAmount: amount(4), UseBalance: true, Currency: "CNY"})
		if err == nil && !paid.Decimal.Equal(decimal.NewFromInt(4)) {
			t.Fatalf("paid=%s", paid)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	order := &orderdomain.Order{ID: 1, TotalAmount: amount(100)}
	for i, delta := range []int64{25, 75} {
		before := decimal.Zero
		if i == 1 {
			before = decimal.NewFromInt(25)
		}
		if err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
			return svc.HandleOrderRefunded(tx, order, decimal.NewFromInt(delta), before, "refund")
		}); err != nil {
			t.Fatal(err)
		}
	}
	account, err := wallets.GetAccountByUserID(user.ID)
	if err != nil || !account.Balance.Decimal.Equal(decimal.NewFromInt(-4)) {
		t.Fatalf("debt=%+v err=%v", account, err)
	}
	if _, _, err := wallet.AdminAdjustBalance(walletcontract.AdjustBalanceInput{UserID: user.ID, OperatorAdminID: 1, Delta: amount(-1)}); !errors.Is(err, walletcontract.ErrInsufficientBalance) {
		t.Fatalf("ordinary debit must not deepen debt: %v", err)
	}
	if _, _, err := wallet.Recharge(walletcontract.RechargeInput{UserID: user.ID, Amount: amount(1), Currency: "CNY"}); err != nil {
		t.Fatal(err)
	}
	account, err = wallets.GetAccountByUserID(user.ID)
	if err != nil || !account.Balance.Decimal.Equal(decimal.NewFromInt(-3)) {
		t.Fatalf("partial debt repayment=%+v err=%v", account, err)
	}
	if err := wallets.WithinTransaction(func(tx walletcontract.Transaction) error {
		_, _, err := wallet.CreditInTransaction(tx, walletcontract.CreditInput{UserID: user.ID, Amount: amount(4), Currency: "CNY", Reference: "topup:second"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	account, err = wallets.GetAccountByUserID(user.ID)
	if err != nil || !account.Balance.Decimal.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("recharged=%+v err=%v", account, err)
	}
	row, err := store.GetCommissionByOrderAndProfile(1, profile.ID, "gpt_reward")
	if err != nil || row.Status != constants.AffiliateCommissionStatusRejected || !row.CommissionAmount.Decimal.IsZero() {
		t.Fatalf("refunded reward=%+v err=%v", row, err)
	}
	var count int64
	if err := db.Model(&walletdomain.Transaction{}).Where("type = ?", "affiliate_reward").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("credit count=%d err=%v", count, err)
	}
}

func TestRewardWalletAdjustmentRollbackAndReferenceIdempotency(t *testing.T) {
	_, db, store := setupAffiliateServiceTest(t)
	if err := db.AutoMigrate(&walletdomain.Account{}, &walletdomain.Transaction{}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("rollback")
	err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
		if err := tx.AdjustRewardWallet(1, decimal.NewFromInt(3), "reward:test", 1); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var count int64
	db.Model(&walletdomain.Transaction{}).Count(&count)
	if count != 0 {
		t.Fatal("rolled-back reward survived")
	}
	for i := 0; i < 2; i++ {
		if err := store.WithinTransaction(func(tx affiliatecontract.Store) error {
			return tx.AdjustRewardWallet(1, decimal.NewFromInt(3), "reward:test", 1)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var account walletdomain.Account
	if err := db.Where("user_id = ?", 1).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	if !account.Balance.Decimal.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("duplicate reward: %s", account.Balance)
	}
}
