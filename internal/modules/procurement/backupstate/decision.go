// Package backupstate contains conservative eligibility rules for backup
// procurement. Its decisions are advisory until evaluated and claimed inside
// the same transaction that fences the order, primary attempt, refund, and
// competing backup attempts. It does not send orders or grant permissions.
package backupstate

type Product string

const (
	Muse Product = "muse"
	Plus Product = "plus"
)

type Trigger string

const (
	Automatic Trigger = "automatic"
	Manual    Trigger = "manual"
)

type PrimaryEvidence string

const (
	// NoPrimary is used for the manually fulfilled Plus product only.
	NoPrimary PrimaryEvidence = "no_primary"
	// NeverSent is a durable, verified assertion, not an absence of a receipt.
	NeverSent PrimaryEvidence = "never_sent"
	// NoPurchaseConfirmed is a verified assertion that no purchase occurred.
	NoPurchaseConfirmed PrimaryEvidence = "no_purchase_confirmed"
)

type BackupState string

const (
	NoBackup  BackupState = "none"
	Claimed   BackupState = "claimed"
	Submitted BackupState = "submitted"
	Uncertain BackupState = "uncertain"
	Completed BackupState = "completed"
	NotSent   BackupState = "not_sent"
)

// Snapshot must be read under the same locks used for the atomic claim. A
// caller must treat legacy/incomplete evidence as unknown, never as NeverSent.
type Snapshot struct {
	Product          Product
	Trigger          Trigger
	Paid             bool
	Refunded         bool
	AlreadyDelivered bool
	PrimaryEvidence  PrimaryEvidence
	PrimaryAttempted bool
	PrimaryUncertain bool
	ExistingBackup   BackupState
	AdminAuthorized  bool
}

type Decision string

const (
	AllowClaim      Decision = "allow_claim"
	RejectOrder     Decision = "reject_order"
	RejectTrigger   Decision = "reject_trigger"
	RejectPrimary   Decision = "reject_primary"
	RejectDuplicate Decision = "reject_duplicate"
)

// Evaluate only permits a claim; the caller must commit that claim atomically
// with the snapshot checks. Any submitted or uncertain primary attempt is
// deliberately blocked, even if a later no-purchase assertion is supplied.
func Evaluate(s Snapshot) Decision {
	if !s.Paid || s.Refunded || s.AlreadyDelivered {
		return RejectOrder
	}
	if s.ExistingBackup != NoBackup {
		return RejectDuplicate
	}
	if s.PrimaryAttempted || s.PrimaryUncertain {
		return RejectPrimary
	}
	switch s.Product {
	case Muse:
		if s.Trigger != Automatic {
			return RejectTrigger
		}
		if s.PrimaryEvidence != NeverSent && s.PrimaryEvidence != NoPurchaseConfirmed {
			return RejectPrimary
		}
	case Plus:
		if s.Trigger != Manual || !s.AdminAuthorized {
			return RejectTrigger
		}
		if s.PrimaryEvidence != NoPrimary {
			return RejectPrimary
		}
	default:
		return RejectTrigger
	}
	return AllowClaim
}
