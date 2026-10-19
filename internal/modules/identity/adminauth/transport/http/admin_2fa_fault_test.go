package adminauthhttp

import (
	"context"
	"errors"
	store "github.com/dujiao-next/internal/modules/identity/adminauth/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMFAMissingStoreMustNotIssueSession(t *testing.T) {
	h := NewAdmin2FAHandler(twoFACodeStub{}, twoFAAuthStub{}, nil, nil)
	c, w := loginContext(`{"challenge_token":"valid-test-challenge","code":"123456"}`)
	h.Verify2FA(c)
	if strings.Contains(w.Body.String(), "private-session-token") {
		t.Fatal("MFA issued a session without challenge replay protection")
	}
}

func testChallengeStore(t *testing.T) ChallengeStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&store.ChallengeRecord{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	return store.NewChallengeStore(db)
}

type brokenChallengeStore struct{ consume bool }

func (s brokenChallengeStore) Reserve(context.Context, string) (bool, error) {
	if s.consume {
		return true, nil
	}
	return false, errors.New("store offline")
}
func (s brokenChallengeStore) Consume(context.Context, string) (bool, error) {
	return false, errors.New("store offline")
}
func TestMFAStorageErrorsNeverIssueSession(t *testing.T) {
	for _, consume := range []bool{false, true} {
		h := NewAdmin2FAHandler(twoFACodeStub{}, twoFAAuthStub{}, brokenChallengeStore{consume}, nil)
		c, w := loginContext(`{"challenge_token":"test","code":"123456"}`)
		h.Verify2FA(c)
		if strings.Contains(w.Body.String(), "private-session-token") {
			t.Fatal("store error issued a session")
		}
	}
}
func TestMFAFiveAttemptsAndNewChallenge(t *testing.T) {
	s := testChallengeStore(t)
	h := NewAdmin2FAHandler(twoFACodeStub{err: ErrTOTPCodeInvalid}, twoFAAuthStub{}, s, nil)
	for i := 0; i < 5; i++ {
		c, w := loginContext(`{"challenge_token":"test","code":"bad"}`)
		h.Verify2FA(c)
		if strings.Contains(w.Body.String(), "private-session-token") {
			t.Fatal("invalid code accepted")
		}
	}
	h = NewAdmin2FAHandler(twoFACodeStub{}, twoFAAuthStub{}, s, nil)
	c, w := loginContext(`{"challenge_token":"test","code":"123456"}`)
	h.Verify2FA(c)
	if strings.Contains(w.Body.String(), "private-session-token") {
		t.Fatal("sixth attempt accepted")
	}
	if ok, err := s.Reserve(context.Background(), "new-challenge"); err != nil || !ok {
		t.Fatal("account locked")
	}
}
func TestMFAConcurrentSuccessOnlyOneSession(t *testing.T) {
	h := NewAdmin2FAHandler(twoFACodeStub{}, twoFAAuthStub{}, testChallengeStore(t), nil)
	var mu sync.Mutex
	count := 0
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, w := loginContext(`{"challenge_token":"test","code":"123456"}`)
			h.Verify2FA(c)
			if strings.Contains(w.Body.String(), "private-session-token") {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if count != 1 {
		t.Fatalf("sessions=%d", count)
	}
}
