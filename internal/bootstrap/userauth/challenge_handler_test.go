package userauthwiring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userauthtransport "github.com/dujiao-next/internal/modules/identity/userauth/transport/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	challengestore "github.com/dujiao-next/internal/modules/identity/userauth/infrastructure/gormstore"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type countedChallengeAuth struct {
	userauthtransport.User2FAAuthService
	completed atomic.Int64
}

func (a *countedChallengeAuth) CompleteLoginAfter2FA(ctx context.Context, id uint, remember bool, version ...uint64) (*userauthtransport.AuthLoginResult, error) {
	a.completed.Add(1)
	return &userauthtransport.AuthLoginResult{User: &userdomain.User{ID: id, Email: "fixture@example.invalid"}, Token: "fixture-access", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type countedChallengeTOTP struct {
	userauthtransport.User2FATOTPService
	verified atomic.Int64
	invalid  bool
}

func (s *countedChallengeTOTP) VerifyChallengeCode(uint, string) error {
	s.verified.Add(1)
	if s.invalid {
		return userauthtransport.ErrTOTPCodeInvalid
	}
	return nil
}

type failingChallengeStore struct{ reserve bool }

func (s failingChallengeStore) Reserve(context.Context, string) (bool, error) {
	if s.reserve {
		return false, errors.New("fixture storage unavailable")
	}
	return true, nil
}
func (s failingChallengeStore) Consume(context.Context, string) (bool, error) {
	return false, errors.New("fixture storage unavailable")
}
func callUserChallenge(t *testing.T, h *userauthtransport.User2FAHandler) int {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/auth/login/verify-2fa", bytes.NewBufferString(`{"challenge_token":"fixture","code":"123456"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.VerifyUser2FA(c)
	var result struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Error(err)
		return -1
	}
	return result.StatusCode
}
func TestUserChallengeUnavailableStorageFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name          string
		store         userauthtransport.User2FAChallengeStore
		verifications int64
	}{
		{"missing", nil, 0}, {"reserve_failure", failingChallengeStore{reserve: true}, 0}, {"consume_failure", failingChallengeStore{}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := &countedChallengeAuth{}
			totp := &countedChallengeTOTP{}
			h := userauthtransport.NewUser2FAHandler(totp, auth, test.store, nil)
			if status := callUserChallenge(t, h); status != 500 {
				t.Fatalf("status=%d", status)
			}
			if auth.completed.Load() != 0 || totp.verified.Load() != test.verifications {
				t.Fatal("storage failure passed authentication boundary")
			}
		})
	}
}
func TestUserChallengeHandlerConcurrentAttemptsAndSessionConsumption(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, invalid := range []bool{true, false} {
		t.Run(map[bool]string{true: "attempt_bound", false: "single_session"}[invalid], func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "challenge.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err = db.AutoMigrate(&challengestore.ChallengeRecord{}); err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { sqlDB.Close() })
			auth := &countedChallengeAuth{}
			totp := &countedChallengeTOTP{invalid: invalid}
			h := userauthtransport.NewUser2FAHandler(totp, auth, challengestore.NewChallengeStore(db), nil)
			var wg sync.WaitGroup
			for i := 0; i < 24; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); callUserChallenge(t, h) }()
			}
			wg.Wait()
			if invalid {
				if totp.verified.Load() != 5 || auth.completed.Load() != 0 {
					t.Fatalf("verify=%d complete=%d", totp.verified.Load(), auth.completed.Load())
				}
			} else if auth.completed.Load() != 1 {
				t.Fatalf("sessions=%d", auth.completed.Load())
			}
			h = userauthtransport.NewUser2FAHandler(totp, auth, challengestore.NewChallengeStore(db), nil)
			if status := callUserChallenge(t, h); status != 401 {
				t.Fatalf("persisted exhausted/consumed challenge accepted: %d", status)
			}
		})
	}
}

func (a *countedChallengeAuth) ParseUserChallengeToken(string) (*userauthtransport.UserChallengeClaims, error) {
	return &userauthtransport.UserChallengeClaims{UserID: 42, JTI: "fixture-challenge"}, nil
}
func (a *countedChallengeAuth) GetUserEmail(uint) (string, error) {
	return "fixture@example.invalid", nil
}
