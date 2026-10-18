package settingshttp

import (
	"strings"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/constants"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	ginutil "github.com/dujiao-next/internal/platform/http/ginutil"
	"github.com/dujiao-next/internal/platform/http/response"
	"github.com/dujiao-next/internal/shared/jsonmap"

	"github.com/gin-gonic/gin"
)

// AdminService 是后台通用设置端口。
type AdminService interface {
	GetByKey(key string) (jsonmap.JSON, error)
	UpdateWithEffects(key string, value map[string]interface{}) (settingsapp.UpdateResult, error)
	InvalidateCallbackRoutesCache()
}

// AdminHandler 处理后台通用设置请求。
type AdminHandler struct {
	settings AdminService
}

func NewAdminHandler(settings AdminService) *AdminHandler {
	if settings == nil {
		panic("settings admin handler: settings is nil")
	}
	return &AdminHandler{settings: settings}
}

type updateRequest struct {
	Key   string                 `json:"key" binding:"required"`
	Value map[string]interface{} `json:"value" binding:"required"`
}

// Get 获取设置。
func (h *AdminHandler) Get(c *gin.Context) {
	key := c.DefaultQuery("key", constants.SettingKeySiteConfig)

	value, err := h.settings.GetByKey(key)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_fetch_failed", err)
		return
	}
	if value == nil {
		response.Success(c, gin.H{})
		return
	}

	response.Success(c, value)
}

// dedicatedSettingEndpoints 有专用接口的设置：专用接口负责校验、归一化与运行时生效（如 SMTP、登录方式），
// 走通用接口会跳过这些步骤，导致配置不生效或写入未校验的值。
var dedicatedSettingEndpoints = map[string]string{
	constants.SettingKeySMTPConfig:               "/admin/settings/smtp",
	constants.SettingKeyCaptchaConfig:            "/admin/settings/captcha",
	constants.SettingKeyTelegramAuthConfig:       "/admin/settings/telegram-auth",
	constants.SettingKeyGoogleAuthConfig:         "/admin/settings/google-auth",
	constants.SettingKeyAffiliateConfig:          "/admin/settings/affiliate",
	constants.SettingKeyOrderEmailTemplateConfig: "/admin/settings/order-email-template",
	constants.SettingKeyTelegramBotConfig:        "/admin/settings/telegram-bot",
	constants.SettingKeyNotificationCenterConfig: "/admin/settings/notification-center",
}

// Update 更新设置。
func (h *AdminHandler) Update(c *gin.Context) {
	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	key := strings.TrimSpace(req.Key)
	if endpoint, ok := dedicatedSettingEndpoints[key]; ok {
		ginutil.RespondErrorWithMsg(
			c,
			response.CodeBadRequest,
			key+" must be updated through "+endpoint,
			nil,
		)
		return
	}

	result, err := h.settings.UpdateWithEffects(key, req.Value)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_save_failed", err)
		return
	}

	if result.HasEffect(settingsapp.EffectInvalidatePublicConfigCache) {
		_ = cache.DelAllPublicConfig(c.Request.Context())
	}
	if result.HasEffect(settingsapp.EffectInvalidateCallbackRoutesCache) {
		h.settings.InvalidateCallbackRoutesCache()
	}
	response.Success(c, result.Value)
}
