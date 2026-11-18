package gormstore

import (
	"errors"
	"time"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	claimstore "github.com/dujiao-next/internal/modules/procurement/backupstate/infrastructure/gormstore"
	"gorm.io/gorm"
)

// GateAdmin exposes audited gate changes for the super-admin HTTP entry.
type GateAdmin struct {
	DB  *gorm.DB
	Now func() time.Time
}

func (g *GateAdmin) ChangeGate(adminID uint, enable bool) error {
	if g == nil || g.DB == nil || adminID == 0 {
		return errors.New("backup procurement gate unavailable")
	}
	now := time.Now()
	if g.Now != nil {
		now = g.Now()
	}
	return claimstore.ChangeGate(g.DB, adminID, enable, now)
}

func (g *GateAdmin) GateStatus() (enabled bool, err error) {
	if g == nil || g.DB == nil {
		return false, errors.New("backup procurement gate unavailable")
	}
	var gate backupstate.Gate
	if err := g.DB.First(&gate, 1).Error; err != nil {
		return false, errors.New("backup procurement gate unavailable")
	}
	return gate.Enabled, nil
}
