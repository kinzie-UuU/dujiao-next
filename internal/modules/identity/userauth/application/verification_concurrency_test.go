package application

import (
	"fmt"
	"github.com/dujiao-next/internal/config"
	contract "github.com/dujiao-next/internal/modules/identity/emailverification/contract"
	domain "github.com/dujiao-next/internal/modules/identity/emailverification/domain"
	store "github.com/dujiao-next/internal/modules/identity/emailverification/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
	"time"
)

type verificationSnapshotBarrier struct {
	contract.Store
	ready   chan struct{}
	release chan struct{}
}

func (s *verificationSnapshotBarrier) GetLatest(email, purpose string) (*domain.Code, error) {
	r, e := s.Store.GetLatest(email, purpose)
	s.ready <- struct{}{}
	<-s.release
	return r, e
}

func TestConcurrentVerificationHonorsSingleUseAndAttemptLimit(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:verify_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			if err = db.AutoMigrate(&domain.Code{}); err != nil {
				t.Fatal(err)
			}
			repo := store.New(db)
			record := &domain.Code{Email: "concurrent@example.invalid", Purpose: "register", Code: "123456", ExpiresAt: time.Now().Add(time.Hour), SentAt: time.Now(), CreatedAt: time.Now()}
			if err = repo.Create(record); err != nil {
				t.Fatal(err)
			}
			barrier := &verificationSnapshotBarrier{Store: repo, ready: make(chan struct{}, 2), release: make(chan struct{})}
			cfg := &config.Config{}
			cfg.Email.VerifyCode.MaxAttempts = 1
			svc := &Service{cfg: cfg, codeRepo: barrier}
			code := "000000"
			if valid {
				code = record.Code
			}
			var successes atomic.Int32
			done := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() {
					_, err := svc.verifyCode(record.Email, record.Purpose, code)
					if err == nil {
						successes.Add(1)
					}
					done <- err
				}()
			}
			<-barrier.ready
			<-barrier.ready
			close(barrier.release)
			<-done
			<-done
			got, err := repo.GetLatest(record.Email, record.Purpose)
			if err != nil {
				t.Fatal(err)
			}
			if got.AttemptCount != 1 {
				t.Fatalf("attempt count=%d", got.AttemptCount)
			}
			want := int32(0)
			if valid {
				want = 1
			}
			if successes.Load() != want {
				t.Fatalf("successes=%d want=%d", successes.Load(), want)
			}
		})
	}
}
