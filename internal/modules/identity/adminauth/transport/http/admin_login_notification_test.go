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

type loginNoticeSpy struct {
	items    []AdminLoginNotification
	failures []AdminLoginFailureNotification
}

func (s *loginNoticeSpy) NotifyAdminLoginFailure(v AdminLoginFailureNotification) {
	s.failures = append(s.failures, v)
}

func (s *loginNoticeSpy) NotifyAdminLogin(v AdminLoginNotification) { s.items = append(s.items, v) }

type loginAuthStub struct {
	LoginAuthService
	result *AuthLoginResult
	err    error
}

func (s loginAuthStub) Login(_, _ string) (*AuthLoginResult, error) { return s.result, s.err }

type twoFAAuthStub struct {
	AuthService
	parseErr error
	err      error
}

func (s twoFAAuthStub) ParseChallengeToken(string) (*ChallengeClaims, error) {
	return &ChallengeClaims{AdminID: 7, JTI: "challenge"}, s.parseErr
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

func TestFailureNoticesCoverPasswordAndTwoFAWithoutCredentials(t *testing.T) {
	for _, username := range []string{"admin", "unknown-private-user"} {
		spy := &loginNoticeSpy{}
		h := NewAdminLoginHandler(loginAuthStub{err: ErrInvalidCredentials}, nil, nil, spy)
		c, _ := loginContext(`{"username":"` + username + `","password":"private-password"}`)
		h.AdminLogin(c)
		if len(spy.failures) != 1 || spy.failures[0].Method != "password" {
			t.Fatal("missing password failure")
		}
		raw, _ := json.Marshal(spy.failures)
		if strings.Contains(string(raw), username) || strings.Contains(string(raw), "private") {
			t.Fatal("credential/account leaked")
		}
	}
	for _, recovery := range []bool{false, true} {
		spy := &loginNoticeSpy{}
		err := ErrTOTPCodeInvalid
		body := `{"challenge_token":"private-challenge","code":"123456"}`
		if recovery {
			err = ErrTOTPRecoveryInvalid
			body = `{"challenge_token":"private-challenge","recovery_code":"private-recovery"}`
		}
		h := NewAdmin2FAHandler(twoFACodeStub{err: err}, twoFAAuthStub{}, nil, nil, spy)
		c, _ := loginContext(body)
		h.Verify2FA(c)
		if len(spy.failures) != 1 || spy.failures[0].ClientIP != "203.0.113.7" {
			t.Fatal("missing 2FA failure")
		}
		raw, _ := json.Marshal(spy.failures)
		if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "123456") {
			t.Fatal("2FA secret leaked")
		}
	}
	spy := &loginNoticeSpy{}
	h := NewAdminLoginHandler(loginAuthStub{err: errors.New("database offline")}, nil, nil, spy)
	c, _ := loginContext(`{"username":"admin","password":"private"}`)
	h.AdminLogin(c)
	if len(spy.failures) != 0 {
		t.Fatal("internal failure counted as bad credentials")
	}
}

func TestInvalidChallengeCountsFailureWithoutExposingToken(t *testing.T) {
	spy := &loginNoticeSpy{}
	h := NewAdmin2FAHandler(twoFACodeStub{}, twoFAAuthStub{parseErr: errors.New("invalid")}, nil, nil, spy)
	c, _ := loginContext(`{"challenge_token":"private-expired-challenge","code":"123456"}`)
	h.Verify2FA(c)
	if len(spy.failures) != 1 || spy.failures[0].Method != "2fa_challenge" {
		t.Fatal("invalid challenge not counted")
	}
}

type enableCodeStub struct{ TOTPService }

func (enableCodeStub) Enable(uint, string) (*TOTPEnableResult, error) {
	return &TOTPEnableResult{EnabledAt: time.Now(), RecoveryCodes: []string{"recovery"}}, nil
}
func TestEnable2FAReissuesCurrentSession(t *testing.T) {
	h := NewAdmin2FAHandler(enableCodeStub{}, twoFAAuthStub{}, nil, nil)
	c, w := loginContext(`{"code":"123456"}`)
	c.Set("admin_id", uint(7))
	h.Enable2FA(c)
	var body struct {
		Data TOTPEnableResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Token != finalLoginResult().Token || len(body.Data.RecoveryCodes) != 1 {
		t.Fatalf("session/recovery response: %s", w.Body.String())
	}
}
