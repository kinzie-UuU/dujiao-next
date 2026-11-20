package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	claimstore "github.com/dujiao-next/internal/modules/procurement/backupstate/infrastructure/gormstore"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"testing"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"github.com/dujiao-next/internal/queue"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type enqueueStub struct {
	payloads []queue.BackupProcurementSubmitPayload
	err      error
}

func (s *enqueueStub) EnqueueBackupProcurementSubmit(payload queue.BackupProcurementSubmitPayload) error {
	s.payloads = append(s.payloads, payload)
	return s.err
}

func triggerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:paid_trigger_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&orderdomain.Order{}, &orderdomain.OrderItem{}, &backupstate.Claim{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPaidTriggerIgnoresUnpinnedOrders(t *testing.T) {
	db := triggerDB(t)
	order := orderdomain.Order{OrderNo: "other", Status: "paid", Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orderdomain.OrderItem{OrderID: order.ID, ProductID: 1, SKUID: 2, Quantity: 1, FulfillmentType: "manual", TitleJSON: map[string]interface{}{"zh-CN": "x"}}).Error; err != nil {
		t.Fatal(err)
	}
	stub := &enqueueStub{}
	if err := (&PaidTrigger{DB: db, Queue: stub}).OnOrderPaid(order.ID); err != nil {
		t.Fatal(err)
	}
	if len(stub.payloads) != 0 {
		t.Fatalf("unexpected enqueue: %+v", stub.payloads)
	}
}

func TestPaidGmailUsesDistinctSnapshotAndBlocksOldPlus(t *testing.T) {
	db := triggerDB(t)
	if err := db.AutoMigrate(&mappingdomain.ReadOnlyBinding{}, &siteconnectiondomain.Connection{}, &fulfillmentdomain.Fulfillment{}, &procurementdomain.Order{}, &backupstate.Gate{}); err != nil {
		t.Fatal(err)
	}
	order := orderdomain.Order{OrderNo: "gmail-paid", Status: "paid", Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	rows := []any{
		&orderdomain.OrderItem{OrderID: order.ID, ProductID: 51, SKUID: 86, Quantity: 1, FulfillmentType: "manual", TitleJSON: map[string]interface{}{"zh-CN": "Gmail老号"}},
		&siteconnectiondomain.Connection{ID: 4, Name: "Fang", BaseURL: "https://canboso.com", Protocol: constants.ConnectionProtocolFangfang, Status: constants.ConnectionStatusActive, ApiKey: "marker", ApiSecret: "cipher"},
		&mappingdomain.ReadOnlyBinding{ConnectionID: 4, LocalProductID: 51, LocalSKUID: 86, UpstreamProductID: 3, UpstreamSKUCode: backupstate.UpstreamGmailProductID, Currency: "USD"},
		&backupstate.Gate{ID: 1, Enabled: true},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	queue := &enqueueStub{}
	trigger := &PaidTrigger{DB: db, Queue: queue}
	if err := trigger.OnOrderPaid(order.ID); err != nil {
		t.Fatal(err)
	}
	if err := trigger.OnOrderPaid(order.ID); err != nil {
		t.Fatal(err)
	}
	var claims []backupstate.Claim
	if err := db.Find(&claims).Error; err != nil || len(claims) != 1 || claims[0].Product != backupstate.Gmail {
		t.Fatalf("wrong or duplicate claim: %+v %v", claims, err)
	}
	var snap struct {
		UpstreamProductID string `json:"upstream_product_id"`
		PreflightMaxUSD   string `json:"preflight_max_usd"`
	}
	if err := json.Unmarshal([]byte(claims[0].SnapshotJSON), &snap); err != nil || snap.UpstreamProductID != backupstate.UpstreamGmailProductID || snap.PreflightMaxUSD != "1.49" {
		t.Fatalf("wrong purchase snapshot %+v %v", snap, err)
	}
	attempt, started, err := claimstore.BeginBound(context.Background(), db, claims[0].ID, func(string) (string, error) { return "synthetic-key", nil }, StructuralPolicy, time.Now())
	if err != nil || !started || attempt.Request.ProductID != backupstate.UpstreamGmailProductID {
		t.Fatalf("correct Gmail begin failed: %v", err)
	}
	if _, started, err := claimstore.BeginBound(context.Background(), db, claims[0].ID, func(string) (string, error) { t.Fatal("duplicate decrypt"); return "", nil }, StructuralPolicy, time.Now()); err == nil || started {
		t.Fatal("duplicate begin accepted")
	}
	if err := db.Model(&backupstate.Claim{}).Where("id = ?", claims[0].ID).Updates(map[string]interface{}{"product": backupstate.Plus, "snapshot_json": fmt.Sprintf(`{"version":1,"quantity":1,"local_product_id":51,"local_sku_id":86,"upstream_product_id":%q}`, backupstate.UpstreamPlusProductID)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := StructuralPolicy(db, claims[0].ID); err == nil {
		t.Fatal("historical Plus allowed new submission")
	}
}

func TestPlusAutomaticOnlyForPaymentsAfterBinding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		offset   time.Duration
		missing  bool
		quantity int
		eligible bool
	}{{"old-paid", -time.Hour, false, 1, false}, {"exact-cutoff", 0, false, 1, false}, {"no-paid-time", time.Hour, true, 1, false}, {"new-paid", time.Hour, false, 1, true}, {"quantity-two", time.Hour, false, 2, false}} {
		t.Run(tc.name, func(t *testing.T) {
			db := triggerDB(t)
			if err := db.AutoMigrate(&mappingdomain.ReadOnlyBinding{}, &siteconnectiondomain.Connection{}, &fulfillmentdomain.Fulfillment{}, &procurementdomain.Order{}, &backupstate.Gate{}); err != nil {
				t.Fatal(err)
			}
			cutoff := time.Date(2026, 11, 20, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
			paid := cutoff.Add(tc.offset).UTC()
			order := orderdomain.Order{OrderNo: "PLUS-" + tc.name, Status: "paid", Currency: "CNY", PaidAt: &paid}
			if tc.missing {
				order.PaidAt = nil
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range []any{
				&orderdomain.OrderItem{OrderID: order.ID, ProductID: 41, SKUID: 74, Quantity: tc.quantity, FulfillmentType: "manual", TitleJSON: map[string]interface{}{"zh-CN": "Plus"}},
				&siteconnectiondomain.Connection{ID: 4, Name: "Fang", BaseURL: "https://canboso.com", Protocol: constants.ConnectionProtocolFangfang, Status: constants.ConnectionStatusActive, ApiKey: "marker", ApiSecret: "cipher"},
				&mappingdomain.ReadOnlyBinding{ConnectionID: 4, LocalProductID: 41, LocalSKUID: 74, UpstreamProductID: 2, UpstreamSKUCode: backupstate.UpstreamPlusProductID, Currency: "USD", CreatedAt: cutoff},
				&backupstate.Gate{ID: 1, Enabled: true},
			} {
				if err := db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			queue := &enqueueStub{}
			trigger := &PaidTrigger{DB: db, Queue: queue}
			for i := 0; i < 2; i++ {
				err := trigger.OnOrderPaid(order.ID)
				if tc.eligible && err != nil {
					t.Fatal(err)
				}
			}
			var claims []backupstate.Claim
			if err := db.Find(&claims).Error; err != nil {
				t.Fatal(err)
			}
			if !tc.eligible {
				if len(claims) != 0 || len(queue.payloads) != 0 {
					t.Fatal("historical/invalid order purchased")
				}
				return
			}
			if len(claims) != 1 || claims[0].Product != backupstate.Plus {
				t.Fatal("wrong or duplicate Plus claim")
			}
			var snap struct {
				UpstreamProductID string `json:"upstream_product_id"`
				PreflightMaxUSD   string `json:"preflight_max_usd"`
			}
			if json.Unmarshal([]byte(claims[0].SnapshotJSON), &snap) != nil || snap.UpstreamProductID != backupstate.UpstreamPlusProductID || snap.PreflightMaxUSD != "5.29" {
				t.Fatal("wrong Plus snapshot")
			}
			// A stale queued claim must also fail at Begin if paid-time eligibility is lost.
			if err := db.Model(&order).Update("paid_at", cutoff).Error; err != nil {
				t.Fatal(err)
			}
			if _, started, err := claimstore.BeginBound(context.Background(), db, claims[0].ID, func(string) (string, error) { t.Fatal("old payment reached decrypt"); return "", nil }, StructuralPolicy, time.Now()); err == nil || started {
				t.Fatal("old queued payment accepted")
			}
			if err := db.Model(&order).Update("paid_at", cutoff.Add(tc.offset).UTC()).Error; err != nil {
				t.Fatal(err)
			}
			attempt, started, err := claimstore.BeginBound(context.Background(), db, claims[0].ID, func(string) (string, error) { return "synthetic", nil }, StructuralPolicy, time.Now())
			if err != nil || !started || attempt.Request.ProductID != backupstate.UpstreamPlusProductID {
				t.Fatalf("Plus Begin=%v", err)
			}
			if _, started, err := claimstore.BeginBound(context.Background(), db, claims[0].ID, func(string) (string, error) { t.Fatal("repeat decrypt"); return "", nil }, StructuralPolicy, time.Now()); err == nil || started {
				t.Fatal("repeat purchase")
			}
		})
	}
}
