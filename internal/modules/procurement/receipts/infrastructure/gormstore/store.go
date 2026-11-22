package gormstore

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"

	securestoreserializer "github.com/dujiao-next/internal/securestore/serializer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

var ErrPurchaseReceiptExists = errors.New("purchase receipt already exists")
var purchaseAttemptHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PurchaseReceipt is an immutable encrypted HTTP response for one purchase attempt.
// AttemptHash identifies the attempt without exposing the idempotency key. Raw
// credentials are never placed on the model or passed to the database driver.
type PurchaseReceipt struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	OwnerKind          string    `gorm:"type:varchar(24);not null;uniqueIndex:idx_purchase_receipt_attempt" json:"owner_kind"`
	OwnerID            uint      `gorm:"not null;uniqueIndex:idx_purchase_receipt_attempt" json:"owner_id"`
	AttemptHash        string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_purchase_receipt_attempt" json:"-"`
	HTTPStatus         int       `gorm:"not null" json:"http_status"`
	ResponseCiphertext string    `gorm:"type:text;not null" json:"-"`
	CreatedAt          time.Time `gorm:"not null" json:"created_at"`
}

func (PurchaseReceipt) TableName() string { return "procurement_purchase_receipts" }

// SavePurchaseReceipt stores the complete response exactly once independently
// of procurement status. An existing attempt is never updated or overwritten.
// No error includes the response, cipher, attempt identifier, or database error.
func SavePurchaseReceipt(ctx context.Context, db *gorm.DB, ownerKind string, ownerID uint, attemptHash string, httpStatus int, raw []byte) error {
	if db == nil || !validPurchaseReceiptOwner(ownerKind, ownerID) || !purchaseAttemptHash.MatchString(attemptHash) || httpStatus < 100 || httpStatus > 599 {
		return errors.New("invalid purchase receipt")
	}
	codec, ok := schema.GetSerializer(securestoreserializer.StringSerializerName)
	if !ok {
		return errors.New("encrypted receipt storage unavailable")
	}
	// Nonempty envelope ensures even an empty HTTP body is encrypted.
	encoded := "v1:" + base64.StdEncoding.EncodeToString(raw)
	value, err := codec.Value(ctx, nil, reflect.Value{}, encoded)
	ciphertext, valid := value.(string)
	if err != nil || !valid || !securestoreserializer.IsEncryptedString(ciphertext) {
		return errors.New("encrypted receipt storage unavailable")
	}
	receipt := PurchaseReceipt{OwnerKind: ownerKind, OwnerID: ownerID, AttemptHash: attemptHash, HTTPStatus: httpStatus, ResponseCiphertext: ciphertext}
	result := db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_kind"}, {Name: "owner_id"}, {Name: "attempt_hash"}}, DoNothing: true}).Create(&receipt)
	if result.Error != nil {
		return errors.New("purchase receipt persistence failed")
	}
	if result.RowsAffected != 1 {
		return ErrPurchaseReceiptExists
	}
	return nil
}

// ReadPurchaseReceipt returns the private bytes only to trusted caller code.
// Callers must never log, expose, or marshal the returned response.
func ReadPurchaseReceipt(ctx context.Context, db *gorm.DB, ownerKind string, ownerID uint, attemptHash string) ([]byte, int, error) {
	if db == nil || !validPurchaseReceiptOwner(ownerKind, ownerID) || !purchaseAttemptHash.MatchString(attemptHash) {
		return nil, 0, errors.New("invalid purchase receipt")
	}
	var record PurchaseReceipt
	if err := db.WithContext(ctx).Where("owner_kind = ? AND owner_id = ? AND attempt_hash = ?", ownerKind, ownerID, attemptHash).Take(&record).Error; err != nil {
		return nil, 0, errors.New("purchase receipt unavailable")
	}
	if !securestoreserializer.IsEncryptedString(record.ResponseCiphertext) {
		return nil, 0, errors.New("invalid encrypted purchase receipt")
	}
	codec, ok := schema.GetSerializer(securestoreserializer.StringSerializerName)
	if !ok {
		return nil, 0, errors.New("encrypted receipt storage unavailable")
	}
	type responseHolder struct {
		Response string `gorm:"serializer:secure_string"`
	}
	var holder responseHolder
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&holder); err != nil {
		return nil, 0, errors.New("purchase receipt decoding failed")
	}
	field := stmt.Schema.LookUpField("Response")
	if field == nil || codec.Scan(ctx, field, reflect.ValueOf(&holder).Elem(), record.ResponseCiphertext) != nil || !strings.HasPrefix(holder.Response, "v1:") {
		return nil, 0, errors.New("purchase receipt decoding failed")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(holder.Response, "v1:"))
	if err != nil {
		return nil, 0, errors.New("purchase receipt decoding failed")
	}
	return raw, record.HTTPStatus, nil
}

func validPurchaseReceiptOwner(kind string, id uint) bool {
	return id != 0 && (kind == "procurement" || kind == "backup_claim")
}
