package integrationtest

import (
	"github.com/golang-jwt/jwt/v5"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	admincontract "github.com/dujiao-next/internal/modules/identity/admin/contract"
	adminauthapp "github.com/dujiao-next/internal/modules/identity/adminauth/application"
)

func TestPasswordResetRevokesPendingAdminChallenge(t *testing.T) {
	auth, _, repo := newAuthTestService(t)
	admin := createAuthTestAdmin(t, repo, "challenge", "old-password")
	token, _, _, err := auth.IssueChallengeToken(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseChallengeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.CompleteLoginAfter2FA(admin.ID, *claims.TokenVersion); err != nil {
		t.Fatalf("normal login: %v", err)
	}
	if err := repo.UpdatePassword(admin.ID, "reset-password-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseChallengeToken(token); err == nil {
		t.Fatal("password reset left old challenge valid")
	}
	if _, err := auth.CompleteLoginAfter2FA(admin.ID, *claims.TokenVersion); err == nil {
		t.Fatal("password reset during verification issued a new session")
	}
	fresh, _, _, err := auth.IssueChallengeToken(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	freshClaims, err := auth.ParseChallengeToken(fresh)
	if err != nil {
		t.Fatal(err)
	}
	login, err := auth.CompleteLoginAfter2FA(admin.ID, *freshClaims.TokenVersion)
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.ParseJWT(login.Token)
	if err != nil || session.TokenVersion != *freshClaims.TokenVersion {
		t.Fatalf("fresh session version mismatch: %v", err)
	}
}

// Revoke after the login read but before its write, deterministically exercising
// the dangerous interleaving that used to Save the old full administrator row.
type revokeBeforeLoginStore struct{ admincontract.Store }

func (s revokeBeforeLoginStore) UpdateLastLogin(id uint, version uint64, at time.Time) (bool, error) {
	if err := s.Store.UpdatePassword(id, "new-password-hash"); err != nil {
		return false, err
	}
	return s.Store.UpdateLastLogin(id, version, at)
}

func TestLoginCannotOverwriteConcurrentPasswordReset(t *testing.T) {
	_, _, repo := newAuthTestService(t)
	admin := createAuthTestAdmin(t, repo, "login-race", "old-password")
	cfg := &config.Config{JWT: config.JWTConfig{SecretKey: "test-secret", ExpireHours: 1}}
	auth := adminauthapp.NewService(cfg, revokeBeforeLoginStore{Store: repo})
	if _, err := auth.CompleteLoginAfter2FA(admin.ID, admin.TokenVersion); err == nil {
		t.Fatal("issued session over concurrent revocation")
	}
	stored, err := repo.GetByID(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash != "new-password-hash" || stored.TokenVersion != admin.TokenVersion+1 {
		t.Fatal("login overwrote security state")
	}
}

func TestLastLoginWritePreservesConsumedRecoveryCode(t *testing.T) {
	_, repo, _ := newTOTPTestService(t)
	admin := createTOTPTestAdmin(t, repo, "login-recovery")
	if err := repo.UpdateRecoveryCodes(admin.ID, "consumed-snapshot"); err != nil {
		t.Fatal(err)
	}
	if updated, err := repo.UpdateLastLogin(admin.ID, admin.TokenVersion, time.Now()); err != nil || !updated {
		t.Fatalf("login: %v", err)
	}
	stored, err := repo.GetByID(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RecoveryCodes != "consumed-snapshot" {
		t.Fatal("login overwrote recovery consumption")
	}
}

func TestPasswordUpdatePreservesRecoveryAndRejectsStaleVersion(t *testing.T) {
	_, _, repo := newAuthTestService(t)
	admin := createAuthTestAdmin(t, repo, "password-race", "old-password")
	if err := repo.UpdateRecoveryCodes(admin.ID, "consumed-snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdatePassword(admin.ID, "new-hash", admin.TokenVersion); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdatePassword(admin.ID, "stale-hash", admin.TokenVersion); err == nil {
		t.Fatal("stale password write accepted")
	}
	stored, err := repo.GetByID(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RecoveryCodes != "consumed-snapshot" || stored.PasswordHash != "new-hash" || stored.TokenVersion != admin.TokenVersion+1 {
		t.Fatal("password update overwrote concurrent security state")
	}
}

func TestAccountEditCannotRestoreRecoveryOrRevokedCredentials(t *testing.T) {
	_, _, repo := newAuthTestService(t)
	admin := createAuthTestAdmin(t, repo, "account-race", "old-password")
	snapshot, err := repo.GetByID(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRecoveryCodes(admin.ID, "consumed-snapshot"); err != nil {
		t.Fatal(err)
	}
	snapshot.Username = "renamed"
	if err := repo.Update(snapshot, snapshot.TokenVersion); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RecoveryCodes != "consumed-snapshot" {
		t.Fatal("account edit restored old recovery codes")
	}
	if err := repo.UpdatePassword(admin.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	snapshot.IsSuper = true
	if err := repo.Update(snapshot, snapshot.TokenVersion); err == nil {
		t.Fatal("stale account edit accepted after password reset")
	}
	current, err := repo.GetByID(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.PasswordHash != "new-hash" || current.TokenVersion != snapshot.TokenVersion+1 || current.IsSuper {
		t.Fatal("stale edit overwrote revocation or role")
	}
	if err := repo.ClearTOTP(admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(current, current.TokenVersion); err == nil {
		t.Fatal("stale account edit accepted after MFA reset")
	}
}

func TestLegacyChallengeWithoutVersionIsRejected(t *testing.T) {
	auth, _, repo := newAuthTestService(t)
	admin := createAuthTestAdmin(t, repo, "legacy-challenge", "old-password")
	token, _, _, err := auth.IssueChallengeToken(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseChallengeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	claims.TokenVersion = nil
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("jwt-secret-for-test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseChallengeToken(legacy); err == nil {
		t.Fatal("legacy versionless challenge accepted")
	}
}
