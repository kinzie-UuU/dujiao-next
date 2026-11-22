package backupstate

import "testing"

func TestMuseRequiresVerifiedNeverAttemptedPrimary(t *testing.T) {
	base := Snapshot{Product: Muse, Trigger: Automatic, Paid: true, PrimaryEvidence: NeverSent, ExistingBackup: NoBackup}
	tests := []struct {
		name string
		edit func(*Snapshot)
		want Decision
	}{
		{"verified never sent", func(*Snapshot) {}, AllowClaim},
		{"verified no purchase", func(s *Snapshot) { s.PrimaryEvidence = NoPurchaseConfirmed }, AllowClaim},
		{"no primary pin", func(s *Snapshot) { s.PrimaryEvidence = NoPrimary }, AllowClaim},
		{"unknown legacy evidence", func(s *Snapshot) { s.PrimaryEvidence = "" }, RejectPrimary},
		{"submitted despite claim of no purchase", func(s *Snapshot) { s.PrimaryEvidence = NoPurchaseConfirmed; s.PrimaryAttempted = true }, RejectPrimary},
		{"uncertain despite claim of no purchase", func(s *Snapshot) { s.PrimaryEvidence = NoPurchaseConfirmed; s.PrimaryUncertain = true }, RejectPrimary},
		{"unpaid", func(s *Snapshot) { s.Paid = false }, RejectOrder},
		{"refunded", func(s *Snapshot) { s.Refunded = true }, RejectOrder},
		{"already delivered", func(s *Snapshot) { s.AlreadyDelivered = true }, RejectOrder},
		{"manual muse", func(s *Snapshot) { s.Trigger = Manual }, RejectTrigger},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.edit(&s)
			if got := Evaluate(s); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlusRequiresAuthorizedManualTrigger(t *testing.T) {
	base := Snapshot{Product: Plus, Trigger: Manual, Paid: true, PrimaryEvidence: NoPrimary, ExistingBackup: NoBackup, AdminAuthorized: true}
	if got := Evaluate(base); got != AllowClaim {
		t.Fatalf("manual trigger: %s", got)
	}
	for name, edit := range map[string]func(*Snapshot){
		"unauthorized": func(s *Snapshot) { s.AdminAuthorized = false },
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			edit(&s)
			if got := Evaluate(s); got != RejectTrigger {
				t.Fatalf("got %s", got)
			}
		})
	}
	base.PrimaryEvidence = NeverSent
	if got := Evaluate(base); got != RejectPrimary {
		t.Fatalf("unexpected primary: %s", got)
	}
}

func TestPlusAllowsPaidAutomaticWithoutAdmin(t *testing.T) {
	base := Snapshot{Product: Plus, Trigger: Automatic, Paid: true, PrimaryEvidence: NoPrimary, ExistingBackup: NoBackup}
	if got := Evaluate(base); got != AllowClaim {
		t.Fatalf("automatic plus: %s", got)
	}
	base.PrimaryEvidence = NeverSent
	if got := Evaluate(base); got != RejectPrimary {
		t.Fatalf("automatic plus unexpected primary: %s", got)
	}
}

func TestEveryExistingBackupPreventsAnotherClaim(t *testing.T) {
	for _, state := range []BackupState{"", Claimed, Submitted, Uncertain, Completed, "unknown"} {
		s := Snapshot{Product: Muse, Trigger: Automatic, Paid: true, PrimaryEvidence: NeverSent, ExistingBackup: state}
		if got := Evaluate(s); got != RejectDuplicate {
			t.Errorf("state %q: got %q", state, got)
		}
	}
}
