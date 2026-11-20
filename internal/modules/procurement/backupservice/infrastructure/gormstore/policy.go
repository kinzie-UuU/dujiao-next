package gormstore

import (
	"encoding/json"
	"errors"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"gorm.io/gorm"
)

// StructuralPolicy re-checks local product identity under the begin transaction.
// FX and markup approval belong in the Muse preflight path before claim.
func StructuralPolicy(tx *gorm.DB, claimID uint) error {
	if tx == nil || claimID == 0 {
		return errors.New("backup purchase policy unavailable")
	}
	var c backupstate.Claim
	if err := tx.First(&c, claimID).Error; err != nil {
		return errors.New("backup purchase policy unavailable")
	}
	var snap struct {
		Version           int    `json:"version"`
		LocalProductID    uint   `json:"local_product_id"`
		LocalSKUID        uint   `json:"local_sku_id"`
		UpstreamProductID string `json:"upstream_product_id"`
		Quantity          int    `json:"quantity"`
	}
	if json.Unmarshal([]byte(c.SnapshotJSON), &snap) != nil || snap.Version != 1 || snap.Quantity != 1 {
		return errors.New("backup purchase policy unavailable")
	}
	switch c.Product {
	case backupstate.Muse:
		if c.ProductID != backupstate.LocalMuseProductID || c.SKUID != backupstate.LocalMuseSKUID || snap.LocalProductID != backupstate.LocalMuseProductID || snap.LocalSKUID != backupstate.LocalMuseSKUID || snap.UpstreamProductID != backupstate.UpstreamMuseProductID {
			return errors.New("backup purchase policy unavailable")
		}
	case backupstate.Gmail:
		if c.ProductID != backupstate.LocalGmailProductID || c.SKUID != backupstate.LocalGmailSKUID || snap.LocalProductID != backupstate.LocalGmailProductID || snap.LocalSKUID != backupstate.LocalGmailSKUID || snap.UpstreamProductID != backupstate.UpstreamGmailProductID {
			return errors.New("backup purchase policy unavailable")
		}
	case backupstate.Plus:
		if c.ProductID != backupstate.LocalPlusProductID || c.SKUID != backupstate.LocalPlusSKUID || snap.LocalProductID != backupstate.LocalPlusProductID || snap.LocalSKUID != backupstate.LocalPlusSKUID || snap.UpstreamProductID != backupstate.UpstreamPlusProductID {
			return errors.New("backup purchase policy unavailable")
		}
		eligible, err := plusPaymentAfterBinding(tx, c.LocalOrderID)
		if err != nil || !eligible {
			return errors.New("backup purchase policy unavailable")
		}
	default:
		return errors.New("backup purchase policy unavailable")
	}
	return nil
}

// A new Plus binding is the activation boundary. Old paid orders must never
// become purchases when callbacks, queued jobs, or recovery are replayed.
// Receipt recovery bypasses Begin and retains the original submitted purchase.
func plusPaymentAfterBinding(db *gorm.DB, orderID uint) (bool, error) {
	var order orderdomain.Order
	if err := db.First(&order, orderID).Error; err != nil {
		return false, err
	}
	var bindings []mappingdomain.ReadOnlyBinding
	if err := db.Where("local_product_id = ? AND local_sku_id = ?", backupstate.LocalPlusProductID, backupstate.LocalPlusSKUID).Find(&bindings).Error; err != nil {
		return false, err
	}
	if len(bindings) != 1 || bindings[0].UpstreamProductID != 2 || bindings[0].UpstreamSKUCode != backupstate.UpstreamPlusProductID || bindings[0].CreatedAt.IsZero() || order.PaidAt == nil {
		return false, nil
	}
	return order.PaidAt.After(bindings[0].CreatedAt), nil
}
