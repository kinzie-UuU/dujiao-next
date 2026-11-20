package gormstore

import (
	"encoding/json"
	"errors"

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
		// Historical Plus receipts remain readable, but no new Plus submission is authorized.
		return errors.New("backup purchase policy unavailable")
	default:
		return errors.New("backup purchase policy unavailable")
	}
	return nil
}
