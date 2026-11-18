package backuphttp

import "github.com/gin-gonic/gin"

// RegisterAdminRoutes mounts Plus backup trigger and gate controls behind the
// normal admin JWT/RBAC middleware group. Handlers still require super-admin.
func RegisterAdminRoutes(admin gin.IRoutes, handler *Handler) {
	if admin == nil || handler == nil {
		panic("backup procurement admin routes: required dependency is nil")
	}
	admin.GET("/backup-procurement/gate", handler.GetGate)
	admin.PUT("/backup-procurement/gate", handler.SetGate)
	admin.POST("/orders/:id/backup-procurement/plus", handler.TriggerPlus)
}
