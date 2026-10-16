package adminauthwiring

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	adminauthtransport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/google/uuid"
	"strings"
	"time"
)

type adminLoginNotifierAdapter struct {
	notifications notificationcontract.NotificationEnqueuer
	slots         chan struct{}
	prefix        string
}

func (a adminLoginNotifierAdapter) NotifyAdminLogin(login adminauthtransport.AdminLoginNotification) {
	if a.notifications == nil {
		return
	}
	// Bound work without waiting on Redis or Telegram in the authentication request.
	select {
	case a.slots <- struct{}{}:
	default:
		logger.Warnw("admin_login_notification_capacity_exceeded", "admin_id", login.AdminID)
		return
	}
	go func() {
		defer func() { <-a.slots }()
		a.enqueue(login)
	}()
}

func (a adminLoginNotifierAdapter) enqueue(login adminauthtransport.AdminLoginNotification) {
	err := a.notifications.Enqueue(notificationcontract.EnqueueInput{
		EventType: constants.NotificationEventAdminLoginSuccess,
		BizType:   "admin_login", BizID: login.AdminID,
		Data: jsonmap.JSON{
			"admin_username": login.Username, "client_ip": login.ClientIP,
			"login_method": login.Method, "login_at": login.LoginAt, "login_event_id": login.EventID,
		},
	})
	if err != nil {
		// Authentication remains successful even when the notification queue is unavailable.
		logger.Warnw("admin_login_notification_enqueue_failed", "admin_id", login.AdminID)
	}
}

// Redis keeps counters shared across workers; submitted usernames are never keys or payloads.
const failureWindowScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('EXPIRE', KEYS[1], 600) end
if count < 5 then return 0 end
if redis.call('SET', KEYS[2], ARGV[1], 'NX', 'EX', 900) then return count end
return 0
`

func (a adminLoginNotifierAdapter) failureKeys(ip string) []string {
	prefix := strings.TrimSpace(a.prefix)
	if prefix == "" {
		prefix = constants.RedisPrefixDefault
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(ip)))
	return []string{prefix + ":admin_login_fail:count:" + digest, prefix + ":admin_login_fail:alert:" + digest}
}
func (a adminLoginNotifierAdapter) NotifyAdminLoginFailure(failure adminauthtransport.AdminLoginFailureNotification) {
	if a.notifications == nil {
		return
	}
	select {
	case a.slots <- struct{}{}:
	default:
		logger.Warnw("admin_login_failure_notification_capacity_exceeded")
		return
	}
	go func() { defer func() { <-a.slots }(); a.enqueueFailure(failure) }()
}
func (a adminLoginNotifierAdapter) enqueueFailure(failure adminauthtransport.AdminLoginFailureNotification) {
	client := cache.Client()
	if client == nil {
		logger.Warnw("admin_login_failure_counter_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	keys := a.failureKeys(failure.ClientIP)
	eventID := uuid.NewString()
	count, err := client.Eval(ctx, failureWindowScript, keys, eventID).Int64()
	if err != nil {
		logger.Warnw("admin_login_failure_counter_failed")
		return
	}
	if count == 0 {
		return
	}
	err = a.notifications.Enqueue(notificationcontract.EnqueueInput{
		EventType: constants.NotificationEventAdminLoginFailed, BizType: "admin_login", Data: jsonmap.JSON{
			"client_ip": failure.ClientIP, "login_method": failure.Method, "failure_count": count, "window_minutes": 10,
			"occurred_at": time.Now().UTC().Format(time.RFC3339), "security_event_id": eventID,
		},
	})
	if err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		// Release only our reservation; the next failure can retry enqueueing the warning.
		_ = client.Eval(cleanupCtx, `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end return 0`, []string{keys[1]}, eventID).Err()
		logger.Warnw("admin_login_failure_notification_enqueue_failed")
	}
}
