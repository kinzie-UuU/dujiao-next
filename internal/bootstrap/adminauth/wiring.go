package adminauthwiring

import (
	"github.com/dujiao-next/internal/app/container"
	captchahttp "github.com/dujiao-next/internal/modules/captcha/transport/http"
	adminchallengestore "github.com/dujiao-next/internal/modules/identity/adminauth/infrastructure/gormstore"
	adminauthtransport "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	"github.com/dujiao-next/internal/platform/database/gormdb"
)

type Handlers struct {
	Login     *adminauthtransport.AdminLoginHandler
	TwoFA     *adminauthtransport.Admin2FAHandler
	UserTwoFA *adminauthtransport.AdminUser2FAHandler
}

func New(c *container.Container) Handlers {
	recorder := adminLoginRecorderAdapter{logs: c.AdminLoginLogService}
	notifier := adminLoginNotifierAdapter{notifications: c.NotificationService, slots: make(chan struct{}, 32), prefix: c.Config.Redis.Prefix}
	return Handlers{
		Login: adminauthtransport.NewAdminLoginHandler(
			adminLoginAuthTransportAdapter{auth: c.AuthService},
			captchahttp.NewVerifier(c.CaptchaService),
			recorder,
			notifier,
		),
		TwoFA: adminauthtransport.NewAdmin2FAHandler(
			admin2FATOTPTransportAdapter{totp: c.TOTPService},
			admin2FAAuthTransportAdapter{auth: c.AuthService},
			adminchallengestore.NewChallengeStore(gormdb.DB),
			recorder,
			notifier,
		),
		UserTwoFA: adminauthtransport.NewAdminUser2FAHandler(
			adminUser2FATransportAdapter{totp: c.UserTOTPService},
		),
	}
}
