package adminauthwiring

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"
	adminstore "github.com/dujiao-next/internal/modules/identity/admin/infrastructure/gormstore"
	authapp "github.com/dujiao-next/internal/modules/identity/adminauth/application"
	challengestore "github.com/dujiao-next/internal/modules/identity/adminauth/infrastructure/gormstore"
	totpapp "github.com/dujiao-next/internal/modules/identity/adminauth/totp/application"
	transport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Real password/challenge/TOTP/recovery paths with no Redis and only synthetic data.
func TestMFALoginAndRecoveryWithoutRedis(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	if err = db.AutoMigrate(&admindomain.Admin{}, &challengestore.ChallengeRecord{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{App: config.AppConfig{SecretKey: "test-encryption"}, JWT: config.JWTConfig{SecretKey: "test-jwt", ExpireHours: 24}}
	repo := adminstore.New(db)
	hash, _ := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	admin := &admindomain.Admin{Username: "synthetic-admin", PasswordHash: string(hash)}
	if err = repo.Create(admin); err != nil {
		t.Fatal(err)
	}
	auth := authapp.NewService(cfg, repo)
	testTime := time.Now()
	svc := totpapp.NewService(cfg, repo, nil, totpapp.WithClock(func() time.Time { return testTime }))
	setup, err := svc.Setup(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(setup.Secret, testTime)
	enabled, err := svc.Enable(admin.ID, code)
	if err != nil {
		t.Fatal(err)
	}
	makeHandler := func() *transport.Admin2FAHandler {
		return transport.NewAdmin2FAHandler(admin2FATOTPTransportAdapter{svc}, admin2FAAuthTransportAdapter{auth}, challengestore.NewChallengeStore(db), nil)
	}
	verify := func(h *transport.Admin2FAHandler, token, code, recovery string) string {
		raw, _ := json.Marshal(map[string]string{"challenge_token": token, "code": code, "recovery_code": recovery})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/verify", strings.NewReader(string(raw)))
		c.Request.Header.Set("Content-Type", "application/json")
		h.Verify2FA(c)
		var result struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data.Token
	}
	login := func() string {
		r, e := auth.Login("synthetic-admin", "test-password")
		if e != nil || !r.RequiresTOTP {
			t.Fatalf("password login: %v", e)
		}
		return r.ChallengeToken
	}
	token := login()
	if verify(makeHandler(), token, code, "") == "" {
		t.Fatal("normal TOTP login failed without Redis")
	}
	if verify(makeHandler(), token, code, "") != "" {
		t.Fatal("replay accepted by rebuilt handler")
	}
	token = login()
	for i := 0; i < 5; i++ {
		if verify(makeHandler(), token, "invalid", "") != "" {
			t.Fatal("bad OTP accepted")
		}
	}
	if verify(makeHandler(), token, code, "") != "" {
		t.Fatal("failure bound bypassed")
	}
	token = login()
	if verify(makeHandler(), token, "", enabled.RecoveryCodes[0]) == "" {
		t.Fatal("fresh recovery login blocked after failures")
	}
	token = login()
	if verify(makeHandler(), token, "", enabled.RecoveryCodes[0]) != "" {
		t.Fatal("used recovery code accepted")
	}
	if verify(makeHandler(), token, code, "") == "" {
		t.Fatal("normal retry blocked after bad recovery code")
	}
}
