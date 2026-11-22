package gormstore

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	siteconnectioncontract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupSiteConnectionStoreTest(t *testing.T) (*gorm.DB, *Store) {
	t.Helper()
	dsn := fmt.Sprintf("file:siteconnection_store_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&siteconnectiondomain.Connection{}); err != nil {
		t.Fatalf("migrate site connection: %v", err)
	}
	return db, New(db)
}

func TestFangConnectionMutationsShareBackupFence(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	if err := db.AutoMigrate(&backupstate.Gate{}, &backupstate.Claim{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&backupstate.Gate{ID: 1, Enabled: false}).Error; err != nil {
		t.Fatal(err)
	}
	connection := &siteconnectiondomain.Connection{Name: "Fang", BaseURL: "https://canboso.com", ApiKey: "marker", ApiSecret: "ciphertext", Protocol: constants.ConnectionProtocolFangfang, Status: constants.ConnectionStatusDisabled}
	if err := store.Create(connection); err != nil {
		t.Fatal(err)
	}
	connection.Name = "allowed before enabling"
	if err := store.Update(connection); err != nil {
		t.Fatalf("closed idle gate: %v", err)
	}
	if err := db.Model(&backupstate.Gate{}).Where("id = 1").Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	connection.Name = "must not save"
	if err := store.Update(connection); err == nil {
		t.Fatal("open gate permitted connection change")
	}
	if err := store.Delete(connection.ID); err == nil {
		t.Fatal("open gate permitted deletion")
	}
	if err := db.Model(&backupstate.Gate{}).Where("id = 1").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	claim := &backupstate.Claim{LocalOrderID: 1, State: backupstate.Uncertain, Product: backupstate.Plus}
	if err := db.Create(claim).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Update(connection); err == nil {
		t.Fatal("uncertain purchase permitted connection change")
	}
	if err := store.Delete(connection.ID); err == nil {
		t.Fatal("uncertain purchase permitted deletion")
	}
	if err := db.Model(claim).Updates(map[string]interface{}{"state": backupstate.NotSent, "submitted_at": nil, "request_sha256": "", "connection_sha256": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Update(connection); err != nil {
		t.Fatalf("proved not sent: %v", err)
	}
	if err := db.Model(claim).Update("request_sha256", "unexpected").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(connection.ID); err == nil {
		t.Fatal("contradictory not-sent evidence permitted deletion")
	}
	var saved siteconnectiondomain.Connection
	if err := db.First(&saved, connection.ID).Error; err != nil || saved.DeletedAt != nil || saved.Name != connection.Name {
		t.Fatalf("connection state invalid: %v", err)
	}
}

func TestFangConnectionMutationFailsClosedWithoutGate(t *testing.T) {
	_, store := setupSiteConnectionStoreTest(t)
	c := &siteconnectiondomain.Connection{Name: "Fang", BaseURL: "https://canboso.com", ApiKey: "marker", ApiSecret: "ciphertext", Protocol: constants.ConnectionProtocolFangfang, Status: constants.ConnectionStatusDisabled}
	if err := store.Create(c); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(c); err == nil {
		t.Fatal("missing gate permitted update")
	}
	if err := store.Delete(c.ID); err == nil {
		t.Fatal("missing gate permitted deletion")
	}
}

func TestOtherConnectionsCanUpdateWithoutBackupSchema(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	c := &siteconnectiondomain.Connection{Name: "normal", BaseURL: "https://example.com", ApiKey: "key", ApiSecret: "ciphertext", Protocol: constants.ConnectionProtocolDujiaoNext, Status: constants.ConnectionStatusActive}
	if err := store.Create(c); err != nil {
		t.Fatal(err)
	}
	c.Name, c.ApiSecret, c.Status = "changed", "rotated", constants.ConnectionStatusDisabled
	if err := store.Update(c); err != nil {
		t.Fatal(err)
	}
	var saved siteconnectiondomain.Connection
	if err := db.First(&saved, c.ID).Error; err != nil || saved.Name != "changed" || saved.ApiSecret != "rotated" || saved.Status != constants.ConnectionStatusDisabled {
		t.Fatalf("ordinary update changed behavior: %v", err)
	}
}

func TestProtocolChangeCannotBypassBackupFence(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	if err := db.AutoMigrate(&backupstate.Gate{}, &backupstate.Claim{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&backupstate.Gate{ID: 1, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	normal := &siteconnectiondomain.Connection{Name: "normal", BaseURL: "https://example.com", ApiKey: "marker", ApiSecret: "ciphertext", Protocol: constants.ConnectionProtocolDujiaoNext}
	fang := &siteconnectiondomain.Connection{Name: "fang", BaseURL: "https://canboso.com", ApiKey: "marker", ApiSecret: "ciphertext", Protocol: constants.ConnectionProtocolFangfang}
	if err := store.Create(normal); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(fang); err != nil {
		t.Fatal(err)
	}
	normal.Protocol = constants.ConnectionProtocolFangfang
	if err := store.Update(normal); err == nil {
		t.Fatal("conversion to Fang bypassed gate")
	}
	fang.Protocol = constants.ConnectionProtocolDujiaoNext
	if err := store.Update(fang); err == nil {
		t.Fatal("conversion from Fang bypassed gate")
	}
	var saved siteconnectiondomain.Connection
	if err := db.First(&saved, fang.ID).Error; err != nil || saved.Protocol != constants.ConnectionProtocolFangfang {
		t.Fatalf("Fang protocol changed: %v", err)
	}
}

func TestRecordPingCannotOverwriteFangConnectionConfiguration(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	if err := db.AutoMigrate(&backupstate.Gate{}, &backupstate.Claim{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&backupstate.Gate{ID: 1, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	c := &siteconnectiondomain.Connection{Name: "Fang", BaseURL: "https://canboso.com", ApiKey: "marker", ApiSecret: "new-ciphertext", Protocol: constants.ConnectionProtocolFangfang, Status: constants.ConnectionStatusPending}
	if err := store.Create(c); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if err := store.RecordPing(c.ID, when, true, true); err != nil {
		t.Fatal(err)
	}
	var got siteconnectiondomain.Connection
	if err := db.First(&got, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ApiSecret != "new-ciphertext" || got.Status != constants.ConnectionStatusPending || !got.LastPingOK || got.LastPingAt == nil || !got.LastPingAt.Equal(when) {
		t.Fatal("ping modified protected configuration or did not store health")
	}
	if err := db.Model(&got).Updates(map[string]interface{}{"api_secret": "rotated-ciphertext", "status": constants.ConnectionStatusDisabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.RecordPing(c.ID, when, false, true); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&got, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ApiSecret != "rotated-ciphertext" || got.Status != constants.ConnectionStatusDisabled || got.LastPingOK {
		t.Fatal("ping overwrote newer configuration")
	}

	normal := &siteconnectiondomain.Connection{Name: "normal", BaseURL: "https://example.com", ApiKey: "key", ApiSecret: "ciphertext", Protocol: constants.ConnectionProtocolDujiaoNext, Status: constants.ConnectionStatusPending}
	if err := store.Create(normal); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordPing(normal.ID, when, true, true); err != nil {
		t.Fatal(err)
	}
	if err := db.First(normal, normal.ID).Error; err != nil || normal.Status != constants.ConnectionStatusActive {
		t.Fatalf("ordinary pending activation changed: %v", err)
	}
}

func TestStoreDeleteHidesConnectionFromEveryReadPath(t *testing.T) {
	db, store := setupSiteConnectionStoreTest(t)
	connection := &siteconnectiondomain.Connection{
		Name:               "primary upstream",
		BaseURL:            "https://upstream.example.com",
		ApiKey:             "upstream-key",
		ApiSecret:          "encrypted-secret",
		Protocol:           constants.ConnectionProtocolDujiaoNext,
		Status:             constants.ConnectionStatusActive,
		RetryMax:           5,
		RetryIntervals:     "[30,60,300]",
		ExchangeRate:       decimal.NewFromInt(1),
		PriceMarkupPercent: decimal.Zero,
		PriceRoundingMode:  "none",
	}
	if err := store.Create(connection); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	if err := store.Delete(connection.ID); err != nil {
		t.Fatalf("delete connection: %v", err)
	}

	if got, err := store.GetByID(connection.ID); err != nil || got != nil {
		t.Fatalf("GetByID after delete = %#v, %v; want nil, nil", got, err)
	}
	if got, err := store.GetByApiKey(connection.ApiKey); err != nil || got != nil {
		t.Fatalf("GetByApiKey after delete = %#v, %v; want nil, nil", got, err)
	}
	listed, total, err := store.List(siteconnectioncontract.ListFilter{})
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if total != 0 || len(listed) != 0 {
		t.Fatalf("list after delete = total %d rows %d; want empty", total, len(listed))
	}
	active, err := store.ListActive()
	if err != nil {
		t.Fatalf("list active connections: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active list after delete has %d rows; want empty", len(active))
	}

	var persisted siteconnectiondomain.Connection
	if err := db.Where("id = ?", connection.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load persisted deleted connection: %v", err)
	}
	if persisted.DeletedAt == nil {
		t.Fatal("delete must persist deleted_at instead of physically removing the row")
	}
}
