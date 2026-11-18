package gormstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
)

func TestSnapshotUsesPersistedBindingWithoutStoringSecret(t *testing.T) {
	db := claimDB(t)
	if err := db.AutoMigrate(&mappingdomain.ReadOnlyBinding{}, &siteconnectiondomain.Connection{}); err != nil {
		t.Fatal(err)
	}
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	conn := siteconnectiondomain.Connection{ID: 4, Name: "Fang Fang", BaseURL: "https://canboso.com", Protocol: "fangfang-readonly", ApiKey: "marker", ApiSecret: "fake-encrypted-secret"}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatal(err)
	}
	b := mappingdomain.ReadOnlyBinding{ConnectionID: conn.ID, LocalProductID: in.productID, LocalSKUID: in.skuID, UpstreamProductID: 2, UpstreamSKUCode: "6a4b4d7d1827db4ca39bb8be", Currency: "USD"}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	if err := prepareSnapshot(db, c.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	var stored backupstate.Claim
	if err := db.First(&stored, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.SnapshotJSON, conn.ApiSecret) {
		t.Fatal("secret leaked into snapshot")
	}
	var snap purchaseSnapshot
	if err := json.Unmarshal([]byte(stored.SnapshotJSON), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ConnectionID != 4 || snap.UpstreamProductID != b.UpstreamSKUCode || snap.Quantity != 1 || snap.PreflightMaxUSD != "7.00" || snap.IdempotencyKey == "" || !sha256Hex(snap.ConnectionSHA256) {
		t.Fatalf("invalid snapshot %+v", snap)
	}
	if err := prepareSnapshot(db, c.ID, time.Now()); err == nil {
		t.Fatal("snapshot overwritten")
	}
}
