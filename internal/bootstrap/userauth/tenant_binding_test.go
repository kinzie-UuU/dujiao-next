package userauthwiring

import (
	"context"
	"github.com/dujiao-next/internal/config"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userauthapp "github.com/dujiao-next/internal/modules/identity/userauth/application"
	resellercontract "github.com/dujiao-next/internal/modules/reseller/contract"
	"testing"
)

func TestLoginResultBindsBothAccessAndChallengeTokensToRequestTenant(t *testing.T) {
	cfg := &config.Config{}
	cfg.UserJWT.SecretKey = "tenant-adapter-test-secret"
	auth := userauthapp.NewService(cfg, nil, nil, nil, nil, nil, nil)
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
