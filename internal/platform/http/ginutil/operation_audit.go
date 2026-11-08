package ginutil

import (
	"github.com/dujiao-next/internal/shared/operationaudit"
	"github.com/gin-gonic/gin"
)

// RecordOperation records only explicit handler events, never request/response bodies.
// Call with defer immediately before the business operation; the default result is failed.
func RecordOperation(c *gin.Context, event *operationaudit.Event) {
	if recorder, ok := c.Get("admin_operation_recorder"); ok {
		if record, ok := recorder.(func(*operationaudit.Event)); ok {
			record(event)
		}
	}
}
