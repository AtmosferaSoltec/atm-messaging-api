package optout

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

// AddRequest is the body of POST /v1/sessions/:id/opt-outs.
type AddRequest struct {
	To string `json:"to" binding:"required"`
}

// Response is the public representation of an opt-out.
type Response struct {
	To        string    `json:"to"`
	CreatedAt time.Time `json:"created_at"`
}

// Handler serves the opt-out endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts the opt-out routes on an authenticated router group.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	group.POST("/sessions/:id/opt-outs", handler.Add)
	group.GET("/sessions/:id/opt-outs", handler.List)
	group.DELETE("/sessions/:id/opt-outs/:to", handler.Remove)
}

// Add handles POST /v1/sessions/:id/opt-outs.
func (h *Handler) Add(c *gin.Context) {
	sessionID, ok := parseSessionID(c)
	if !ok {
		return
	}
	var req AddRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "to is required"))
		return
	}
	if err := h.service.Add(c.Request.Context(), tenant.FromContext(c).ID, sessionID, req.To); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// Remove handles DELETE /v1/sessions/:id/opt-outs/:to.
func (h *Handler) Remove(c *gin.Context) {
	sessionID, ok := parseSessionID(c)
	if !ok {
		return
	}
	if err := h.service.Remove(c.Request.Context(), tenant.FromContext(c).ID, sessionID, c.Param("to")); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// List handles GET /v1/sessions/:id/opt-outs.
func (h *Handler) List(c *gin.Context) {
	sessionID, ok := parseSessionID(c)
	if !ok {
		return
	}
	entries, err := h.service.List(c.Request.Context(), tenant.FromContext(c).ID, sessionID)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	items := make([]Response, 0, len(entries))
	for _, e := range entries {
		items = append(items, Response{To: e.Recipient, CreatedAt: e.CreatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func parseSessionID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apierror.Write(c, apierror.NotFound("session_not_found", "session not found"))
		return uuid.Nil, false
	}
	return id, true
}

func translate(err error) error {
	switch {
	case errors.Is(err, ErrSessionNotFound):
		return apierror.NotFound("session_not_found", "session not found")
	case errors.Is(err, ErrInvalidRecipient):
		return apierror.BadRequest("invalid_recipient", err.Error())
	}
	return err
}
