package integrationtest

import (
	"github.com/dujiao-next/internal/constants"
	"github.com/golang-jwt/jwt/v5"
	"testing"
	"time"
)

func TestUserChallengeRevokedByPasswordOrDisable(t *testing.T) {
	auth, _, repo, _ := newUser2FATestServices(t)
	user := createActiveUser(t, repo, "challenge@example.invalid", "old-password")
	token, _, _, err := auth.IssueUserChallengeToken(user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseUserChallengeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.CompleteLoginAfter2FA(user.ID, false, *claims.TokenVersion); err != nil {
		t.Fatal(err)
	}
	oldVersion := user.TokenVersion
	user.TokenVersion++
	user.PasswordHash = "new-hash"
	if err := repo.Update(user, oldVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseUserChallengeToken(token); err == nil {
		t.Fatal("old challenge survived password change")
	}
	if _, err := auth.CompleteLoginAfter2FA(user.ID, false, *claims.TokenVersion); err == nil {
		t.Fatal("password change during verification issued session")
	}
	fresh, _, _, err := auth.IssueUserChallengeToken(user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	freshClaims, err := auth.ParseUserChallengeToken(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.CompleteLoginAfter2FA(user.ID, false, *freshClaims.TokenVersion); err != nil {
		t.Fatal(err)
	}
	// Even a status-only administrative disable must block challenge completion.
	user.Status = constants.UserStatusDisabled
	if err := repo.Update(user); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseUserChallengeToken(fresh); err == nil {
		t.Fatal("disabled user challenge accepted")
	}
	if _, err := auth.CompleteLoginAfter2FA(user.ID, false, *freshClaims.TokenVersion); err == nil {
		t.Fatal("disabled user received session")
	}
}

func TestUserLegacyChallengeWithoutVersionRejected(t *testing.T) {
	auth, _, repo, _ := newUser2FATestServices(t)
	user := createActiveUser(t, repo, "legacy@example.invalid", "old-password")
	token, _, _, err := auth.IssueUserChallengeToken(user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseUserChallengeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	claims.TokenVersion = nil
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("user-jwt-test-secret-2fa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseUserChallengeToken(legacy); err == nil {
		t.Fatal("legacy challenge accepted")
	}
}

func TestUserUpdatePreservesRecoveryAndRejectsStaleSecuritySnapshot(t *testing.T) {
	_, repo, _ := newUserTOTPTestService(t)
	user := createUserTOTPTestUser(t, repo, "snapshot@example.invalid")
	if err := repo.UpdateRecoveryCodes(user.ID, "consumed-snapshot"); err != nil {
		t.Fatal(err)
	}
	user.DisplayName = "new name"
	if err := repo.Update(user); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RecoveryCodes != "consumed-snapshot" {
		t.Fatal("profile restored old recovery snapshot")
	}
	if err := repo.ClearTOTP(user.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(user); err == nil {
		t.Fatal("profile overwrote MFA revocation")
	}
	if updated, err := repo.UpdateLastLogin(user.ID, user.TokenVersion, time.Now()); err != nil || updated {
		t.Fatalf("login accepted old security version: %v", err)
	}
}
