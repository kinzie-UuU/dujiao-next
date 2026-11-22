package domain

import (
	"github.com/dujiao-next/internal/shared/jsonmap"
	"time"
)

// AdminOperationLog contains allowlisted observations, not raw business payloads.
// Snapshots are outside the business transaction and may observe concurrent changes.
type AdminOperationLog struct {
	ID               uint         `gorm:"primarykey"`
	OperatorAdminID  uint         `gorm:"index;not null"`
	OperatorUsername string       `gorm:"type:varchar(100)"`
	ClientIP         string       `gorm:"type:varchar(45);index"`
	RequestID        string       `gorm:"type:varchar(64);index"`
	Action           string       `gorm:"type:varchar(100);index"`
	Target           string       `gorm:"type:varchar(160);index"`
	Result           string       `gorm:"type:varchar(32);index"`
	BeforeJSON       jsonmap.JSON `gorm:"type:json"`
	AfterJSON        jsonmap.JSON `gorm:"type:json"`
	CreatedAt        time.Time    `gorm:"index"`
}

func (AdminOperationLog) TableName() string { return "admin_operation_audit_logs" }
