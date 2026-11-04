package securestore_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	apicredentialdomain "github.com/dujiao-next/internal/modules/apicredential/domain"
	cardsecretcontract "github.com/dujiao-next/internal/modules/cardsecret/contract"
	cardsecretdomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	cardsecretstore "github.com/dujiao-next/internal/modules/cardsecret/infrastructure/gormstore"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	"github.com/dujiao-next/internal/securestore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSensitiveValuesAreEncryptedAndRemainUsable(t *testing.T) {
	const key = "test-secure-storage-key-with-enough-entropy"
	if err := securestore.Configure(key); err != nil {
		t.Fatalf("configure: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "secure.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&cardsecretdomain.Secret{}, &fulfillmentdomain.Fulfillment{}, &procurementdomain.Order{}, &apicredentialdomain.ApiCredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	secrets := []cardsecretdomain.Secret{
		{ProductID: 1, SKUID: 1, Secret: "ALPHA-CARD-001", Status: cardsecretdomain.StatusAvailable},
		{ProductID: 1, SKUID: 1, Secret: "BETA-CARD-002", Status: cardsecretdomain.StatusAvailable},
		{ProductID: 1, SKUID: 1, Secret: "BETA-CARD-003", Status: cardsecretdomain.StatusUsed},
	}
	if err := db.Create(&secrets).Error; err != nil {
		t.Fatalf("create secrets: %v", err)
	}
	fulfillment := fulfillmentdomain.Fulfillment{
		OrderID:       99,
		Type:          "auto",
		Status:        "delivered",
		Payload:       "DELIVERED-CARD-999",
		LogisticsJSON: jsonmap.JSON{"account": "buyer-secret@example.test", "password": "delivery-password"},
	}
	if err := db.Create(&fulfillment).Error; err != nil {
		t.Fatalf("create fulfillment: %v", err)
	}

	assertRawEncrypted(t, db, "card_secrets", secrets[0].ID, "secret", "ALPHA-CARD-001")
	assertRawEncrypted(t, db, "fulfillments", fulfillment.ID, "payload", "DELIVERED-CARD-999")
	assertRawEncrypted(t, db, "fulfillments", fulfillment.ID, "logistics_json", "delivery-password")

	store := cardsecretstore.New(db)
	items, total, err := store.List(cardsecretcontract.ListFilter{ProductID: 1, Secret: "beta-card", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("search encrypted secrets: %v", err)
	}
	if total != 2 || len(items) != 1 || items[0].Secret != "BETA-CARD-002" {
		t.Fatalf("encrypted search compatibility failed: total=%d items=%+v", total, items)
	}

	if err := db.Exec(
		"INSERT INTO card_secrets (product_id, sku_id, secret, status) VALUES (?, ?, ?, ?)",
		1, 1, "LEGACY-CARD-004", cardsecretdomain.StatusAvailable,
	).Error; err != nil {
		t.Fatalf("insert legacy secret: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO fulfillments (order_id, type, status, payload, logistics_json) VALUES (?, ?, ?, ?, ?)",
		100, "manual", "delivered", "LEGACY-DELIVERY-100", `{"account":"legacy-account","password":"legacy-password"}`,
	).Error; err != nil {
		t.Fatalf("insert legacy fulfillment: %v", err)
	}
	if err := db.Exec("INSERT INTO procurement_orders (id, connection_id,local_order_id, local_order_no, upstream_payload, trace_id,currency) VALUES (1,1,1,'TEST','LEGACY-UPSTREAM','TEST','CNY')").Error; err != nil {
		t.Fatal(err)
	}
	credential := apicredentialdomain.ApiCredential{UserID: 1, ApiKey: "test-key", ApiSecret: "new-api-secret"}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	assertRawEncrypted(t, db, "api_credentials", credential.ID, "api_secret", "new-api-secret")
	credential.ApiSecret = "rotated-api-secret"
	if err := db.Save(&credential).Error; err != nil {
		t.Fatal(err)
	}
	assertRawEncrypted(t, db, "api_credentials", credential.ID, "api_secret", "rotated-api-secret")
	if err := db.Exec("INSERT INTO api_credentials (id,user_id,api_key,api_secret) VALUES (99,99,'legacy-key','legacy-api-secret')").Error; err != nil {
		t.Fatal(err)
	}
	var legacyBefore apicredentialdomain.ApiCredential
	if err := db.First(&legacyBefore, 99).Error; err != nil || legacyBefore.ApiSecret != "legacy-api-secret" {
		t.Fatal("legacy credential unreadable before migration")
	}
	result, err := securestore.Backfill(db)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if result.CardSecrets != 1 || result.Fulfillments != 1 || result.ProcurementPayloads != 1 || result.APICredentials != 1 {
		t.Fatalf("unexpected backfill result: %+v", result)
	}

	var legacySecret cardsecretdomain.Secret
	if err := db.Where("secret != ?", "").Order("id desc").First(&legacySecret).Error; err != nil {
		t.Fatalf("read migrated secret: %v", err)
	}
	if legacySecret.Secret != "LEGACY-CARD-004" {
		t.Fatalf("migrated secret changed: %q", legacySecret.Secret)
	}
	assertRawEncrypted(t, db, "card_secrets", legacySecret.ID, "secret", "LEGACY-CARD-004")

	assertRawEncrypted(t, db, "procurement_orders", 1, "upstream_payload", "LEGACY-UPSTREAM")
	var proc procurementdomain.Order
	if err := db.First(&proc, 1).Error; err != nil || proc.UpstreamPayload != "LEGACY-UPSTREAM" {
		t.Fatal("legacy procurement no longer readable")
	}
	assertRawEncrypted(t, db, "api_credentials", 99, "api_secret", "legacy-api-secret")
	for id, expected := range map[uint]string{credential.ID: "rotated-api-secret", 99: "legacy-api-secret"} {
		var loaded apicredentialdomain.ApiCredential
		if err := db.First(&loaded, id).Error; err != nil {
			t.Fatal(err)
		}
		actualMAC := hmac.New(sha256.New, []byte(loaded.ApiSecret))
		actualMAC.Write([]byte("signed-request"))
		expectedMAC := hmac.New(sha256.New, []byte(expected))
		expectedMAC.Write([]byte("signed-request"))
		if !hmac.Equal(actualMAC.Sum(nil), expectedMAC.Sum(nil)) {
			t.Fatal("API signing secret changed")
		}
		encoded, err := json.Marshal(loaded)
		if err != nil || strings.Contains(string(encoded), expected) || strings.Contains(string(encoded), "api_secret") {
			t.Fatal("API credential leaks secret in JSON")
		}
	}
	second, err := securestore.Backfill(db)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.CardSecrets != 0 || second.Fulfillments != 0 || second.ProcurementPayloads != 0 || second.APICredentials != 0 {
		t.Fatalf("backfill is not idempotent: %+v", second)
	}
}

func TestWrongKeyFailsClosed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrong-key.db")
	if err := securestore.Configure("first-encryption-key"); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cardsecretdomain.Secret{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cardsecretdomain.Secret{ProductID: 1, SKUID: 1, Secret: "MUST-NOT-LEAK", Status: cardsecretdomain.StatusAvailable}).Error; err != nil {
		t.Fatal(err)
	}

	if err := securestore.Configure("different-encryption-key"); err != nil {
		t.Fatal(err)
	}
	wrongDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var item cardsecretdomain.Secret
	if err := wrongDB.First(&item).Error; err == nil {
		t.Fatalf("wrong key unexpectedly returned plaintext: %q", item.Secret)
	}
}

func assertRawEncrypted(t *testing.T, db *gorm.DB, table string, id uint, column, plaintext string) {
	t.Helper()
	var raw string
	if err := db.Table(table).Select(column).Where("id = ?", id).Scan(&raw).Error; err != nil {
		t.Fatalf("read raw %s.%s: %v", table, column, err)
	}
	if !strings.Contains(raw, "enc:v1:") {
		t.Fatalf("%s.%s is not encrypted: %q", table, column, raw)
	}
	if strings.Contains(raw, plaintext) {
		t.Fatalf("%s.%s still contains plaintext", table, column)
	}
}
