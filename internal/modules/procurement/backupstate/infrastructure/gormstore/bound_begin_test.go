package gormstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"gorm.io/gorm"
)

func TestBoundBeginRejectsDriftAndReplay(t *testing.T) {
	db := claimDB(t)
	if err := db.AutoMigrate(&mappingdomain.ReadOnlyBinding{}, &siteconnectiondomain.Connection{}); err != nil {
		t.Fatal(err)
	}
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	conn := siteconnectiondomain.Connection{ID: 4, Name: "test", BaseURL: "https://canboso.com", Protocol: constants.ConnectionProtocolFangfang, Status: constants.ConnectionStatusActive, ApiKey: "marker", ApiSecret: "cipher-original"}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mappingdomain.ReadOnlyBinding{ConnectionID: 4, LocalProductID: in.productID, LocalSKUID: in.skuID, UpstreamProductID: 2, UpstreamSKUCode: "6a4b4d7d1827db4ca39bb8be", Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := prepareSnapshot(db, c.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	var saved backupstate.Claim
	if err := db.First(&saved, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	originalSnapshot := saved.SnapshotJSON
	var invalid purchaseSnapshot
	if err := json.Unmarshal([]byte(originalSnapshot), &invalid); err != nil {
		t.Fatal(err)
	}
	invalid.UpstreamProductID = "unexpected-product"
	badSnapshot, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("snapshot_json", string(badSnapshot)).Error; err != nil {
		t.Fatal(err)
	}
	decryptCalls := 0
	decrypt := func(s string) (string, error) {
		decryptCalls++
		if s != "cipher-original" {
			t.Fatal("unexpected ciphertext")
		}
		return "synthetic-key", nil
	}
	policy := func(_ *gorm.DB, id uint) error {
		if id != c.ID {
			t.Fatal("wrong claim")
		}
		return nil
	}
	if _, started, err := beginBound(context.Background(), db, c.ID, decrypt, policy, time.Now()); err == nil || started || decryptCalls != 0 {
		t.Fatal("unexpected product accepted")
	}
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", c.ID).Update("snapshot_json", originalSnapshot).Error; err != nil {
		t.Fatal(err)
	}
	if _, started, err := beginBound(context.Background(), db, c.ID, decrypt, nil, time.Now()); err == nil || started {
		t.Fatal("missing policy accepted")
	}
	if err := db.Exec("UPDATE site_connections SET api_secret = ? WHERE id = ?", "changed", 4).Error; err != nil {
		t.Fatal(err)
	}
	if _, started, err := beginBound(context.Background(), db, c.ID, decrypt, policy, time.Now()); err == nil || started || decryptCalls != 0 {
		t.Fatal("identity drift accepted")
	}
	if err := db.Exec("UPDATE site_connections SET api_secret = ? WHERE id = ?", "cipher-original", 4).Error; err != nil {
		t.Fatal(err)
	}
	attempt, started, err := beginBound(context.Background(), db, c.ID, decrypt, policy, time.Now())
	if err != nil || !started || attempt.Client == nil || attempt.ClaimID != c.ID || decryptCalls != 1 {
		t.Fatalf("valid bound begin failed: %v", err)
	}
	if _, started, err := beginBound(context.Background(), db, c.ID, decrypt, policy, time.Now()); err == nil || started || decryptCalls != 1 {
		t.Fatal("purchase replay accepted")
	}
}
