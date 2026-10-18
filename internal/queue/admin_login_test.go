package queue

import (
	"net"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
)

func TestLoginNoticeReportsDisabledQueue(t *testing.T) {
	c, _ := NewClient(nil)
	for _, event := range []string{constants.NotificationEventAdminLoginSuccess, constants.NotificationEventAdminLoginFailed, constants.NotificationEventAdminPaymentConfigChanged} {
		if c.EnqueueNotificationDispatch(NotificationDispatchPayload{EventType: event}) == nil {
			t.Fatalf("disabled security queue accepted %s", event)
		}
	}
	if err := c.EnqueueNotificationDispatch(NotificationDispatchPayload{EventType: constants.NotificationEventOrderPaidSuccess}); err != nil {
		t.Fatal("existing event behavior changed")
	}
}

func TestLoginNoticeEnqueueHasBoundedWaitForUnresponsiveRedis(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	release := make(chan struct{})
	defer close(release)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); <-release }()
		}
	}()
	c, err := NewClient(&config.QueueConfig{Enabled: true, Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	err = c.EnqueueNotificationDispatch(NotificationDispatchPayload{EventType: constants.NotificationEventAdminLoginSuccess})
	if err == nil {
		t.Fatal("unresponsive Redis accepted enqueue")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("enqueue exceeded bounded wait: %v", elapsed)
	}
}
