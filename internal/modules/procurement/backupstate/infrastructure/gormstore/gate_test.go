package gormstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
)

func TestGateDefaultsClosedAndEnableAudits(t *testing.T) {
	db := claimDB(t)
	if err := db.Model(&backupstate.Gate{}).Where("id = ?", 1).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := changeGate(db, 17, true, time.Now()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	var gate backupstate.Gate
	if err := db.First(&gate, 1).Error; err != nil || !gate.Enabled {
		t.Fatal("gate not opened")
	}
	var audit backupstate.GateAudit
	if err := db.Last(&audit).Error; err != nil || audit.AdminID != 17 || audit.Action != "enable" || audit.Outcome != "enabled" {
		t.Fatalf("missing audit: %v", err)
	}
	if err := changeGate(db, 17, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()); !errors.Is(err, errGateClosed) {
		t.Fatalf("closed gate submitted: %v", err)
	}
}
func TestDisableRefusesInFlightAndAudits(t *testing.T) {
	db := claimDB(t)
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := changeGate(db, 22, false, time.Now()); !errors.Is(err, errGatePolicy) {
		t.Fatalf("disabled in-flight claim: %v", err)
	}
	var g backupstate.Gate
	if err := db.First(&g, 1).Error; err != nil || !g.Enabled {
		t.Fatal("in-flight disable altered gate")
	}
	var audit backupstate.GateAudit
	if err := db.Last(&audit).Error; err != nil || audit.Outcome != "blocked_inflight" {
		t.Fatalf("audit missing: %v", err)
	}
}
func TestDisablePreservesAuditOnActualChange(t *testing.T) {
	db := claimDB(t)
	if err := changeGate(db, 22, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	var g backupstate.Gate
	if err := db.First(&g, 1).Error; err != nil || g.Enabled {
		t.Fatal("disable failed")
	}
	var audit backupstate.GateAudit
	if err := db.Last(&audit).Error; err != nil || audit.Outcome != "disabled" {
		t.Fatalf("audit missing: %v", err)
	}
}
func TestMissingGateAlwaysFailsClosed(t *testing.T) {
	db := claimDB(t)
	if err := db.Where("id = ?", 1).Delete(&backupstate.Gate{}).Error; err != nil {
		t.Fatal(err)
	}
	in := seedClaim(t, db, backupstate.Plus)
	c, err := claim(db, in)
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	if err := transition(db, c.ID, backupstate.Claimed, backupstate.Submitted, h, h, time.Now()); !errors.Is(err, errGateClosed) {
		t.Fatalf("missing gate submitted: %v", err)
	}
}

func TestDisableRollsBackWhenAuditCannotBeWritten(t *testing.T) {
	db := claimDB(t)
	if err := db.Migrator().DropTable(&backupstate.GateAudit{}); err != nil {
		t.Fatal(err)
	}
	if err := changeGate(db, 22, false, time.Now()); err == nil {
		t.Fatal("disable succeeded without audit")
	}
	var g backupstate.Gate
	if err := db.First(&g, 1).Error; err != nil || !g.Enabled {
		t.Fatal("disable escaped audit transaction")
	}
}
