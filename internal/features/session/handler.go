package session

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/account"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

// Handler serves the session endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Create handles POST /v1/sessions.
func (h *Handler) Create(c *gin.Context) {
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "account_ref and channel are required"))
		return
	}

	t := tenant.FromContext(c)
	sess, err := h.service.Create(c.Request.Context(), t.ID, req.AccountRef, channel.Type(req.Channel))
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusCreated, NewResponse(sess))
}

// List handles GET /v1/sessions?account_ref=.
func (h *Handler) List(c *gin.Context) {
	t := tenant.FromContext(c)
	sessions, err := h.service.List(c.Request.Context(), t.ID, c.Query("account_ref"))
	if err != nil {
		apierror.Write(c, err)
		return
	}
	items := make([]Response, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, NewResponse(s))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Get handles GET /v1/sessions/:id.
func (h *Handler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	sess, err := h.service.Get(c.Request.Context(), tenant.FromContext(c).ID, id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, NewResponse(sess))
}

// Delete handles DELETE /v1/sessions/:id.
func (h *Handler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), tenant.FromContext(c).ID, id); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// QRStream handles GET /v1/sessions/:id/qr/stream as Server-Sent Events.
//
// Events: `qr` (data: the code to render), `connected` and `closed`
// (data: the reason). The stream ends after `connected` or `closed`.
func (h *Handler) QRStream(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	sess, events, unsubscribe, err := h.service.Watch(c.Request.Context(), tenant.FromContext(c).ID, id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	defer unsubscribe()

	StreamQR(c, sess, events)
}

// StreamQR writes a session's QR/connection events to the response as
// Server-Sent Events until it connects, closes or the client leaves. The
// events are `qr` (data: the code to render), `connected` and `closed`
// (data: the reason). It is shared with the public connect-link endpoint.
func StreamQR(c *gin.Context, sess Session, events <-chan channel.Event) {
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	if sess.Status == StatusConnected {
		c.SSEvent("connected", "already connected")
		return
	}
	if sess.Status == StatusBanned {
		c.SSEvent("closed", "banned")
		return
	}

	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case evt := <-events:
			switch evt.Kind {
			case channel.EventQR:
				c.SSEvent("qr", evt.Code)
				return true
			case channel.EventConnected:
				c.SSEvent("connected", "connected")
				return false
			case channel.EventPairingFailed, channel.EventLoggedOut, channel.EventBanned:
				c.SSEvent("closed", string(evt.Kind))
				return false
			}
			return true
		}
	})
}

func parseID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apierror.Write(c, apierror.NotFound("session_not_found", "session not found"))
		return uuid.Nil, false
	}
	return id, true
}

// translate maps domain errors to API errors; unknown errors pass through.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return apierror.NotFound("session_not_found", "session not found")
	case errors.Is(err, ErrSessionExists):
		return apierror.Conflict("session_exists", "this account already has a session for this channel")
	case errors.Is(err, ErrUnsupportedChannel):
		return apierror.BadRequest("unsupported_channel", "unsupported channel")
	case errors.Is(err, account.ErrInvalidExternalRef):
		return apierror.BadRequest("invalid_account_ref", err.Error())
	}
	return err
}
