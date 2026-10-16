package application

import (
	"github.com/dujiao-next/internal/config"
	"testing"
)

func TestChallengeTokenRetainsTenant(t *testing.T) {
	cfg := &config.Config{}
	cfg.UserJWT.SecretKey = "test-challenge-tenant-secret"
	svc := &Service{cfg: cfg}
	token, _, _, err := svc.IssueUserChallengeToken(9, false)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := svc.BindUserChallengeTenant(token, 7)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.ParseUserChallengeToken(bound)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ResellerID != 7 || claims.UserID != 9 {
		t.Fatalf("wrong binding: %+v", claims)
	}
}
