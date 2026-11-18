package gormstore_test

import (
	"fmt"
	procurementstore "github.com/dujiao-next/internal/modules/procurement/infrastructure/gormstore"
	"testing"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBeginSubmissionBlocksAnyBackupClaim(t *testing.T) {
	for _, state := range []backupstate.BackupState{backupstate.Claimed, backupstate.Submitted, backupstate.Uncertain, backupstate.Completed} {
		t.Run(string(state), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:primary_fence_%s_%d?mode=memory&cache=shared", state, time.Now().UnixNano())), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&orderdomain.Order{}, &procurementdomain.Order{}, &backupstate.Claim{}); err != nil {
				t.Fatal(err)
			}
			order := orderdomain.Order{OrderNo: "test", Status: "paid", Currency: "CNY"}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			proc := procurementdomain.Order{LocalOrderID: order.ID, ConnectionID: 2, Status: "pending", SubmissionVersion: 1, Currency: "CNY"}
			if err := db.Create(&proc).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&backupstate.Claim{LocalOrderID: order.ID, Product: backupstate.Muse, State: state}).Error; err != nil {
				t.Fatal(err)
			}
			ok, err := procurementstore.New(db).BeginSubmission(proc.ID, "pending", "req", "conn", time.Now())
			if err == nil || ok {
				t.Fatalf("backup state %s: claimed=%v err=%v", state, ok, err)
			}
			if err := db.First(&proc, proc.ID).Error; err != nil {
				t.Fatal(err)
			}
			if proc.Status != "pending" || proc.SubmittedAt != nil {
				t.Fatal("blocked primary was mutated")
			}
		})
	}
}

func TestBeginSubmissionAllowsProvenNotSentBackup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:primary_notsent_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&orderdomain.Order{}, &procurementdomain.Order{}, &backupstate.Claim{}); err != nil {
		t.Fatal(err)
	}
	order := orderdomain.Order{OrderNo: "test", Status: "paid", Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	proc := procurementdomain.Order{LocalOrderID: order.ID, ConnectionID: 2, Status: "pending", SubmissionVersion: 1, Currency: "CNY"}
	if err := db.Create(&proc).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&backupstate.Claim{LocalOrderID: order.ID, Product: backupstate.Muse, State: backupstate.NotSent}).Error; err != nil {
		t.Fatal(err)
	}
	ok, err := procurementstore.New(db).BeginSubmission(proc.ID, "pending", "req", "conn", time.Now())
	if err != nil || !ok {
		t.Fatalf("claimed=%v err=%v", ok, err)
	}
}
