package message

import "github.com/gin-gonic/gin"

// RegisterRoutes mounts the message routes on an authenticated router group.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	messages := group.Group("/messages")
	messages.POST("", handler.Send)
	messages.GET("/:id", handler.Get)
}
