package application

import "testing"

func TestUserSuppliedEmailRejectsTelegramIdentity(t *testing.T) {
	if _, err := normalizeUserSuppliedEmail(" TELEGRAM_123@LOGIN.LOCAL "); err == nil {
		t.Fatal("accepted reserved identity")
	}
	if got, err := normalizeUserSuppliedEmail(" Buyer@Example.com "); err != nil || got != "buyer@example.com" {
		t.Fatalf("got %q, %v", got, err)
	}
}
