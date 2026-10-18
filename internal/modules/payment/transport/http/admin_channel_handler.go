package paymenthttp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	ginutil "github.com/dujiao-next/internal/platform/http/ginutil"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/platform/http/response"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/gin-gonic/gin"
)

const redactedPaymentConfigValue = "••••••••"

var sensitivePaymentConfigKeys = map[string]struct{}{
	"api_secret":           {},
	"api_v3_key":           {},
	"auth_token":           {},
	"client_secret":        {},
	"merchant_key":         {},
	"merchant_private_key": {},
	"merchant_token":       {},
	"notify_secret":        {},
	"private_key":          {},
	"secret_key":           {},
	"webhook_secret":       {},
}

// AdminChannelListFilter 后台支付渠道列表过滤条件。
type AdminChannelListFilter struct {
	Page         int
	PageSize     int
	ProviderType string
	ChannelType  string
	ActiveOnly   bool
}

// AdminChannelCatalog 后台支付渠道管理端口。
type AdminChannelCatalog interface {
	ValidateChannel(channel *paymentdomain.PaymentChannel) error
	GetChannel(id uint) (*paymentdomain.PaymentChannel, error)
	ListChannels(filter AdminChannelListFilter) ([]paymentdomain.PaymentChannel, int64, error)
	Create(channel *paymentdomain.PaymentChannel) error
	Update(channel *paymentdomain.PaymentChannel) error
	Delete(id uint) error
}

// CreatePaymentChannelRequest 创建支付渠道请求
type CreatePaymentChannelRequest struct {
	Name               string                 `json:"name" binding:"required"`
	Icon               *string                `json:"icon"`
	ProviderType       string                 `json:"provider_type" binding:"required"`
	ChannelType        string                 `json:"channel_type" binding:"required"`
	InteractionMode    string                 `json:"interaction_mode" binding:"required"`
	FeeRate            *money.Amount          `json:"fee_rate"`
	FixedFee           *money.Amount          `json:"fixed_fee"`
	MinAmount          *money.Amount          `json:"min_amount"`
	MaxAmount          *money.Amount          `json:"max_amount"`
	HideAmountOutRange *bool                  `json:"hide_amount_out_range"`
	PaymentRoles       []string               `json:"payment_roles"`
	MemberLevels       []uint                 `json:"member_levels"`
	PaymentTypes       []string               `json:"payment_types"`
	ConfigJSON         map[string]interface{} `json:"config_json"`
	IsActive           *bool                  `json:"is_active"`
	SortOrder          int                    `json:"sort_order"`
}

// UpdatePaymentChannelRequest 更新支付渠道请求
type UpdatePaymentChannelRequest struct {
	Name               string                 `json:"name"`
	Icon               *string                `json:"icon"`
	ProviderType       string                 `json:"provider_type"`
	ChannelType        string                 `json:"channel_type"`
	InteractionMode    string                 `json:"interaction_mode"`
	FeeRate            *money.Amount          `json:"fee_rate"`
	FixedFee           *money.Amount          `json:"fixed_fee"`
	MinAmount          *money.Amount          `json:"min_amount"`
	MaxAmount          *money.Amount          `json:"max_amount"`
	HideAmountOutRange *bool                  `json:"hide_amount_out_range"`
	PaymentRoles       []string               `json:"payment_roles"`
	MemberLevels       []uint                 `json:"member_levels"`
	PaymentTypes       []string               `json:"payment_types"`
	ConfigJSON         map[string]interface{} `json:"config_json"`
	IsActive           *bool                  `json:"is_active"`
	SortOrder          *int                   `json:"sort_order"`
}

// AdminChannelHandler 处理后台支付渠道 HTTP。
type AdminChannelSecurity interface {
	Verify(context.Context, uint, string) error
	Changed(uint, string, string, uint, []string)
}

type AdminChannelHandler struct {
	security AdminChannelSecurity
	channels AdminChannelCatalog
}

func NewAdminChannelHandler(channels AdminChannelCatalog, security AdminChannelSecurity) *AdminChannelHandler {
	if channels == nil {
		panic("payment admin channel handler: channels is nil")
	}
	return &AdminChannelHandler{channels: channels, security: security}
}

// CreatePaymentChannel 创建支付渠道
func (h *AdminChannelHandler) CreatePaymentChannel(c *gin.Context) {
	if !h.authorizeWrite(c) {
		return
	}
	var req CreatePaymentChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	channel := &paymentdomain.PaymentChannel{
		Name:            req.Name,
		ProviderType:    req.ProviderType,
		ChannelType:     req.ChannelType,
		InteractionMode: req.InteractionMode,
		ConfigJSON:      jsonmap.JSON(req.ConfigJSON),
		PaymentRoles:    req.PaymentRoles,
		MemberLevels:    req.MemberLevels,
		PaymentTypes:    req.PaymentTypes,
		SortOrder:       req.SortOrder,
		IsActive:        true,
	}
	if req.Icon != nil {
		channel.Icon = *req.Icon
	}
	if req.IsActive != nil {
		channel.IsActive = *req.IsActive
	}
	if req.HideAmountOutRange != nil {
		channel.HideAmountOutRange = *req.HideAmountOutRange
	}
	if req.FeeRate != nil {
		channel.FeeRate = *req.FeeRate
	}
	if req.FixedFee != nil {
		channel.FixedFee = *req.FixedFee
	}
	if req.MinAmount != nil {
		channel.MinAmount = *req.MinAmount
	}
	if req.MaxAmount != nil {
		channel.MaxAmount = *req.MaxAmount
	}

	if err := h.channels.ValidateChannel(channel); err != nil {
		switch {
		case errors.Is(err, ErrPaymentProviderNotSupported):
			ginutil.RespondError(c, response.CodeBadRequest, "error.payment_provider_not_supported", nil)
		case errors.Is(err, ErrPaymentChannelConfigInvalid):
			ginutil.RespondError(c, response.CodeBadRequest, "error.payment_channel_config_invalid", nil)
		default:
			ginutil.RespondError(c, response.CodeBadRequest, "error.payment_channel_invalid", nil)
		}
		return
	}

	if err := h.channels.Create(channel); err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_channel_create_failed", err)
		return
	}
	_ = cache.DelAllPublicConfig(c.Request.Context())

	h.security.Changed(adminID(c), c.ClientIP(), "create", channel.ID, channelChangedFields(nil, channel))
	response.Success(c, redactPaymentChannel(channel))
}

// UpdatePaymentChannel 更新支付渠道
func (h *AdminChannelHandler) UpdatePaymentChannel(c *gin.Context) {
	if !h.authorizeWrite(c) {
		return
	}
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_channel_invalid", nil)
		return
	}

	var req UpdatePaymentChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	channel, err := h.channels.GetChannel(id)
	if err != nil {
		switch {
		case errors.Is(err, ErrPaymentChannelNotFound):
			ginutil.RespondError(c, response.CodeNotFound, "error.payment_channel_not_found", nil)
		default:
			ginutil.RespondError(c, response.CodeInternal, "error.payment_channel_update_failed", err)
		}
		return
	}

	before := *channel
	if req.Name != "" {
		channel.Name = req.Name
	}
	if req.Icon != nil {
		channel.Icon = *req.Icon
	}
	if req.ProviderType != "" {
		channel.ProviderType = req.ProviderType
	}
	if req.ChannelType != "" {
		channel.ChannelType = req.ChannelType
	}
	if req.InteractionMode != "" {
		channel.InteractionMode = req.InteractionMode
	}
	if req.FeeRate != nil {
		channel.FeeRate = *req.FeeRate
	}
	if req.FixedFee != nil {
		channel.FixedFee = *req.FixedFee
	}
	if req.MinAmount != nil {
		channel.MinAmount = *req.MinAmount
	}
	if req.MaxAmount != nil {
		channel.MaxAmount = *req.MaxAmount
	}
	if req.PaymentRoles != nil {
		channel.PaymentRoles = req.PaymentRoles
	}
	if req.MemberLevels != nil {
		channel.MemberLevels = req.MemberLevels
	}
	if req.PaymentTypes != nil {
		channel.PaymentTypes = req.PaymentTypes
	}
	if req.ConfigJSON != nil {
		channel.ConfigJSON = mergePaymentChannelConfig(channel.ConfigJSON, req.ConfigJSON)
	}
	if req.IsActive != nil {
		channel.IsActive = *req.IsActive
	}
	if req.HideAmountOutRange != nil {
		channel.HideAmountOutRange = *req.HideAmountOutRange
	}
	if req.SortOrder != nil {
		channel.SortOrder = *req.SortOrder
	}

	if err := h.channels.ValidateChannel(channel); err != nil {
		switch {
		case errors.Is(err, ErrPaymentProviderNotSupported):
			ginutil.RespondError(c, response.CodeBadRequest, "error.payment_provider_not_supported", nil)
		case errors.Is(err, ErrPaymentChannelConfigInvalid):
			ginutil.RespondError(c, response.CodeBadRequest, "error.payment_channel_config_invalid", nil)
		default:
			ginutil.RespondError(c, response.CodeBadRequest, "error.payment_channel_invalid", nil)
		}
		return
	}

	if err := h.channels.Update(channel); err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_channel_update_failed", err)
		return
	}
	_ = cache.DelAllPublicConfig(c.Request.Context())

	h.security.Changed(adminID(c), c.ClientIP(), "update", channel.ID, channelChangedFields(&before, channel))
	response.Success(c, redactPaymentChannel(channel))
}

// DeletePaymentChannel 删除支付渠道
func (h *AdminChannelHandler) DeletePaymentChannel(c *gin.Context) {
	if !h.authorizeWrite(c) {
		return
	}
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_channel_invalid", nil)
		return
	}

	if err := h.channels.Delete(id); err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_channel_delete_failed", err)
		return
	}
	_ = cache.DelAllPublicConfig(c.Request.Context())

	h.security.Changed(adminID(c), c.ClientIP(), "delete", id, []string{"channel"})
	response.Success(c, gin.H{"deleted": true})
}

// GetPaymentChannel 获取支付渠道详情
func (h *AdminChannelHandler) GetPaymentChannel(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_channel_invalid", nil)
		return
	}

	channel, err := h.channels.GetChannel(id)
	if err != nil {
		switch {
		case errors.Is(err, ErrPaymentChannelNotFound):
			ginutil.RespondError(c, response.CodeNotFound, "error.payment_channel_not_found", nil)
		default:
			ginutil.RespondError(c, response.CodeInternal, "error.payment_channel_fetch_failed", err)
		}
		return
	}

	response.Success(c, redactPaymentChannel(channel))
}

// GetPaymentChannels 获取支付渠道列表
func (h *AdminChannelHandler) GetPaymentChannels(c *gin.Context) {
	page, pageSize := ginutil.ParsePagination(c)

	providerType := c.Query("provider_type")
	channelType := c.Query("channel_type")
	activeOnly, err := ginutil.ParseQueryBool(c, "active_only")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	channels, total, err := h.channels.ListChannels(AdminChannelListFilter{
		Page:         page,
		PageSize:     pageSize,
		ProviderType: providerType,
		ChannelType:  channelType,
		ActiveOnly:   activeOnly,
	})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_channel_fetch_failed", err)
		return
	}

	redacted := make([]paymentdomain.PaymentChannel, 0, len(channels))
	for idx := range channels {
		redacted = append(redacted, *redactPaymentChannel(&channels[idx]))
	}
	pagination := response.BuildPagination(page, pageSize, total)
	response.SuccessWithPage(c, redacted, pagination)
}

func isSensitivePaymentConfigKey(key string) bool {
	_, ok := sensitivePaymentConfigKeys[strings.ToLower(strings.TrimSpace(key))]
	return ok
}

func redactPaymentChannel(channel *paymentdomain.PaymentChannel) *paymentdomain.PaymentChannel {
	if channel == nil {
		return nil
	}
	redacted := *channel
	if channel.ConfigJSON == nil {
		return &redacted
	}
	redacted.ConfigJSON = make(jsonmap.JSON, len(channel.ConfigJSON))
	for key, value := range channel.ConfigJSON {
		if isSensitivePaymentConfigKey(key) {
			if strings.TrimSpace(asConfigString(value)) != "" {
				redacted.ConfigJSON[key] = redactedPaymentConfigValue
			}
			continue
		}
		redacted.ConfigJSON[key] = value
	}
	return &redacted
}

func mergePaymentChannelConfig(existing jsonmap.JSON, incoming map[string]interface{}) jsonmap.JSON {
	merged := make(jsonmap.JSON, len(incoming)+len(existing))
	explicitlyCleared := make(map[string]struct{})
	for key, value := range incoming {
		if isSensitivePaymentConfigKey(key) {
			if value == nil {
				explicitlyCleared[key] = struct{}{}
				continue
			}
			trimmed := strings.TrimSpace(asConfigString(value))
			if trimmed == "" || trimmed == redactedPaymentConfigValue {
				if current, ok := existing[key]; ok {
					merged[key] = current
				}
				continue
			}
		}
		merged[key] = value
	}
	for key, value := range existing {
		if !isSensitivePaymentConfigKey(key) {
			continue
		}
		if _, cleared := explicitlyCleared[key]; cleared {
			continue
		}
		if _, exists := merged[key]; !exists {
			merged[key] = value
		}
	}
	return merged
}

func asConfigString(value interface{}) string {
	text, _ := value.(string)
	return text
}

func adminID(c *gin.Context) uint { id, _ := ginutil.GetAdminID(c); return id }
func (h *AdminChannelHandler) authorizeWrite(c *gin.Context) bool {
	id, ok := ginutil.GetAdminID(c)
	if !ok {
		return false
	}
	if h.security == nil || h.security.Verify(c.Request.Context(), id, c.GetHeader("X-Admin-TOTP")) != nil {
		response.ErrorWithHTTPStatus(c, 403, response.CodeForbidden, "收款变更需要已绑定验证器的新六位验证码，且安全通知服务必须可用；请等待新验证码后重试。")
		return false
	}
	return true
}

// Compare only schema field names. Configuration values never enter notices.
func channelChangedFields(before, after *paymentdomain.PaymentChannel) []string {
	names := []string{"name", "icon", "provider_type", "channel_type", "interaction_mode", "fee_rate", "fixed_fee", "min_amount", "max_amount", "hide_amount_out_range", "payment_roles", "member_levels", "payment_types", "config_json", "is_active", "sort_order"}
	toMap := func(c *paymentdomain.PaymentChannel) map[string]interface{} {
		b, _ := json.Marshal(c)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		return m
	}
	old, next := toMap(before), toMap(after)
	changed := []string{}
	for _, key := range names {
		if before == nil || !reflect.DeepEqual(old[key], next[key]) {
			changed = append(changed, key)
		}
	}
	return changed
}
