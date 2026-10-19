package adminauthwiring

import (
	"encoding/json"
	"errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"
	adminstore "github.com/dujiao-next/internal/modules/identity/admin/infrastructure/gormstore"
	authapp "github.com/dujiao-next/internal/modules/identity/adminauth/application"
	challengestore "github.com/dujiao-next/internal/modules/identity/adminauth/infrastructure/gormstore"
	totpapp "github.com/dujiao-next/internal/modules/identity/adminauth/totp/application"
	adminauthtransport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	transport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type loginQueueSpy struct {
	mu    sync.Mutex
	input notificationcontract.EnqueueInput
	err   error
	calls int
}

func (s *loginQueueSpy) Enqueue(input notificationcontract.EnqueueInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.input = input
	s.calls++
	return s.err
}
func TestAdminLoginAdapterQueuesOnlySafeFactsAndToleratesFailure(t *testing.T) {
	for _, err := range []error{nil, errors.New("queue unavailable")} {
		queue := &loginQueueSpy{err: err}
		adapter := adminLoginNotifierAdapter{notifications: queue}
		adapter.enqueue(adminauthtransport.AdminLoginNotification{AdminID: 7, Username: "admin", ClientIP: "203.0.113.7", Method: "password", LoginAt: "2026-10-16T12:00:00Z", EventID: "unique-login"})
		if queue.calls != 1 || queue.input.EventType != constants.NotificationEventAdminLoginSuccess || queue.input.BizID != 7 || queue.input.Force {
			t.Fatalf("unexpected event: %+v", queue.input)
		}
		if len(queue.input.Data) != 5 || queue.input.Data["login_event_id"] != "unique-login" {
			t.Fatal("payload allowlist changed")
		}
	}
}

type blockedLoginQueue struct {
	entered chan struct{}
	release chan struct{}
}

func (q *blockedLoginQueue) Enqueue(notificationcontract.EnqueueInput) error {
	q.entered <- struct{}{}
	<-q.release
	return nil
}
func TestAdminLoginNotificationNeverWaitsForQueueAndBoundsConcurrency(t *testing.T) {
	q := &blockedLoginQueue{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(q.release)
	a := adminLoginNotifierAdapter{notifications: q, slots: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() { a.NotifyAdminLogin(adminauthtransport.AdminLoginNotification{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("login blocked on queue")
	}
	select {
	case <-q.entered:
	case <-time.After(time.Second):
		t.Fatal("queue not invoked")
	}
	// The first enqueue stays blocked; the next notification must not start another worker.
	a.NotifyAdminLogin(adminauthtransport.AdminLoginNotification{})
	if len(a.slots) != 1 || len(q.entered) != 0 {
		t.Fatal("unbounded enqueue workers")
	}
}

func TestFailureAlertWindowThresholdCooldownAndEnqueueRetry(t *testing.T) {
	r := miniredis.RunT(t)
	port, _ := strconv.Atoi(r.Port())
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: r.Host(), Port: port, Prefix: "security-test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
	q := &loginQueueSpy{}
	a := adminLoginNotifierAdapter{notifications: q, prefix: "security-test"}
	f := adminauthtransport.AdminLoginFailureNotification{ClientIP: "203.0.113.7", Method: "password"}
	for i := 0; i < 4; i++ {
		a.enqueueFailure(f)
	}
	if q.calls != 0 {
		t.Fatal("alert below threshold")
	}
	a.enqueueFailure(f)
	if q.calls != 1 || q.input.EventType != constants.NotificationEventAdminLoginFailed || q.input.Data["failure_count"] != int64(5) || len(q.input.Data) != 6 {
		t.Fatalf("bad alert: %+v", q)
	}
	for i := 0; i < 10; i++ {
		a.enqueueFailure(f)
	}
	if q.calls != 1 {
		t.Fatal("cooldown bypassed")
	}
	f.ClientIP = "203.0.113.8"
	for i := 0; i < 4; i++ {
		a.enqueueFailure(f)
	}
	r.FastForward(11 * time.Minute)
	a.enqueueFailure(f)
	if q.calls != 1 {
		t.Fatal("expired count survived window")
	}
	r.FastForward(5 * time.Minute)
	f.ClientIP = "203.0.113.7"
	q.err = errors.New("queue offline")
	for i := 0; i < 5; i++ {
		a.enqueueFailure(f)
	}
	if q.calls != 2 {
		t.Fatal("cooldown did not expire")
	}
	q.err = nil
	a.enqueueFailure(f)
	if q.calls != 3 {
		t.Fatal("failed enqueue consumed cooldown")
	}
	a.enqueueFailure(f)
	if q.calls != 3 {
		t.Fatal("successful enqueue did not retain cooldown")
	}
}

func TestConcurrentFailureAlertsReserveOnlyOneNotification(t *testing.T) {
	r := miniredis.RunT(t)
	port, _ := strconv.Atoi(r.Port())
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: r.Host(), Port: port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
	q := &loginQueueSpy{}
	a := adminLoginNotifierAdapter{notifications: q}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.enqueueFailure(adminauthtransport.AdminLoginFailureNotification{ClientIP: "203.0.113.1", Method: "password"})
		}()
	}
	wg.Wait()
	if q.calls != 1 {
		t.Fatalf("concurrent sends=%d", q.calls)
	}
}

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
