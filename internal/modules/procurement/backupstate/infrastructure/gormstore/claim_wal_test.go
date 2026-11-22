package gormstore

import (
	"path/filepath"
	"sync"
	"testing"

	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWALSeparateConnectionsClaimOnceAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-claims.db")
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	open := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		return db
	}
	first := open()
	if err := first.AutoMigrate(&orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillmentdomain.Fulfillment{}, &procurementdomain.Order{}, &backupstate.Claim{}, &backupstate.Gate{}, &backupstate.GateAudit{}); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := first.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil || mode != "wal" {
		t.Fatalf("journal mode=%q err=%v", mode, err)
	}
	in := seedClaim(t, first, backupstate.Plus)
	const n = 8
	connections := make([]*gorm.DB, n)
	connections[0] = first
	for i := 1; i < n; i++ {
		connections[i] = open()
	}
	start := make(chan struct{})
	results := make(chan error, n)
	var workers sync.WaitGroup
	for _, db := range connections {
		workers.Add(1)
		go func(db *gorm.DB) { defer workers.Done(); <-start; _, err := claim(db, in); results <- err }(db)
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful claims=%d, want exactly one", succeeded)
	}
	for _, db := range connections {
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
	}
	reopened := open()
	t.Cleanup(func() {
		if db, err := reopened.DB(); err == nil {
			_ = db.Close()
		}
	})
	var claims []backupstate.Claim
	if err := reopened.Where("local_order_id = ?", in.orderID).Find(&claims).Error; err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].State != backupstate.Claimed || claims[0].ProductID != in.productID || claims[0].SKUID != in.skuID || claims[0].AdminID != in.adminID {
		t.Fatalf("unexpected persisted claims: %+v", claims)
	}
	if _, err := claim(reopened, in); err == nil {
		t.Fatal("reopened database accepted another claim")
	}
}
