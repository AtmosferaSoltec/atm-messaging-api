package message

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

// maxRequestBody bounds the JSON body: a 16 MiB attachment grows by a third in base64.
const maxRequestBody = 24 << 20

// Handler serves the message endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Send handles POST /v1/messages.
func (h *Handler) Send(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBody)

	var req SendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierror.Write(c, apierror.New(http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large"))
			return
		}
		apierror.Write(c, apierror.BadRequest("invalid_request", "request body is not valid JSON"))
		return
	}

	msg, err := h.service.Send(c.Request.Context(), tenant.FromContext(c).ID, req)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusAccepted, SendResponse{MessageID: msg.ID.String(), Status: string(msg.Status)})
}

// Get handles GET /v1/messages/:id.
func (h *Handler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apierror.Write(c, apierror.NotFound("message_not_found", "message not found"))
		return
	}
	msg, err := h.service.Get(c.Request.Context(), tenant.FromContext(c).ID, id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, NewResponse(msg))
}

// translate maps domain errors to API errors; unknown errors pass through.
func translate(err error) error {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		return apierror.BadRequest(validation.Code, validation.Message)
	case errors.Is(err, ErrNotFound):
		return apierror.NotFound("message_not_found", "message not found")
	case errors.Is(err, ErrSessionNotFound):
		return apierror.NotFound("session_not_found", "this account has no session for the channel")
	case errors.Is(err, ErrRecipientOptedOut):
		return apierror.New(http.StatusUnprocessableEntity, "recipient_opted_out", "the recipient opted out of promotional messages")
	case errors.Is(err, ErrSessionNotConnected):
		return apierror.Conflict("session_not_connected", "the session is not connected; scan the QR code first")
	}
	return err
}
