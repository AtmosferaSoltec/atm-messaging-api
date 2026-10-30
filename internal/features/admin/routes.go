package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the administration routes. group is /admin/v1; login is
// public and everything else requires an admin token.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler, auth *AuthService) {
	// Browsers send an OPTIONS preflight before calling with an Authorization
	// header. Without a route Gin would answer 404 before the group's CORS
	// middleware runs; that middleware answers allowed origins, others get an
	// empty 204 with no CORS headers and the browser blocks the call.
	group.OPTIONS("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	group.POST("/auth/login", handler.Login)

	protected := group.Group("", RequireAdmin(auth))
	protected.POST("/auth/logout", handler.Logout)
	protected.GET("/auth/me", handler.Me)

	protected.GET("/tenants", handler.ListTenants)
	protected.POST("/tenants", handler.CreateTenant)
	protected.GET("/tenants/:id", handler.GetTenant)
	protected.PATCH("/tenants/:id", handler.UpdateTenant)
	protected.GET("/tenants/:id/webhook", handler.GetTenantWebhook)
	protected.PUT("/tenants/:id/webhook", handler.SetTenantWebhook)
	protected.POST("/tenants/:id/webhook/test", handler.TestTenantWebhook)
	protected.DELETE("/tenants/:id/webhook", handler.DeleteTenantWebhook)
	protected.GET("/tenants/:id/webhook/events", handler.TenantWebhookEvents)

	protected.GET("/sessions", handler.ListSessions)
	protected.POST("/sessions", handler.CreateSession)
	protected.POST("/sessions/:id/connect-link", handler.CreateConnectLink)
	protected.POST("/sessions/:id/disconnect", handler.DisconnectSession)
	protected.GET("/sessions/:id", handler.GetSession)
	protected.DELETE("/sessions/:id", handler.DeleteSession)

	protected.GET("/messages", handler.ListMessages)
	protected.POST("/messages", handler.SendMessage)
	protected.GET("/messages/:id", handler.GetMessage)
	protected.GET("/metrics", handler.Metrics)
	protected.GET("/metrics/activity", handler.Activity)
}
