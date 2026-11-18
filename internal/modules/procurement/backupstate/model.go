package backupstate

import "time"

// Claim is a permanent unique reservation. No delete or reset is permitted:
// an ambiguous or completed purchase can never authorize another attempt.
type Claim struct {
	ID               uint        `gorm:"primaryKey"`
	LocalOrderID     uint        `gorm:"not null;uniqueIndex"`
	ProductID        uint        `gorm:"not null"`
	SKUID            uint        `gorm:"column:sku_id;not null"`
	PrimaryOrderID   uint        `gorm:"not null;default:0"`
	Product          Product     `gorm:"type:varchar(16);not null"`
	State            BackupState `gorm:"type:varchar(20);not null"`
	AdminID          uint        `gorm:"not null;default:0"`
	RequestSHA256    string      `gorm:"type:varchar(64);not null;default:''"`
	ConnectionSHA256 string      `gorm:"type:varchar(64);not null;default:''"`
	SnapshotJSON     string      `gorm:"type:text;not null;default:''" json:"-"`
	SubmittedAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (Claim) TableName() string { return "backup_procurement_claims" }

// Gate is the singleton, default-closed purchase switch shared by connection
// changes and backup submissions. It must never be automatically reopened.
type Gate struct {
	ID        uint `gorm:"primaryKey;autoIncrement:false"`
	Enabled   bool `gorm:"not null;default:false"`
	UpdatedAt time.Time
}

func (Gate) TableName() string { return "backup_procurement_gate" }

// GateAudit records administrative changes atomically with the gate state.
type GateAudit struct {
	ID        uint      `gorm:"primaryKey"`
	AdminID   uint      `gorm:"not null"`
	Action    string    `gorm:"type:varchar(16);not null"`
	Outcome   string    `gorm:"type:varchar(32);not null"`
	CreatedAt time.Time `gorm:"not null"`
}

func (GateAudit) TableName() string { return "backup_procurement_gate_audits" }
