package userauthhttp

import (
	"bytes"
	"context"
	"encoding/json"
	resellercontract "github.com/dujiao-next/internal/modules/reseller/contract"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"

	"github.com/gin-gonic/gin"
)

type sourceTestTOTPService struct{}

func (sourceTestTOTPService) GetStatus(uint) (*UserTOTPStatus, error) {
	return &UserTOTPStatus{}, nil
}
func (sourceTestTOTPService) Setup(uint) (*UserTOTPSetupResult, error) {
	return &UserTOTPSetupResult{}, nil
}
func (sourceTestTOTPService) Enable(uint, string) (*UserTOTPEnableResult, error) {
	return &UserTOTPEnableResult{}, nil
}
func (sourceTestTOTPService) Disable(uint, string, bool) error { return nil }
func (sourceTestTOTPService) RegenerateRecoveryCodes(uint, string) ([]string, error) {
	return nil, nil
}
func (sourceTestTOTPService) VerifyChallengeCode(uint, string) error         { return nil }
func (sourceTestTOTPService) VerifyChallengeRecoveryCode(uint, string) error { return nil }

type sourceTest2FAAuth struct {
	resellerID uint
	source     string
}

func (s sourceTest2FAAuth) ParseUserChallengeToken(string) (*UserChallengeClaims, error) {
	return &UserChallengeClaims{
		ResellerID:  s.resellerID,
		UserID:      42,
		JTI:         "source-test-jti",
		LoginSource: s.source,
	}, nil
}

func (sourceTest2FAAuth) CompleteLoginAfter2FA(context.Context, uint, bool, ...uint64) (*AuthLoginResult, error) {
	now := time.Now()
	return &AuthLoginResult{
		User: &userdomain.User{
			ID:    42,
			Email: "buyer@example.com",
		},
		Token:     "access-token",
		ExpiresAt: now.Add(time.Hour),
	}, nil
}

func (sourceTest2FAAuth) GetUserEmail(uint) (string, error) {
	return "buyer@example.com", nil
}

type sourceTestChallengeStore struct{}

func (sourceTestChallengeStore) Reserve(context.Context, string) (bool, error) {
	return true, nil
}
func (sourceTestChallengeStore) Consume(context.Context, string) (bool, error) {
	return true, nil
}

type sourceTestLoginRecord struct {
	source string
	status string
}

type sourceTestLoginRecorder struct {
	records []sourceTestLoginRecord
}

func (r *sourceTestLoginRecorder) Record(_ string, _ uint, status, _, source, _, _, _ string) {
	r.records = append(r.records, sourceTestLoginRecord{source: source, status: status})
}

func TestVerifyUser2FAPreservesExternalLoginSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, source := range []string{constants.LoginLogSourceGoogle, constants.LoginLogSourceTelegram} {
		t.Run(source, func(t *testing.T) {
			loginRecorder := &sourceTestLoginRecorder{}
			handler := NewUser2FAHandler(
				sourceTestTOTPService{},
				sourceTest2FAAuth{source: source},
				sourceTestChallengeStore{},
				loginRecorder,
			)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(
				http.MethodPost,
				"/api/v1/auth/login/verify-2fa",
				bytes.NewBufferString(`{"challenge_token":"challenge","code":"123456"}`),
			)
			context.Request.Header.Set("Content-Type", "application/json")

			handler.VerifyUser2FA(context)

			if len(loginRecorder.records) != 1 {
				t.Fatalf("login records = %d, want 1", len(loginRecorder.records))
			}
			if loginRecorder.records[0].source != source {
				t.Fatalf("login source = %q, want %q", loginRecorder.records[0].source, source)
			}
			if loginRecorder.records[0].status != constants.LoginLogStatusSuccess {
				t.Fatalf("login status = %q", loginRecorder.records[0].status)
			}
		})
	}
}

func TestVerifyUser2FARejectsCrossTenantChallenge(t *testing.T) {
	for _, pair := range [][2]uint{{7, 0}, {7, 8}, {0, 7}} {
		handler := NewUser2FAHandler(sourceTestTOTPService{}, sourceTest2FAAuth{resellerID: pair[0]}, sourceTestChallengeStore{}, &sourceTestLoginRecorder{})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/verify-2fa", bytes.NewBufferString(`{"challenge_token":"challenge","code":"123456"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		tenant := resellercontract.TenantContext{IsMain: pair[1] == 0}
		if pair[1] != 0 {
			tenant.ResellerID = &pair[1]
		}
		c.Request = c.Request.WithContext(resellercontract.WithTenantContext(c.Request.Context(), tenant))
		handler.VerifyUser2FA(c)
		var body struct {
			StatusCode int `json:"status_code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.StatusCode != 401 {
			t.Fatalf("pair %v accepted: %s", pair, w.Body.String())
		}
	}
}
