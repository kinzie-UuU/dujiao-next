package settingshttp

import (
	"errors"

	ginutil "github.com/dujiao-next/internal/platform/http/ginutil"

	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	"github.com/dujiao-next/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

// AffiliateAdminService 是后台推广返利设置端口。
type AffiliateAdminService interface {
	GetAffiliateSetting() (settingsintegration.AffiliateSetting, error)
	UpdateAffiliateSetting(setting settingsintegration.AffiliateSetting) (settingsintegration.AffiliateSetting, error)
}

// AffiliateHandler 处理后台推广返利设置请求。
type AffiliateHandler struct {
	affiliate AffiliateAdminService
}

func NewAffiliateHandler(affiliate AffiliateAdminService) *AffiliateHandler {
	if affiliate == nil {
		panic("settings affiliate handler: affiliate is nil")
	}
	return &AffiliateHandler{affiliate: affiliate}
}

// GetAffiliate 获取推广返利设置。
func (h *AffiliateHandler) GetAffiliate(c *gin.Context) {
	setting, err := h.affiliate.GetAffiliateSetting()
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.settings_fetch_failed", err)
		return
	}
	response.Success(c, setting)
}

// UpdateAffiliate 更新推广返利设置。
func (h *AffiliateHandler) UpdateAffiliate(c *gin.Context) {
	var req struct {
		settingsintegration.AffiliateSetting
		AllProducts *bool `json:"all_products"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	if req.AllProducts == nil {
		// Older admin clients do not know this field; saving unrelated settings
		// must not silently narrow an existing all-product reward policy.
		current, err := h.affiliate.GetAffiliateSetting()
		if err != nil {
			ginutil.RespondError(c, response.CodeInternal, "error.settings_fetch_failed", err)
			return
		}
		req.AffiliateSetting.AllProducts = current.AllProducts
	} else {
		req.AffiliateSetting.AllProducts = *req.AllProducts
	}
	setting, err := h.affiliate.UpdateAffiliateSetting(req.AffiliateSetting)
	if err != nil {
		if errors.Is(err, settingsintegration.ErrAffiliateConfigInvalid) {
			ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.settings_save_failed", err)
		return
	}
	response.Success(c, setting)
}
