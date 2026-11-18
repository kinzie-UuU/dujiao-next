// Package backuphttp exposes administrative Plus backup purchase and gate controls.
package backuphttp

import (
	"context"
	"fmt"
	"io"

	"github.com/dujiao-next/internal/platform/http/ginutil"
	"github.com/dujiao-next/internal/platform/http/response"
	"github.com/dujiao-next/internal/shared/operationaudit"
	"github.com/gin-gonic/gin"
)

// Trigger must validate the order, supplier, pricing and durable claim itself.
// The returned ID identifies the claim, not a completed supplier purchase.
type Trigger interface {
	TriggerPlusBackup(ctx context.Context, orderID, adminID uint) (claimID uint, err error)
}

// Gate controls the singleton backup purchase switch.
type Gate interface {
	ChangeGate(adminID uint, enable bool) error
	GateStatus() (enabled bool, err error)
}

type Handler struct {
	trigger Trigger
	gate    Gate
}

func NewHandler(trigger Trigger, gate Gate) *Handler {
	if trigger == nil || gate == nil {
		panic("backup procurement handler dependencies are required")
	}
	return &Handler{trigger: trigger, gate: gate}
}

// TriggerPlus accepts only a local order ID in :id and an empty body/query.
// Register it only behind the normal JWT, RBAC, and operation-audit middleware.
func (h *Handler) TriggerPlus(c *gin.Context) {
	if !ginutil.IsSuperAdmin(c) {
		response.Forbidden(c, "super_admin_required")
		return
	}
	adminID, ok := ginutil.GetAdminID(c)
	if !ok {
		return
	}
	if adminID == 0 {
		response.Unauthorized(c, "admin_required")
		return
	}
	orderID, err := ginutil.ParseParamUint(c, "id")
	if err != nil || orderID == 0 || c.Request.URL.RawQuery != "" {
		response.Error(c, response.CodeBadRequest, "invalid_request")
		return
	}
	if c.Request.Body != nil {
		body, readErr := io.ReadAll(io.LimitReader(c.Request.Body, 1))
		if readErr != nil || len(body) != 0 {
			response.Error(c, response.CodeBadRequest, "invalid_request")
			return
		}
	}
	audit := operationaudit.New("backup_procurement.plus_trigger", fmt.Sprint(orderID), nil)
	defer ginutil.RecordOperation(c, audit)
	claimID, err := h.trigger.TriggerPlusBackup(c.Request.Context(), orderID, adminID)
	if err != nil || claimID == 0 {
		response.Error(c, response.CodeInternal, "backup_procurement_unavailable")
		return
	}
	audit.Success(operationaudit.Fields{"claim_id": claimID})
	response.Success(c, gin.H{"claim_id": claimID, "status": "accepted"})
}

// GetGate returns whether backup purchasing is enabled.
func (h *Handler) GetGate(c *gin.Context) {
	if !ginutil.IsSuperAdmin(c) {
		response.Forbidden(c, "super_admin_required")
		return
	}
	enabled, err := h.gate.GateStatus()
	if err != nil {
		response.Error(c, response.CodeInternal, "backup_procurement_unavailable")
		return
	}
	response.Success(c, gin.H{"enabled": enabled})
}

// SetGate enables or disables backup purchasing behind an audited fence.
func (h *Handler) SetGate(c *gin.Context) {
	if !ginutil.IsSuperAdmin(c) {
		response.Forbidden(c, "super_admin_required")
		return
	}
	adminID, ok := ginutil.GetAdminID(c)
	if !ok {
		return
	}
	if adminID == 0 || c.Request.URL.RawQuery != "" {
		response.Error(c, response.CodeBadRequest, "invalid_request")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		response.Error(c, response.CodeBadRequest, "invalid_request")
		return
	}
	audit := operationaudit.New("backup_procurement.gate", "1", operationaudit.Fields{"enabled": *body.Enabled})
	defer ginutil.RecordOperation(c, audit)
	if err := h.gate.ChangeGate(adminID, *body.Enabled); err != nil {
		response.Error(c, response.CodeInternal, "backup_procurement_unavailable")
		return
	}
	audit.Success(operationaudit.Fields{"enabled": *body.Enabled})
	response.Success(c, gin.H{"enabled": *body.Enabled})
}
