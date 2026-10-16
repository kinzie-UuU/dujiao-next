package adminauthwiring

import (
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	adminauthtransport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

type adminLoginNotifierAdapter struct {
	notifications notificationcontract.NotificationEnqueuer
	slots         chan struct{}
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
