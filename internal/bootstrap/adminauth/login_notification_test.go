package adminauthwiring

import (
	"errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	adminauthtransport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
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
