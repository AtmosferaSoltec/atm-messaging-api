package session

import "github.com/gin-gonic/gin"

// RegisterRoutes mounts the session routes on an authenticated router group.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	sessions := group.Group("/sessions")
	sessions.POST("", handler.Create)
	sessions.GET("", handler.List)
	sessions.GET("/:id", handler.Get)
	sessions.GET("/:id/qr/stream", handler.QRStream)
	sessions.DELETE("/:id", handler.Delete)
}
