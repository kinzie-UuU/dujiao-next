package gormstore

import (
	"errors"
	"strings"

	downstreamcontract "github.com/dujiao-next/internal/modules/downstreamcallback/contract"
	downstreamdomain "github.com/dujiao-next/internal/modules/downstreamcallback/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 是下游回调引用的 GORM 仓储。
type Store struct {
	db *gorm.DB
}

var _ downstreamcontract.Repository = (*Store)(nil)

// New 创建下游回调引用仓储。
func New(db *gorm.DB) *Store {
	if db == nil {
		panic("downstream callback store: db is nil")
	}
	return &Store{db: db}
}

func (s *Store) GetByID(id uint) (*downstreamdomain.OrderRef, error) {
	var ref downstreamdomain.OrderRef
	if err := s.db.First(&ref, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &ref, nil
}

func (s *Store) GetByOrderID(orderID uint) (*downstreamdomain.OrderRef, error) {
	var ref downstreamdomain.OrderRef
	if err := s.db.Where("order_id = ?", orderID).First(&ref).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &ref, nil
}

func (s *Store) GetByCredentialAndDownstreamNo(credentialID uint, downstreamOrderNo string) (*downstreamdomain.OrderRef, error) {
	if credentialID == 0 || downstreamOrderNo == "" {
		return nil, nil
	}
	var ref downstreamdomain.OrderRef
	if err := s.db.Where("api_credential_id = ? AND downstream_order_no = ?", credentialID, downstreamOrderNo).First(&ref).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &ref, nil
}

func (s *Store) Create(ref *downstreamdomain.OrderRef) error {
	if ref == nil || ref.OrderID == 0 || ref.ApiCredentialID == 0 {
		return downstreamcontract.ErrInvalidRef
	}
	ref.DownstreamOrderNo = strings.TrimSpace(ref.DownstreamOrderNo)
	if ref.DownstreamOrderNo == "" {
		return s.db.Create(ref).Error
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Lock the existing credential even when its first order reference does
		// not exist yet. PostgreSQL/MySQL serialize writers on this row; SQLite
		// serializes writers and rejects stale snapshot upgrades.
		if tx.Dialector.Name() == "sqlite" {
			// SQLite ignores FOR UPDATE. Acquire its write reservation before
			// checking references; busy/failed acquisition must fail closed.
			locked := tx.Exec("UPDATE api_credentials SET id = id WHERE id = ? AND deleted_at IS NULL", ref.ApiCredentialID)
			if locked.Error != nil {
				return locked.Error
			}
			if locked.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		var credential struct{ ID uint }
		if err := tx.Table("api_credentials").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", ref.ApiCredentialID).Take(&credential).Error; err != nil {
			return err
		}
		var existing downstreamdomain.OrderRef
		err := tx.Where("api_credential_id = ? AND downstream_order_no = ?", ref.ApiCredentialID, ref.DownstreamOrderNo).First(&existing).Error
		if err == nil {
			return downstreamcontract.ErrDuplicateRef
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(ref).Error
	})
}

func (s *Store) Update(ref *downstreamdomain.OrderRef) error {
	return s.db.Save(ref).Error
}

func (s *Store) ListPendingCallbacks(limit int) ([]downstreamdomain.OrderRef, error) {
	var refs []downstreamdomain.OrderRef
	query := s.db.Where("callback_status = ? AND callback_url != ''", downstreamdomain.StatusPending).
		Order("created_at ASC").
		Limit(limit)
	if err := query.Find(&refs).Error; err != nil {
		return nil, err
	}
	return refs, nil
}

func (s *Store) ListByCredentialID(credentialID uint, filter downstreamcontract.RefListFilter) ([]downstreamdomain.OrderRef, int64, error) {
	var refs []downstreamdomain.OrderRef
	var total int64

	query := s.db.Model(&downstreamdomain.OrderRef{}).Where("api_credential_id = ?", credentialID)
	if filter.CallbackStatus != "" {
		query = query.Where("callback_status = ?", filter.CallbackStatus)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = query.Order("created_at DESC")
	if filter.Page > 0 && filter.PageSize > 0 {
		query = query.Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize)
	}
	if err := query.Find(&refs).Error; err != nil {
		return nil, 0, err
	}
	return refs, total, nil
}
