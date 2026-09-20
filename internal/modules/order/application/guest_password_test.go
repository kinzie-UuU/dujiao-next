package application

import (
	"errors"
	"testing"
)

func TestGuestPasswordMinimum(t *testing.T) {
	for _, tc := range []struct {
		password string
		want     error
	}{{" ", ErrGuestPasswordRequired}, {"12345", ErrGuestPasswordTooShort}, {"你好世界呀", ErrGuestPasswordTooShort}, {"123456", nil}, {"你好世界您好", nil}} {
		if got := validateGuestPassword(tc.password); !errors.Is(got, tc.want) {
			t.Errorf("password %q: got %v want %v", tc.password, got, tc.want)
		}
	}
}
