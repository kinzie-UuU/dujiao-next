package siteconnectionhttp

import (
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/shared/operationaudit"

	ginutil "github.com/dujiao-next/internal/platform/http/ginutil"

	siteconnectionapp "github.com/dujiao-next/internal/modules/siteconnection/application"
	siteconnectioncontract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

// AdminService 是后台站点对接连接端口。
type AdminService interface {
	List(filter siteconnectioncontract.ListFilter) ([]siteconnectiondomain.Connection, int64, error)
	GetByID(id uint) (*siteconnectiondomain.Connection, error)
	Create(input siteconnectionapp.CreateInput) (*siteconnectiondomain.Connection, error)
	Update(id uint, input siteconnectionapp.UpdateInput) (*siteconnectiondomain.Connection, error)
	Delete(id uint) error
	Ping(id uint) (*siteconnectionapp.PingResult, error)
	SetStatus(id uint, status string) error
}

// MarkupReapplier 对连接已映射商品重新应用加价。
type MarkupReapplier interface {
	ReapplyMarkup(connectionID uint) (int, error)
}

type updateStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// AdminHandler 处理后台站点对接连接请求。
type AdminHandler struct {
	connections AdminService
	markup      MarkupReapplier
}

func NewAdminHandler(connections AdminService, markup MarkupReapplier) *AdminHandler {
	if connections == nil {
		panic("siteconnection admin handler: connections is nil")
	}
	if markup == nil {
		panic("siteconnection admin handler: markup is nil")
	}
	return &AdminHandler{connections: connections, markup: markup}
}

// GetSiteConnections 获取对接连接列表
func (h *AdminHandler) GetSiteConnections(c *gin.Context) {
	page, pageSize := ginutil.ParsePagination(c)

	conns, total, err := h.connections.List(siteconnectioncontract.ListFilter{
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.connection_fetch_failed", err)
		return
	}

	pagination := response.BuildPagination(page, pageSize, total)
	response.SuccessWithPage(c, conns, pagination)
}

// GetSiteConnection 获取对接连接详情
func (h *AdminHandler) GetSiteConnection(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	conn, err := h.connections.GetByID(id)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.connection_fetch_failed", err)
		return
	}
	if conn == nil {
		ginutil.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
		return
	}

	response.Success(c, conn)
}

// CreateSiteConnection 创建对接连接
func (h *AdminHandler) CreateSiteConnection(c *gin.Context) {
	var input siteconnectionapp.CreateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	audit := operationaudit.New("connection.create", "new", nil)
	defer ginutil.RecordOperation(c, audit)
	conn, err := h.connections.Create(input)
	if err != nil {
		if errors.Is(err, siteconnectioncontract.ErrInvalid) {
			ginutil.RespondError(c, response.CodeBadRequest, "error.connection_invalid", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.connection_create_failed", err)
		return
	}

	audit.Target = fmt.Sprint(conn.ID)
	audit.Success(connectionAuditFields(conn))
	response.Success(c, conn)
}

// UpdateSiteConnection 更新对接连接
func (h *AdminHandler) UpdateSiteConnection(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	var input siteconnectionapp.UpdateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	old, readErr := h.connections.GetByID(id)
	if old != nil {
		snapshot := *old
		old = &snapshot
	}
	before := operationaudit.Fields{"snapshot_available": false}
	if readErr == nil && old != nil {
		before = connectionAuditFields(old)
	}
	audit := operationaudit.New("connection.update", fmt.Sprint(id), before)
	defer ginutil.RecordOperation(c, audit)
	conn, err := h.connections.Update(id, input)
	if err != nil {
		if errors.Is(err, siteconnectioncontract.ErrNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.connection_update_failed", err)
		return
	}

	after := connectionAuditFields(conn)
	if readErr == nil && old != nil {
		after["api_key_changed"] = old.ApiKey != conn.ApiKey
		after["api_secret_changed"] = old.ApiSecret != conn.ApiSecret
		after["base_url_changed"] = old.BaseURL != conn.BaseURL
		after["callback_url_changed"] = old.CallbackURL != conn.CallbackURL
		after["name_changed"] = old.Name != conn.Name
	}
	audit.Success(after)
	response.Success(c, conn)
}

// DeleteSiteConnection 删除对接连接
func (h *AdminHandler) DeleteSiteConnection(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	if err := h.connections.Delete(id); err != nil {
		if errors.Is(err, siteconnectioncontract.ErrNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.connection_delete_failed", err)
		return
	}

	response.Success(c, gin.H{"deleted": true})
}

// PingSiteConnection 测试对接连接
func (h *AdminHandler) PingSiteConnection(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	result, err := h.connections.Ping(id)
	if err != nil {
		if errors.Is(err, siteconnectioncontract.ErrNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
			return
		}
		ginutil.RespondErrorWithMsg(c, response.CodeInternal, err.Error(), err)
		return
	}

	response.Success(c, result)
}

// ReapplyConnectionMarkup 对连接的所有映射商品重新应用加价规则
func (h *AdminHandler) ReapplyConnectionMarkup(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	before := operationaudit.Fields{"snapshot_available": false}
	snapshotter, hasSnapshot := h.markup.(interface {
		MarkupAuditSnapshot(uint) operationaudit.Fields
	})
	if hasSnapshot {
		before = snapshotter.MarkupAuditSnapshot(id)
	}
	audit := operationaudit.New("connection.reapply_markup", fmt.Sprint(id), before)
	defer ginutil.RecordOperation(c, audit)
	count, err := h.markup.ReapplyMarkup(id)
	if err != nil {
		if errors.Is(err, siteconnectioncontract.ErrNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.reapply_markup_failed", err)
		return
	}

	after := operationaudit.Fields{"snapshot_available": false}
	if hasSnapshot {
		after = snapshotter.MarkupAuditSnapshot(id)
	}
	after["reported_updated_products"] = count
	audit.After = after
	// The existing service suppresses individual write failures; never label this as verified success.
	audit.Result = "completed_unverified"
	response.Success(c, gin.H{"updated_products": count})
}

// UpdateSiteConnectionStatus 更新连接状态
func (h *AdminHandler) UpdateSiteConnectionStatus(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	var req updateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	before := operationaudit.Fields{"snapshot_available": false}
	if old, readErr := h.connections.GetByID(id); readErr == nil && old != nil {
		before = operationaudit.Fields{"status": old.Status}
	}
	audit := operationaudit.New("connection.status", fmt.Sprint(id), before)
	defer ginutil.RecordOperation(c, audit)
	if err := h.connections.SetStatus(id, req.Status); err != nil {
		if errors.Is(err, siteconnectioncontract.ErrNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.connection_update_failed", err)
		return
	}

	audit.Success(operationaudit.Fields{"status": req.Status})
	response.Success(c, gin.H{"updated": true})
}

func connectionAuditFields(v *siteconnectiondomain.Connection) operationaudit.Fields {
	return operationaudit.Fields{"status": v.Status, "protocol": v.Protocol, "retry_max": v.RetryMax,
		"exchange_rate": v.ExchangeRate.String(), "price_markup_percent": v.PriceMarkupPercent.String(), "price_rounding_mode": v.PriceRoundingMode, "auto_sync_price": v.AutoSyncPrice}
}
