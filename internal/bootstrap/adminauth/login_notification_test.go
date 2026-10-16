package adminauthwiring

import (
	"errors"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	adminauthtransport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
)

type loginQueueSpy struct {
	input notificationcontract.EnqueueInput
	err   error
	calls int
}

func (s *loginQueueSpy) Enqueue(input notificationcontract.EnqueueInput) error {
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
