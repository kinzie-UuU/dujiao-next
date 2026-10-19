package userauthwiring

import (
	"context"
	"github.com/dujiao-next/internal/config"
	usercontract "github.com/dujiao-next/internal/modules/identity/user/contract"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userauthapp "github.com/dujiao-next/internal/modules/identity/userauth/application"
	resellercontract "github.com/dujiao-next/internal/modules/reseller/contract"
	"github.com/golang-jwt/jwt/v5"
	"testing"
)

func TestLoginResultBindsBothAccessAndChallengeTokensToRequestTenant(t *testing.T) {
	cfg := &config.Config{}
	cfg.UserJWT.SecretKey = "tenant-adapter-test-secret"
	auth := userauthapp.NewService(cfg, tenantChallengeUsers{}, nil, nil, nil, nil, nil)
	access, expires, err := auth.GenerateUserJWT(&userdomain.User{ID: 9, Email: "scope@example.invalid"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	challenge, _, challengeExpires, err := auth.IssueUserChallengeToken(9, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, rid := range []uint{0, 7} {
		tenant := resellercontract.TenantContext{IsMain: rid == 0}
		if rid != 0 {
			tenant.ResellerID = &rid
		}
		ctx := resellercontract.WithTenantContext(context.Background(), tenant)
		for _, needsTOTP := range []bool{false, true} {
			result := &userauthapp.UserLoginResult{Token: access, ExpiresAt: expires}
			if needsTOTP {
				result = &userauthapp.UserLoginResult{RequiresTOTP: true, ChallengeToken: challenge, ChallengeExpiresAt: challengeExpires}
			}
			mapped, err := toUserAuthTransportLoginResult(ctx, auth, result)
			if err != nil {
				t.Fatal(err)
			}
			if needsTOTP {
				claims, err := auth.ParseUserChallengeToken(mapped.ChallengeToken)
				if err != nil {
					t.Fatal(err)
				}
				if claims.ResellerID != rid || claims.UserID != 9 {
					t.Fatalf("wrong challenge tenant: %+v", claims)
				}
			} else {
				claims, err := auth.ParseUserJWT(mapped.Token)
				if err != nil {
					t.Fatal(err)
				}
				if claims.ResellerID != rid || claims.UserID != 9 {
					t.Fatalf("wrong access tenant: %+v", claims)
				}
			}
		}
	}
}

// Token binding still validates the live account version.
type tenantChallengeUsers struct{ usercontract.Store }

func (tenantChallengeUsers) GetByID(id uint) (*userdomain.User, error) {
	return &userdomain.User{ID: id, Status: "active"}, nil
}

func TestLegacyRedisChallengeRejectedAfterPersistentStoreMigration(t *testing.T) {
	cfg := &config.Config{}
	cfg.UserJWT.SecretKey = "challenge-migration-fixture"
	auth := userauthapp.NewService(cfg, tenantChallengeUsers{}, nil, nil, nil, nil, nil)
	token, _, _, err := auth.IssueUserChallengeToken(9, false)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseUserChallengeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	claims.Purpose = "user_2fa_challenge"
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.UserJWT.SecretKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.ParseUserChallengeToken(legacy); err == nil {
		t.Fatal("legacy Redis-only challenge accepted")
	}
}
