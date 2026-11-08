package settingshttp

import (
	"errors"
	"github.com/dujiao-next/internal/shared/operationaudit"
	"strings"

	ginutil "github.com/dujiao-next/internal/platform/http/ginutil"

	settingsmessaging "github.com/dujiao-next/internal/modules/settings/schema/messaging"
	"github.com/dujiao-next/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

var (
	ErrSMTPTestInvalidEmail         = errors.New("invalid email")
	ErrSMTPTestRecipientRejected    = errors.New("email recipient rejected")
	ErrSMTPTestServiceDisabled      = errors.New("email service disabled")
	ErrSMTPTestServiceNotConfigured = errors.New("email service not configured")
)

// SMTPAdminService 是后台 SMTP 设置端口。
type SMTPAdminService interface {
	GetSMTPSetting() (settingsmessaging.SMTPSetting, error)
	PatchSMTPSetting(patch settingsmessaging.SMTPSettingPatch) (settingsmessaging.SMTPSetting, error)
	ApplyRuntime(setting settingsmessaging.SMTPSetting)
	SendTest(setting settingsmessaging.SMTPSetting, toEmail, subject, body string) error
}

// SMTPHandler 处理后台 SMTP 设置请求。
type SMTPHandler struct {
	smtp SMTPAdminService
}

func NewSMTPHandler(smtp SMTPAdminService) *SMTPHandler {
	if smtp == nil {
		panic("settings smtp handler: smtp is nil")
	}
	return &SMTPHandler{smtp: smtp}
}

type smtpTestSendRequest struct {
	ToEmail string `json:"to_email" binding:"required"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// GetSMTP 获取 SMTP 配置（脱敏）。
func (h *SMTPHandler) GetSMTP(c *gin.Context) {
	setting, err := h.smtp.GetSMTPSetting()
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_fetch_failed", err)
		return
	}
	response.Success(c, settingsmessaging.MaskSMTPSettingForAdmin(setting))
}

// UpdateSMTP 更新 SMTP 配置。
func (h *SMTPHandler) UpdateSMTP(c *gin.Context) {
	var req settingsmessaging.SMTPSettingPatch
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	old, readErr := h.smtp.GetSMTPSetting()
	before := operationaudit.Fields{"snapshot_available": false}
	if readErr == nil {
		before = smtpAuditFields(old)
	}
	audit := operationaudit.New("settings.smtp", "smtp_config", before)
	defer ginutil.RecordOperation(c, audit)
	setting, err := h.smtp.PatchSMTPSetting(req)
	if err != nil {
		switch {
		case errors.Is(err, settingsmessaging.ErrSMTPConfigInvalid):
			ginutil.RespondErrorWithMsg(c, response.CodeBadRequest, err.Error(), nil)
		default:
			ginutil.RespondError(c, response.CodeInternal, "error.settings_save_failed", err)
		}
		return
	}

	after := smtpAuditFields(setting)
	if readErr == nil {
		after["password_changed"] = old.Password != setting.Password
		after["username_changed"] = old.Username != setting.Username
		after["host_changed"] = old.Host != setting.Host
		after["from_changed"] = old.From != setting.From || old.FromName != setting.FromName
	}
	audit.Success(after)
	h.smtp.ApplyRuntime(setting)
	response.Success(c, settingsmessaging.MaskSMTPSettingForAdmin(setting))
}

// TestSMTP 测试 SMTP 配置发送。
func (h *SMTPHandler) TestSMTP(c *gin.Context) {
	var req smtpTestSendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	toEmail := strings.TrimSpace(req.ToEmail)
	if toEmail == "" {
		ginutil.RespondError(c, response.CodeBadRequest, "error.email_invalid", nil)
		return
	}

	setting, err := h.smtp.GetSMTPSetting()
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_fetch_failed", err)
		return
	}

	if err := h.smtp.SendTest(setting, toEmail, req.Subject, req.Body); err != nil {
		switch {
		case errors.Is(err, ErrSMTPTestInvalidEmail):
			ginutil.RespondError(c, response.CodeBadRequest, "error.email_invalid", nil)
		case errors.Is(err, ErrSMTPTestRecipientRejected):
			ginutil.RespondError(c, response.CodeBadRequest, "error.email_recipient_not_found", nil)
		case errors.Is(err, ErrSMTPTestServiceDisabled),
			errors.Is(err, ErrSMTPTestServiceNotConfigured):
			ginutil.RespondError(c, response.CodeBadRequest, "error.email_service_not_configured", err)
		default:
			ginutil.RespondError(c, response.CodeInternal, "error.send_verify_code_failed", err)
		}
		return
	}

	response.Success(c, gin.H{"sent": true})
}

func smtpAuditFields(s settingsmessaging.SMTPSetting) operationaudit.Fields {
	return operationaudit.Fields{"enabled": s.Enabled, "port": s.Port, "use_tls": s.UseTLS, "use_ssl": s.UseSSL, "order_notification_enabled": s.OrderNotificationEnabled,
		"verify_expire_minutes": s.VerifyCode.ExpireMinutes, "verify_send_interval_seconds": s.VerifyCode.SendIntervalSeconds, "verify_max_attempts": s.VerifyCode.MaxAttempts, "verify_length": s.VerifyCode.Length}
}
