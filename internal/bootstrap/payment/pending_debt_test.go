package paymentbootstrap

import (
	"errors"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymenttransport "github.com/dujiao-next/internal/modules/payment/transport/http"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"testing"
	"time"
)

type pendingDebtStore struct {
	paymentcontract.Store
	tx      paymentcontract.Transaction
	exposed bool
}

func (s *pendingDebtStore) WithinTransaction(fn func(paymentcontract.Transaction) error) error {
	return fn(s.tx)
}
func (s *pendingDebtStore) GetLatestPendingByOrder(uint, time.Time) (*paymentdomain.Payment, error) {
	s.exposed = true
	return &paymentdomain.Payment{PayURL: "https://example.invalid/pay"}, nil
}

type pendingDebtOrders struct{ ordercontract.Store }

func (pendingDebtOrders) GetByIDForUpdateWithChildren(uint) (*orderdomain.Order, error) {
	return &orderdomain.Order{UserID: 1}, nil
}

type pendingDebtWallet struct {
	walletcontract.Repository
	balance int64
}

func (w pendingDebtWallet) GetAccountByUserIDForUpdate(uint) (*walletdomain.Account, error) {
	return &walletdomain.Account{Balance: money.FromDecimal(decimal.NewFromInt(w.balance))}, nil
}

type pendingDebtWalletTx struct{ repo walletcontract.Repository }

func (w pendingDebtWalletTx) Wallets() walletcontract.Repository { return w.repo }

type pendingDebtTx struct {
	paymentcontract.Transaction
	store   *pendingDebtStore
	balance int64
}

func (t pendingDebtTx) Orders() ordercontract.Store     { return pendingDebtOrders{} }
func (t pendingDebtTx) Payments() paymentcontract.Store { return t.store }
func (t pendingDebtTx) Wallets() walletcontract.Transaction {
	return pendingDebtWalletTx{pendingDebtWallet{balance: t.balance}}
}
func TestPendingLinkBlockedUntilDebtCleared(t *testing.T) {
	for _, balance := range []int64{-5, 0, 3} {
		s := &pendingDebtStore{}
		s.tx = pendingDebtTx{store: s, balance: balance}
		result, err := (pendingLookupAdapter{payments: s}).GetLatestPendingByOrder(1, time.Now())
		if balance < 0 {
			if !errors.Is(err, paymenttransport.ErrWalletOutstandingDebt) || result != nil || s.exposed {
				t.Fatalf("debt exposed pending payment: %+v %v", result, err)
			}
		} else if err != nil || result == nil || !s.exposed {
			t.Fatalf("cleared account blocked: %v", err)
		}
	}
}
