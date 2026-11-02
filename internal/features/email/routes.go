package email

import "github.com/gin-gonic/gin"

// RegisterRoutes mounts the tenant routes on an authenticated /v1 group.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	group.POST("/email", handler.Send)
	group.GET("/email/:id", handler.Get)
}

// RegisterAdminRoutes mounts the admin routes on a group that already requires
// an administrator (the wiring adds the admin middleware).
func RegisterAdminRoutes(group *gin.RouterGroup, handler *Handler) {
	group.GET("/email/metrics", handler.Metrics)
}
