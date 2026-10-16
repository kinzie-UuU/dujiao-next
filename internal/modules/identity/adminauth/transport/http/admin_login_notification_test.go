package adminauthhttp

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"
	"github.com/gin-gonic/gin"
)

type loginNoticeSpy struct{ items []AdminLoginNotification }

func (s *loginNoticeSpy) NotifyAdminLogin(v AdminLoginNotification) { s.items = append(s.items, v) }

type loginAuthStub struct {
	LoginAuthService
	result *AuthLoginResult
	err    error
}

func (s loginAuthStub) Login(_, _ string) (*AuthLoginResult, error) { return s.result, s.err }

type twoFAAuthStub struct {
	AuthService
	err error
}

func (s twoFAAuthStub) ParseChallengeToken(string) (*ChallengeClaims, error) {
	return &ChallengeClaims{AdminID: 7, JTI: "challenge"}, nil
}
func (s twoFAAuthStub) GetAdminUsername(uint) (string, error) { return "admin", nil }
func (s twoFAAuthStub) CompleteLoginAfter2FA(uint) (*AuthLoginResult, error) {
	return finalLoginResult(), s.err
}

type twoFACodeStub struct {
	TOTPService
	err error
}

func (s twoFACodeStub) VerifyChallengeCode(uint, string) error         { return s.err }
func (s twoFACodeStub) VerifyChallengeRecoveryCode(uint, string) error { return s.err }
func finalLoginResult() *AuthLoginResult {
	return &AuthLoginResult{Admin: &admindomain.Admin{ID: 7, Username: "admin"}, Token: "private-session-token", ExpiresAt: time.Now().Add(time.Hour)}
}
func loginContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/login", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.RemoteAddr = "203.0.113.7:1234"
	return c, w
}

func TestPasswordNotificationOnlyAfterFinalLogin(t *testing.T) {
	for _, test := range []struct {
		name string
		totp bool
		err  error
		want int
	}{
		{"complete", false, nil, 1}, {"pending_totp", true, nil, 0}, {"bad_password", false, ErrInvalidCredentials, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := finalLoginResult()
			result.RequiresTOTP = test.totp
			spy := &loginNoticeSpy{}
			h := NewAdminLoginHandler(loginAuthStub{result: result, err: test.err}, nil, nil, spy)
			c, w := loginContext(`{"username":"admin","password":"private-password"}`)
			h.AdminLogin(c)
			if len(spy.items) != test.want {
				t.Fatalf("notices=%d want=%d", len(spy.items), test.want)
			}
			if test.want == 1 {
				v := spy.items[0]
				if v.ClientIP != "203.0.113.7" || v.Method != "password" || v.EventID == "" || v.LoginAt == "" {
					t.Fatalf("missing facts: %+v", v)
				}
				raw, _ := json.Marshal(v)
				if strings.Contains(string(raw), "private-") {
					t.Fatal("sensitive data in notification")
				}
				if w.Code != 200 || !strings.Contains(w.Body.String(), "private-session-token") {
					t.Fatal("login behavior changed")
				}
			}
		})
	}
}

func TestTwoFANotificationOnlyAfterFinalLogin(t *testing.T) {
	for _, test := range []struct {
		name, body, method string
		codeErr, loginErr  error
		want               int
	}{
		{"totp", `{"challenge_token":"private-challenge","code":"123456"}`, constants.AdminLoginEventLogin2FAVerify, nil, nil, 1},
		{"recovery", `{"challenge_token":"private-challenge","recovery_code":"private-recovery"}`, constants.AdminLoginEventLoginRecoveryCode, nil, nil, 1},
		{"invalid", `{"challenge_token":"private-challenge","code":"123456"}`, "", ErrTOTPCodeInvalid, nil, 0},
		{"session_failed", `{"challenge_token":"private-challenge","code":"123456"}`, "", nil, errors.New("failed"), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			spy := &loginNoticeSpy{}
			h := NewAdmin2FAHandler(twoFACodeStub{err: test.codeErr}, twoFAAuthStub{err: test.loginErr}, nil, nil, spy)
			c, _ := loginContext(test.body)
			h.Verify2FA(c)
			if len(spy.items) != test.want {
				t.Fatalf("notices=%d want=%d", len(spy.items), test.want)
			}
			if test.want == 1 && spy.items[0].Method != test.method {
				t.Fatal("incorrect method")
			}
		})
	}
}

func TestSeparateLoginsHaveSeparateEventIDs(t *testing.T) {
	spy := &loginNoticeSpy{}
	c, _ := loginContext(`{}`)
	notifyAdminLogin(c, spy, 7, "admin", "password")
	notifyAdminLogin(c, spy, 7, "admin", "password")
	if spy.items[0].EventID == spy.items[1].EventID {
		t.Fatal("independent logins deduplicated")
	}
}
