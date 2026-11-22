package integrationtest

import (
	"sync"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	totpapplication "github.com/dujiao-next/internal/modules/identity/totp/application"
	usercontract "github.com/dujiao-next/internal/modules/identity/user/contract"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	usertotpapp "github.com/dujiao-next/internal/modules/identity/userauth/totp/application"
)

// Hold both reads until each request has loaded the same persisted snapshot.
// This models separate login challenges, for which challenge-JTI locks do not help.
type recoveryBarrierStore struct {
	usercontract.Store
	ready sync.WaitGroup
}

func (s *recoveryBarrierStore) GetByID(id uint) (*userdomain.User, error) {
	admin, err := s.Store.GetByID(id)
	s.ready.Done()
	s.ready.Wait()
	return admin, err
}

func TestUserRecoveryConcurrentChallengesConsumeOnce(t *testing.T) {
	for _, different := range []bool{false, true} {
		name := "same_code"
		if different {
			name = "different_codes"
		}
		t.Run(name, func(t *testing.T) {
			_, repo, db := newUserTOTPTestService(t)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			admin := createUserTOTPTestUser(t, repo, name)
			codes, encoded, err := totpapplication.GenerateRecoveryCodes(2)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateTOTPEnabled(admin.ID, "encrypted-test-secret", time.Now(), encoded); err != nil {
				t.Fatal(err)
			}
			barrier := &recoveryBarrierStore{Store: repo}
			barrier.ready.Add(2)
			cfg := &config.Config{App: config.AppConfig{SecretKey: "test-secret-key-for-totp"}}
			svc := usertotpapp.NewService(cfg, barrier, nil)
			results := make(chan error, 2)
			second := codes[0]
			if different {
				second = codes[1]
			}
			for _, code := range []string{codes[0], second} {
				go func(code string) { results <- svc.VerifyChallengeRecoveryCode(admin.ID, code) }(code)
			}
			successes := 0
			for i := 0; i < 2; i++ {
				if err := <-results; err == nil {
					successes++
				} else if err != totpapplication.ErrRecoveryCodeInvalid {
					t.Fatal(err)
				}
			}
			if successes != 1 {
				t.Fatalf("concurrent stale snapshots accepted %d requests; want exactly one", successes)
			}
			stored, err := repo.GetByID(admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := totpapplication.DecodeRecoveryCodes(stored.RecoveryCodes)
			if err != nil {
				t.Fatal(err)
			}
			used := 0
			for _, entry := range entries {
				if entry.UsedAt != nil {
					used++
				}
			}
			if used != successes {
				t.Fatalf("lost recovery consumption: used=%d successful=%d", used, successes)
			}
			live := usertotpapp.NewService(cfg, repo, nil)
			for i, entry := range entries {
				err := live.VerifyChallengeRecoveryCode(admin.ID, codes[i])
				if entry.UsedAt != nil && err != totpapplication.ErrRecoveryCodeInvalid {
					t.Fatalf("used code accepted: %v", err)
				}
				if entry.UsedAt == nil && err != nil {
					t.Fatalf("unused code unavailable on retry: %v", err)
				}
			}
		})
	}
}

func TestUserRecoveryStaleSnapshotCannotUndoRegenerationOrDisable(t *testing.T) {
	for _, disable := range []bool{false, true} {
		_, repo, _ := newUserTOTPTestService(t)
		admin := createUserTOTPTestUser(t, repo, "stale")
		codes, oldJSON, err := totpapplication.GenerateRecoveryCodes(1)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateTOTPEnabled(admin.ID, "secret", time.Now(), oldJSON); err != nil {
			t.Fatal(err)
		}
		consumed, err := totpapplication.MatchAndConsumeRecoveryCode(oldJSON, codes[0], time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if disable {
			if err := repo.ClearTOTP(admin.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			_, replacement, err := totpapplication.GenerateRecoveryCodes(1)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateRecoveryCodes(admin.ID, replacement); err != nil {
				t.Fatal(err)
			}
		}
		updated, err := repo.CompareAndSwapRecoveryCodes(admin.ID, oldJSON, consumed)
		if err != nil || updated {
			t.Fatalf("stale recovery write accepted: updated=%v err=%v", updated, err)
		}
	}
}
