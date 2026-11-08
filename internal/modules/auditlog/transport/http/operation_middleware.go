package auditloghttp

import (
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/modules/auditlog/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/operationaudit"
	"github.com/gin-gonic/gin"
	"net"
	"time"
)

type OperationRecorder interface {
	RecordOperation(*domain.AdminOperationLog) error
}

func OperationMiddleware(store OperationRecorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil {
			c.Next()
			return
		}
		c.Set("admin_operation_recorder", func(event *operationaudit.Event) {
			clientIP := ""
			if parsed := net.ParseIP(c.ClientIP()); parsed != nil {
				clientIP = parsed.String()
			}
			row := &domain.AdminOperationLog{ClientIP: clientIP, OperatorAdminID: c.GetUint("admin_id"),
				OperatorUsername: c.GetString("username"), RequestID: c.GetString("request_id"),
				Action: event.Action, Target: event.Target, Result: event.Result,
				BeforeJSON: jsonmap.JSON(event.Before), AfterJSON: jsonmap.JSON(event.After), CreatedAt: time.Now()}
			if row.OperatorAdminID == 0 {
				return
			}
			if err := store.RecordOperation(row); err != nil {
				// Database errors can include SQL values; keep the fallback strictly allowlisted.
				logger.Errorw("admin_operation_audit_write_failed", "request_id", row.RequestID,
					"action", row.Action, "target", row.Target, "operator_admin_id", row.OperatorAdminID)
			}
		})
		c.Next()
	}
}
