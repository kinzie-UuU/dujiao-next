package contract

import (
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"time"
)

// ListFilter 是连接列表查询条件。
type ListFilter struct {
	Status   string
	Page     int
	PageSize int
}

// Repository 是连接持久化端口。
type Repository interface {
	GetByID(id uint) (*siteconnectiondomain.Connection, error)
	GetByApiKey(apiKey string) (*siteconnectiondomain.Connection, error)
	Create(conn *siteconnectiondomain.Connection) error
	Update(conn *siteconnectiondomain.Connection) error
	// RecordPing updates only health fields; it cannot overwrite credentials or
	// activate a Fang connection as a side effect of a read-only check.
	RecordPing(id uint, at time.Time, ok bool, activatePending bool) error
	Delete(id uint) error
	List(filter ListFilter) ([]siteconnectiondomain.Connection, int64, error)
	ListActive() ([]siteconnectiondomain.Connection, error)
}
