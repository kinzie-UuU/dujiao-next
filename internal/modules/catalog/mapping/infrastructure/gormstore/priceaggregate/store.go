// Package priceaggregate is an isolated, unregistered transactional prototype.
// Other existing writers do not yet share its fence: it must not be wired into
// a running catalog until all price writers participate in the same protocol.
package priceaggregate

import (
	"errors"
	"time"

	"github.com/dujiao-next/internal/constants"
	mappingapp "github.com/dujiao-next/internal/modules/catalog/mapping/application"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	connectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRecalculate = errors.New("pricing snapshot changed; recalculate")
var ErrInvalidSnapshot = errors.New("invalid pricing snapshot")

// FXQuote is an isolated prototype model and is not registered for migration.
// A production rate source must validate origin, currency and freshness before
// storing a monotonically versioned quote; no such source is wired here.
type FXQuote struct {
	ID         uint      `gorm:"primaryKey"`
	Version    uint64    `gorm:"not null"`
	Base       string    `gorm:"not null"`
	Quote      string    `gorm:"not null"`
	Rate       string    `gorm:"not null"`
	Source     string    `gorm:"not null"`
	ObservedAt time.Time `gorm:"not null"`
}

func (FXQuote) TableName() string { return "backup_pricing_fx_quotes" }

// Input must originate from a trusted pricing snapshot, never a customer request.
// Model snapshots are compared again under database locks before calculating.
type Input struct {
	Product             productdomain.Product
	SKU                 productdomain.ProductSKU
	Mapping             mappingdomain.Mapping
	Primary             mappingdomain.SKUMapping
	Connection          connectiondomain.Connection
	BackupConnection    connectiondomain.Connection
	Backup              mappingdomain.ReadOnlyBinding
	FX                  FXQuote
	BackupMarkupPercent decimal.Decimal
	MinimumPrice        decimal.Decimal
	PrimaryMaxAge       time.Duration
	BackupMaxAge        time.Duration
	FXMaxAge            time.Duration
}

type Store struct {
	DB  *gorm.DB
	now func() time.Time
}

func New(db *gorm.DB) *Store { return &Store{DB: db, now: time.Now} }

// Apply updates only price_amount on the verified single-SKU product and SKU,
// atomically. On any changed source or old price the transaction rolls back.
func (s *Store) Apply(in Input) (decimal.Decimal, error) {
	if s == nil || s.DB == nil || s.now == nil || s.DB.Dialector.Name() != "sqlite" || in.Product.ID == 0 || in.SKU.ID == 0 || in.Mapping.ID == 0 || in.Primary.ID == 0 || in.Connection.ID == 0 || in.BackupConnection.ID == 0 || in.Backup.ID == 0 || in.FX.ID == 0 || in.FX.Version == 0 || in.Product.UpdatedAt.IsZero() || in.SKU.UpdatedAt.IsZero() || in.Mapping.UpdatedAt.IsZero() || in.Primary.UpdatedAt.IsZero() || in.Backup.UpdatedAt.IsZero() || in.Connection.UpdatedAt.IsZero() || in.BackupConnection.UpdatedAt.IsZero() || in.PrimaryMaxAge <= 0 || in.BackupMaxAge <= 0 || in.FXMaxAge <= 0 {
		return decimal.Zero, ErrInvalidSnapshot
	}
	var target decimal.Decimal
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// SQLite has no FOR UPDATE. A harmless write starts the sole writer
		// before any snapshot reads. This prototype rejects other DB engines.
		if tx.Dialector.Name() == "sqlite" {
			q := tx.Model(&productdomain.Product{}).Where("id = ? AND deleted_at IS NULL", in.Product.ID).UpdateColumn("updated_at", gorm.Expr("updated_at"))
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return ErrRecalculate
			}
		}
		lock := func(model any, id uint) error {
			return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(model, id).Error
		}
		var product productdomain.Product
		var sku productdomain.ProductSKU
		var mapping mappingdomain.Mapping
		var primary mappingdomain.SKUMapping
		var conn connectiondomain.Connection
		var backupConn connectiondomain.Connection
		var backup mappingdomain.ReadOnlyBinding
		var fx FXQuote
		for _, row := range []struct {
			dest any
			id   uint
		}{{&product, in.Product.ID}, {&sku, in.SKU.ID}, {&mapping, in.Mapping.ID}, {&primary, in.Primary.ID}, {&conn, in.Connection.ID}, {&backupConn, in.BackupConnection.ID}, {&backup, in.Backup.ID}, {&fx, in.FX.ID}} {
			if err := lock(row.dest, row.id); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrRecalculate
				}
				return err
			}
		}
		if product.DeletedAt != nil || sku.DeletedAt != nil || mapping.DeletedAt != nil || primary.DeletedAt != nil || conn.DeletedAt != nil || !product.UpdatedAt.Equal(in.Product.UpdatedAt) || !sku.UpdatedAt.Equal(in.SKU.UpdatedAt) || !product.PriceAmount.Decimal.Equal(in.Product.PriceAmount.Decimal) || !sku.PriceAmount.Decimal.Equal(in.SKU.PriceAmount.Decimal) || !mapping.UpdatedAt.Equal(in.Mapping.UpdatedAt) || !primary.UpdatedAt.Equal(in.Primary.UpdatedAt) || !primary.UpstreamPrice.Decimal.Equal(in.Primary.UpstreamPrice.Decimal) || primary.UpstreamStock != in.Primary.UpstreamStock || primary.UpstreamIsActive != in.Primary.UpstreamIsActive || !sameTimePtr(primary.StockSyncedAt, in.Primary.StockSyncedAt) || !conn.UpdatedAt.Equal(in.Connection.UpdatedAt) || !conn.ExchangeRate.Equal(in.Connection.ExchangeRate) || !conn.PriceMarkupPercent.Equal(in.Connection.PriceMarkupPercent) || conn.PriceRoundingMode != in.Connection.PriceRoundingMode || !backup.UpdatedAt.Equal(in.Backup.UpdatedAt) || !backup.QuotedAt.Equal(in.Backup.QuotedAt) || backup.UpstreamPrice != in.Backup.UpstreamPrice || backup.UpstreamStock != in.Backup.UpstreamStock || backup.Currency != in.Backup.Currency || fx.ID != in.FX.ID || fx.Version != in.FX.Version || fx.Base != in.FX.Base || fx.Quote != in.FX.Quote || fx.Rate != in.FX.Rate || fx.Source != in.FX.Source || !fx.ObservedAt.Equal(in.FX.ObservedAt) {
			return ErrRecalculate
		}
		if backupConn.DeletedAt != nil || !backupConn.UpdatedAt.Equal(in.BackupConnection.UpdatedAt) || backupConn.Status != in.BackupConnection.Status || backupConn.Protocol != in.BackupConnection.Protocol || backupConn.BaseURL != in.BackupConnection.BaseURL || conn.Status != in.Connection.Status || conn.Protocol != in.Connection.Protocol {
			return ErrRecalculate
		}
		if mapping.ConnectionID != in.Mapping.ConnectionID || mapping.LocalProductID != in.Mapping.LocalProductID || mapping.UpstreamProductID != in.Mapping.UpstreamProductID || mapping.IsActive != in.Mapping.IsActive || mapping.UpstreamStatus != in.Mapping.UpstreamStatus || primary.ProductMappingID != in.Primary.ProductMappingID || primary.LocalSKUID != in.Primary.LocalSKUID || primary.UpstreamSKUID != in.Primary.UpstreamSKUID || backup.LocalProductID != in.Backup.LocalProductID || backup.LocalSKUID != in.Backup.LocalSKUID || backup.ConnectionID != in.Backup.ConnectionID {
			return ErrRecalculate
		}
		if !sku.IsActive || sku.ProductID != product.ID || !mapping.IsActive || mapping.LocalProductID != product.ID || mapping.ConnectionID != conn.ID || conn.Protocol != constants.ConnectionProtocolNewzoe || conn.Status != constants.ConnectionStatusActive || primary.ProductMappingID != mapping.ID || primary.LocalSKUID != sku.ID || !primary.UpstreamIsActive || primary.StockSyncedAt == nil || backup.LocalProductID != product.ID || backup.LocalSKUID != sku.ID || backup.ConnectionID != backupConn.ID || backupConn.Protocol != constants.ConnectionProtocolFangfang || backupConn.Status != constants.ConnectionStatusActive || backupConn.BaseURL != "https://canboso.com" || backup.UpstreamProductID != in.Backup.UpstreamProductID || backup.UpstreamSKUCode != in.Backup.UpstreamSKUCode || backup.Currency != "USD" || fx.Base != "USD" || fx.Quote != "CNY" || fx.Source == "" || conn.ExchangeRate.LessThanOrEqual(decimal.Zero) || !product.PriceAmount.Decimal.Equal(sku.PriceAmount.Decimal) {
			return ErrInvalidSnapshot
		}
		// Product price equals a minimum over all active SKUs. The prototype only
		// handles a product with exactly one active SKU; no guessed roll-up.
		var skuCount int64
		if err := tx.Model(&productdomain.ProductSKU{}).Where("product_id = ? AND deleted_at IS NULL AND is_active = ?", product.ID, true).Count(&skuCount).Error; err != nil {
			return err
		}
		if skuCount != 1 {
			return ErrInvalidSnapshot
		}
		rate, err := decimal.NewFromString(fx.Rate)
		if err != nil || !rate.IsPositive() {
			return ErrInvalidSnapshot
		}
		backupPrice, err := decimal.NewFromString(backup.UpstreamPrice)
		if err != nil || !backupPrice.IsPositive() {
			return ErrInvalidSnapshot
		}
		primaryPrice := mappingapp.CalculateLocalPrice(primary.UpstreamPrice.Decimal, conn.ExchangeRate, conn.PriceMarkupPercent, conn.PriceRoundingMode)
		now := s.now().UTC()
		target, _, err = mappingapp.CalculateAggregatedQuotePrice(mappingapp.AggregatedQuotePricingInput{PrimaryPrice: primaryPrice, PrimaryQuotedAt: *primary.StockSyncedAt, PrimaryMaxAge: in.PrimaryMaxAge, ExchangeRateAt: fx.ObservedAt, ExchangeRateMaxAge: in.FXMaxAge, Backup: mappingapp.BackupQuotePricingInput{CurrentPrice: sku.PriceAmount.Decimal, SupplierPrice: backupPrice, ExchangeRate: rate, MarkupPercent: in.BackupMarkupPercent, MinimumPrice: in.MinimumPrice, Stock: backup.UpstreamStock, QuotedAt: backup.QuotedAt, Now: now, MaxAge: in.BackupMaxAge}})
		if err != nil || !target.IsPositive() {
			return ErrInvalidSnapshot
		}
		if target.Equal(sku.PriceAmount.Decimal) {
			return nil
		}
		update := func(model any, id uint, old decimal.Decimal, updated time.Time) error {
			q := tx.Model(model).Where("id = ? AND deleted_at IS NULL AND price_amount = ? AND updated_at = ?", id, old.StringFixed(2), updated).Update("price_amount", target.StringFixed(2))
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return ErrRecalculate
			}
			return nil
		}
		if err := update(&productdomain.ProductSKU{}, sku.ID, in.SKU.PriceAmount.Decimal, in.SKU.UpdatedAt); err != nil {
			return err
		}
		return update(&productdomain.Product{}, product.ID, in.Product.PriceAmount.Decimal, in.Product.UpdatedAt)
	})
	if err != nil {
		var sqliteError interface{ Code() int }
		if errors.As(err, &sqliteError) && (sqliteError.Code()&0xff == 5 || sqliteError.Code()&0xff == 6) {
			return decimal.Zero, ErrRecalculate
		}
		return decimal.Zero, err
	}
	return target, nil
}

func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
