package gormstore

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openStore(t *testing.T, path string) *ChallengeStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&ChallengeRecord{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	return NewChallengeStore(db)
}
func TestChallengeConcurrentAttemptsAndConsumePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	stores := []*ChallengeStore{openStore(t, path), openStore(t, path)}
	ctx := context.Background()
	var allowed, consumed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := stores[i%2].Reserve(ctx, "same-token")
			if err != nil {
				t.Error(err)
			}
			if ok {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("attempts=%d", allowed.Load())
	}
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := stores[i%2].Consume(ctx, "same-token")
			if err != nil {
				t.Error(err)
			}
			if ok {
				consumed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if consumed.Load() != 1 {
		t.Fatalf("sessions=%d", consumed.Load())
	}
	restarted := openStore(t, path)
	if ok, err := restarted.Reserve(ctx, "same-token"); err != nil || ok {
		t.Fatalf("replay after restart: %v %v", ok, err)
	}
	if ok, err := restarted.Reserve(ctx, "fresh-password-challenge"); err != nil || !ok {
		t.Fatalf("new login locked: %v %v", ok, err)
	}
}
func TestChallengeStorageFailureAndRecovery(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "auth.db"))
	ctx := context.Background()
	store.db.Exec("PRAGMA query_only=ON")
	if ok, err := store.Reserve(ctx, "test"); err == nil || ok {
		t.Fatal("write failure opened challenge")
	}
	store.db.Exec("PRAGMA query_only=OFF")
	if ok, err := store.Reserve(ctx, "test"); err != nil || !ok {
		t.Fatalf("recovery blocked: %v", err)
	}
	store.db.Exec("PRAGMA query_only=ON")
	if ok, err := store.Consume(ctx, "test"); err == nil || ok {
		t.Fatal("consume failure issued session")
	}
	store.db.Exec("PRAGMA query_only=OFF")
	if ok, err := store.Consume(ctx, "test"); err != nil || !ok {
		t.Fatalf("consume recovery blocked: %v", err)
	}
}
func TestChallengeCleanupPreservesLiveState(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "auth.db"))
	ctx := context.Background()
	store.db.Create(&ChallengeRecord{Key: "expired", ExpiresAt: time.Now().UTC().Add(-time.Hour)})
	if ok, err := store.Reserve(ctx, "live"); !ok || err != nil {
		t.Fatal(err)
	}
	var count int64
	store.db.Model(&ChallengeRecord{}).Where("key = ?", "expired").Count(&count)
	if count != 0 {
		t.Fatal("expired rows retained")
	}
	var row ChallengeRecord
	store.db.First(&row)
	if row.Key == "live" || row.ExpiresAt.Before(time.Now().Add(5*time.Minute)) {
		t.Fatal("unsafe retention or raw token storage")
	}
}
