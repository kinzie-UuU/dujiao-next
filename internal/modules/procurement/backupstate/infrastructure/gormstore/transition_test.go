package gormstore

import (
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"strings"
	"testing"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
)

func TestTransitionsNeverRepeatPurchase(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	now := time.Now()
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, now); err != nil {
		t.Fatal(err)
	}
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, now); err == nil {
		t.Fatal("repeated submission allowed")
	}
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.NotSent, "", "", now); err == nil {
		t.Fatal("submitted released as not_sent")
	}
	if err := transition(db, c.ID, backupstate.Submitted, backupstate.Completed, "", "", now); err == nil {
		t.Fatal("completed without locally committed fulfillment")
	}
	if err := transition(db, c.ID, backupstate.Submitted, backupstate.Uncertain, "", "", now); err != nil {
		t.Fatal(err)
	}
	if err := transition(db, c.ID, backupstate.Uncertain, backupstate.Submitted, h, h, now); err == nil {
		t.Fatal("uncertain retry allowed")
	}
}
func TestNotSentPreservesUniqueClaim(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Muse)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.NotSent, "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := claim(db, in); err == nil {
		t.Fatal("terminal record allowed second claim")
	}
	if err := transition(db, c.ID, backupstate.NotSent, backupstate.Submitted, strings.Repeat("a", 64), strings.Repeat("a", 64), time.Now()); err == nil {
		t.Fatal("not_sent claim reused")
	}
}
func TestSubmissionRevalidatesOrder(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&orderdomain.Order{}).Where("id = ?", in.orderID).Update("status", "refunded").Error; err != nil {
		t.Fatal(err)
	}
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, strings.Repeat("a", 64), strings.Repeat("a", 64), time.Now()); err == nil {
		t.Fatal("refunded order submitted")
	}
}
func TestSubmitAndNotSentCannotBothWin(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	results := make(chan error, 2)
	go func() { results <- transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()) }()
	go func() { results <- transition(db, c.ID, backupstate.Claimed, backupstate.NotSent, "", "", time.Now()) }()
	wins := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful transitions = %d, want 1", wins)
	}
	var stored backupstate.Claim
	if err := db.First(&stored, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.State == backupstate.Submitted {
		if stored.SubmittedAt == nil || stored.RequestSHA256 != h || stored.ConnectionSHA256 != h {
			t.Fatal("submission fence incomplete")
		}
	} else if stored.State == backupstate.NotSent {
		if stored.SubmittedAt != nil || stored.RequestSHA256 != "" || stored.ConnectionSHA256 != "" {
			t.Fatal("not_sent retained submission evidence")
		}
	} else {
		t.Fatalf("invalid final state %s", stored.State)
	}
}
