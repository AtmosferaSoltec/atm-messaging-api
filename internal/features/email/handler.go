package email

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

// maxRequestBody bounds the JSON body of a send request.
const maxRequestBody = 16 << 10

// Handler serves the email endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a Handler.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// Send handles POST /v1/email.
func (h *Handler) Send(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBody)
	var req SendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "request body is not valid JSON"))
		return
	}
	e, err := h.service.Enqueue(c.Request.Context(), tenant.FromContext(c).ID, req)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusAccepted, SendResponse{ID: e.ID.String(), Status: string(StatusQueued)})
}

// Get handles GET /v1/email/:id.
func (h *Handler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apierror.Write(c, apierror.NotFound("email_not_found", "email not found"))
		return
	}
	e, err := h.service.Get(c.Request.Context(), tenant.FromContext(c).ID, id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, NewResponse(e))
}

// Metrics handles GET /admin/v1/email/metrics.
func (h *Handler) Metrics(c *gin.Context) {
	out, err := h.service.Metrics(c.Request.Context())
	if err != nil {
		apierror.Write(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func translate(err error) error {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		return apierror.New(http.StatusUnprocessableEntity, validation.Code, validation.Message)
	case errors.Is(err, ErrNotFound):
		return apierror.NotFound("email_not_found", "email not found")
	case errors.Is(err, ErrRateLimited):
		return apierror.New(http.StatusTooManyRequests, "rate_limited", "too many emails; try again later")
	case errors.Is(err, ErrUnavailable):
		return apierror.New(http.StatusServiceUnavailable, "email_unavailable", "email delivery is not configured")
	default:
		return err
	}
}
