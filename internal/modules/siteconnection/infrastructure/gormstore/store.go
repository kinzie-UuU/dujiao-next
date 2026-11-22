package gormstore

import (
	"errors"
	"time"

	"github.com/dujiao-next/internal/constants"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	siteconnectioncontract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 是 SiteConnection 的 GORM 持久化实现。
type Store struct {
	db *gorm.DB
}

var _ siteconnectioncontract.Repository = (*Store)(nil)

// New 创建连接仓库。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// GetByID 根据 ID 获取
func (r *Store) GetByID(id uint) (*siteconnectiondomain.Connection, error) {
	var conn siteconnectiondomain.Connection
	if err := r.db.Where("deleted_at IS NULL").First(&conn, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &conn, nil
}

// GetByApiKey 根据 ApiKey 获取连接
func (r *Store) GetByApiKey(apiKey string) (*siteconnectiondomain.Connection, error) {
	var conn siteconnectiondomain.Connection
	if err := r.db.Where("deleted_at IS NULL AND api_key = ?", apiKey).First(&conn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &conn, nil
}

// Create 创建连接
func (r *Store) Create(conn *siteconnectiondomain.Connection) error {
	return r.db.Create(conn).Error
}

// RecordPing never re-saves a stale credential-bearing connection. Its only
// status transition preserves the existing pending behavior for non-Fang sites.
func (r *Store) RecordPing(id uint, at time.Time, ok bool, activatePending bool) error {
	return r.db.Model(&siteconnectiondomain.Connection{}).Where("id = ? AND deleted_at IS NULL", id).Updates(map[string]interface{}{
		"last_ping_at": at,
		"last_ping_ok": ok,
		"status":       gorm.Expr("CASE WHEN status = ? AND protocol <> ? AND ? THEN ? ELSE status END", constants.ConnectionStatusPending, constants.ConnectionProtocolFangfang, activatePending, constants.ConnectionStatusActive),
	}).Error
}

// Update 更新连接
func (r *Store) Update(conn *siteconnectiondomain.Connection) error {
	if conn == nil || conn.ID == 0 {
		return errors.New("invalid connection")
	}
	var current siteconnectiondomain.Connection
	if err := r.db.Select("id", "protocol").Where("deleted_at IS NULL").First(&current, conn.ID).Error; err != nil {
		return err
	}
	if current.Protocol != constants.ConnectionProtocolFangfang && conn.Protocol != constants.ConnectionProtocolFangfang {
		// A concurrent edit may have converted this row to Fang after our read.
		// The conditional write cannot overwrite such a protected connection.
		result := r.db.Model(&siteconnectiondomain.Connection{}).Where("id = ? AND deleted_at IS NULL AND protocol <> ?", conn.ID, constants.ConnectionProtocolFangfang).Select("*").Updates(conn)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("connection changed concurrently")
		}
		return nil
	}
	return r.withBackupConnectionFence(conn.ID, func(tx *gorm.DB) error {
		result := tx.Model(&siteconnectiondomain.Connection{}).Where("id = ? AND deleted_at IS NULL", conn.ID).Select("*").Updates(conn)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("connection changed concurrently")
		}
		return nil
	})
}

// Delete 软删除连接
func (r *Store) Delete(id uint) error {
	var current siteconnectiondomain.Connection
	if err := r.db.Select("id", "protocol").Where("deleted_at IS NULL").First(&current, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if current.Protocol != constants.ConnectionProtocolFangfang {
		result := r.db.Model(&siteconnectiondomain.Connection{}).Where("id = ? AND deleted_at IS NULL AND protocol <> ?", id, constants.ConnectionProtocolFangfang).Update("deleted_at", time.Now())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("connection changed concurrently")
		}
		return nil
	}
	return r.withBackupConnectionFence(id, func(tx *gorm.DB) error {
		return tx.Model(&siteconnectiondomain.Connection{}).Where("id = ? AND deleted_at IS NULL", id).Update("deleted_at", time.Now()).Error
	})
}

// Connection changes and supplier submissions serialize on the same gate row.
// Reject edits while the gate is open or any claim could still reach a supplier.
func (r *Store) withBackupConnectionFence(id uint, apply func(*gorm.DB) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&backupstate.Gate{}).Where("id = ?", 1).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
		}
		var gate backupstate.Gate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&gate, 1).Error; err != nil {
			return errors.New("backup purchase gate unavailable")
		}
		var locked siteconnectiondomain.Connection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("deleted_at IS NULL").First(&locked, id).Error; err != nil {
			return err
		}
		if gate.Enabled {
			return errors.New("disable backup purchasing before editing connection")
		}
		var count int64
		if err := tx.Model(&backupstate.Claim{}).Where("state <> ? AND NOT (state = ? AND submitted_at IS NULL AND request_sha256 = '' AND connection_sha256 = '')", backupstate.Completed, backupstate.NotSent).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errors.New("backup purchase requires reconciliation before editing connection")
		}
		return apply(tx)
	})
}

// List 列表查询
func (r *Store) List(filter siteconnectioncontract.ListFilter) ([]siteconnectiondomain.Connection, int64, error) {
	var conns []siteconnectiondomain.Connection
	var total int64

	q := r.db.Model(&siteconnectiondomain.Connection{}).Where("deleted_at IS NULL")
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}

	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	q = q.Order("created_at DESC")
	if filter.Page > 0 && filter.PageSize > 0 {
		q = q.Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize)
	}

	if err := q.Find(&conns).Error; err != nil {
		return nil, 0, err
	}

	return conns, total, nil
}

// ListActive 获取所有启用的连接
func (r *Store) ListActive() ([]siteconnectiondomain.Connection, error) {
	var conns []siteconnectiondomain.Connection
	if err := r.db.Where("deleted_at IS NULL AND status = ?", "active").Find(&conns).Error; err != nil {
		return nil, err
	}
	return conns, nil
}
