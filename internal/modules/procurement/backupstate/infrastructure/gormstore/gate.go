package gormstore

import (
	"errors"
	"time"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Gate ID 1 is the only supported row. Connection edits already share this
// fence. Enabling remains an explicit admin action; the default row stays closed.

var errGateClosed = errors.New("backup procurement gate is closed")
var errGatePolicy = errors.New("backup procurement gate change rejected")

// ChangeGate is the audited admin entry for opening or closing backup purchases.
func ChangeGate(db *gorm.DB, adminID uint, enable bool, now time.Time) error {
	return changeGate(db, adminID, enable, now)
}

// lockGate must be taken before all root/child/claim/connection locks in every
// transaction that might submit or change the gate. An absent gate is closed.
func lockGate(tx *gorm.DB) (*backupstate.Gate, error) {
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Model(&backupstate.Gate{}).Where("id = ?", 1).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return nil, err
		}
	}
	var g backupstate.Gate
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&g, 1).Error; err != nil {
		return nil, errGateClosed
	}
	return &g, nil
}

// changeGate writes the attempted change and its outcome in the same
// transaction. Enabling is allowed only when no in-flight claim exists and
// connection edits continue to refuse mutations while the gate is open.
func changeGate(db *gorm.DB, adminID uint, enable bool, now time.Time) error {
	if db == nil || adminID == 0 {
		return errGatePolicy
	}
	rejected := false
	err := db.Transaction(func(tx *gorm.DB) error {
		g, err := lockGate(tx)
		if err != nil {
			return err
		}
		action, outcome := "disable", "unchanged"
		var count int64
		if err := tx.Model(&backupstate.Claim{}).Where("NOT (state = ? AND submitted_at IS NULL AND request_sha256 = '' AND connection_sha256 = '') AND state <> ?", backupstate.NotSent, backupstate.Completed).Count(&count).Error; err != nil {
			return err
		}
		if enable {
			action = "enable"
			if count > 0 {
				outcome = "blocked_inflight"
				rejected = true
			} else if !g.Enabled {
				result := tx.Model(&backupstate.Gate{}).Where("id = ? AND enabled = ?", 1, false).Updates(map[string]interface{}{"enabled": true, "updated_at": now})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return errGatePolicy
				}
				outcome = "enabled"
			}
		} else if count > 0 {
			outcome = "blocked_inflight"
			rejected = true
		} else if g.Enabled {
			result := tx.Model(&backupstate.Gate{}).Where("id = ? AND enabled = ?", 1, true).Updates(map[string]interface{}{"enabled": false, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errGatePolicy
			}
			outcome = "disabled"
		}
		return tx.Create(&backupstate.GateAudit{AdminID: adminID, Action: action, Outcome: outcome, CreatedAt: now}).Error
	})
	if err != nil {
		return err
	}
	if rejected {
		return errGatePolicy
	}
	return nil
}
