package application

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestDummyAdminPasswordHashExecutesBcrypt(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyAdminPasswordHash))
	if err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("dummy hash must be valid cost-%d bcrypt: cost=%d err=%v", bcrypt.DefaultCost, cost, err)
	}
	// A mismatch, as opposed to a malformed hash error, requires bcrypt to
	// complete its password computation and prevents a silent timing regression.
	if err := bcrypt.CompareHashAndPassword([]byte(dummyAdminPasswordHash), []byte("not-the-dummy-password")); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("dummy hash did not complete bcrypt comparison: %v", err)
	}
}
