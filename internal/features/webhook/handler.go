package webhook

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

// SetRequest is the body of PUT /v1/webhook.
type SetRequest struct {
	URL string `json:"url" binding:"required"`
}

// ConfigResponse is the tenant's webhook configuration.
type ConfigResponse struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

// EventResponse is the public representation of a webhook event.
type EventResponse struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
}

// NewEventResponse maps an Event to its public representation.
func NewEventResponse(e Event) EventResponse {
	return EventResponse{
		ID: e.ID.String(), Type: e.Type, Status: string(e.Status), Attempts: e.Attempts,
		LastError: e.LastError, CreatedAt: e.CreatedAt, DeliveredAt: e.DeliveredAt,
	}
}

// Handler serves the tenant-facing webhook endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts the webhook routes on an authenticated router group.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	hooks := group.Group("/webhook")
	hooks.GET("", handler.Get)
	hooks.PUT("", handler.Set)
	hooks.DELETE("", handler.Delete)
	hooks.POST("/test", handler.Test)
	hooks.GET("/events", handler.Events)
}

// Get handles GET /v1/webhook.
func (h *Handler) Get(c *gin.Context) {
	url, secret, err := h.service.Config(c.Request.Context(), tenant.FromContext(c).ID)
	if err != nil {
		apierror.Write(c, err)
		return
	}
	c.JSON(http.StatusOK, ConfigResponse{URL: url, Secret: secret})
}

// Set handles PUT /v1/webhook.
func (h *Handler) Set(c *gin.Context) {
	var req SetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "url is required"))
		return
	}
	t := tenant.FromContext(c)
	if err := h.service.SetURL(c.Request.Context(), t.ID, req.URL); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, ConfigResponse{URL: req.URL, Secret: t.WebhookSecret})
}

// Delete handles DELETE /v1/webhook: it turns webhooks off.
func (h *Handler) Delete(c *gin.Context) {
	if err := h.service.SetURL(c.Request.Context(), tenant.FromContext(c).ID, ""); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// Test handles POST /v1/webhook/test: it queues a webhook.test event.
func (h *Handler) Test(c *gin.Context) {
	t := tenant.FromContext(c)
	url, _, err := h.service.Config(c.Request.Context(), t.ID)
	if err != nil {
		apierror.Write(c, err)
		return
	}
	if url == "" {
		apierror.Write(c, apierror.Conflict("webhook_not_configured", "set a webhook url first"))
		return
	}
	if err := h.service.Notify(c.Request.Context(), t.ID, TestEventType, gin.H{"message": "webhook test"}); err != nil {
		apierror.Write(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

// Events handles GET /v1/webhook/events?limit=: recent deliveries, to debug a receiver.
func (h *Handler) Events(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	events, err := h.service.Recent(c.Request.Context(), tenant.FromContext(c).ID, limit)
	if err != nil {
		apierror.Write(c, err)
		return
	}
	items := make([]EventResponse, 0, len(events))
	for _, e := range events {
		items = append(items, NewEventResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func translate(err error) error {
	if errors.Is(err, ErrInvalidURL) {
		return apierror.BadRequest("invalid_url", err.Error())
	}
	return err
}
