package gormstore

import (
	"github.com/dujiao-next/internal/modules/auditlog/domain"
	"gorm.io/gorm"
)

type OperationStore struct{ db *gorm.DB }

func NewOperationStore(db *gorm.DB) *OperationStore { return &OperationStore{db: db} }
func (s *OperationStore) RecordOperation(event *domain.AdminOperationLog) error {
	return s.db.Create(event).Error
}
