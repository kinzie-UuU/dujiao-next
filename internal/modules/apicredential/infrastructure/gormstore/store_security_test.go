package gormstore_test

import (
	apicredentialdomain "github.com/dujiao-next/internal/modules/apicredential/domain"
	"github.com/dujiao-next/internal/modules/apicredential/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestTouchLastUsedPreservesRevocationAndSecret(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&apicredentialdomain.ApiCredential{}); err != nil {
		t.Fatal(err)
	}
	store := gormstore.New(db)
	cred := &apicredentialdomain.ApiCredential{UserID: 88, ApiKey: "key", ApiSecret: "rotated", IsActive: false, Status: "revoked"}
	if err := store.Create(cred); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.TouchLastUsedAt(cred.ID, now); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByID(cred.ID)
	if err != nil || got.IsActive || got.ApiSecret != "rotated" || got.Status != "revoked" || got.LastUsedAt == nil {
		t.Fatalf("lost security state: %+v, %v", got, err)
	}
	if err := store.Delete(cred.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchLastUsedAt(cred.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var deleted apicredentialdomain.ApiCredential
	if err := db.First(&deleted, cred.ID).Error; err != nil {
		t.Fatal(err)
	}
	if deleted.LastUsedAt == nil || !deleted.LastUsedAt.Equal(*got.LastUsedAt) {
		t.Fatal("touch modified deleted credential")
	}
}
