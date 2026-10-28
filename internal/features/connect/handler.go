package connect

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/session"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

// InfoResponse is what the public connect page shows. It deliberately has no
// tenant, account or session identifiers.
type InfoResponse struct {
	Label     string    `json:"label"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Handler serves the public connect-link endpoints (no authentication: the
// token in the URL is the credential).
type Handler struct {
	service *Service
}

// NewHandler creates a Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts the public routes on a router group such as /public.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	group.GET("/connect/:token", handler.Info)
	group.GET("/connect/:token/qr/stream", handler.QRStream)
}

// Info handles GET /public/connect/:token.
func (h *Handler) Info(c *gin.Context) {
	info, _, err := h.service.Resolve(c.Request.Context(), c.Param("token"))
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, InfoResponse{
		Label: info.Label, Channel: string(info.Channel), Status: string(info.Status), ExpiresAt: info.ExpiresAt,
	})
}

// QRStream handles GET /public/connect/:token/qr/stream as Server-Sent Events,
// with the same events as the tenant-facing QR stream.
func (h *Handler) QRStream(c *gin.Context) {
	sess, events, unsubscribe, err := h.service.Watch(c.Request.Context(), c.Param("token"))
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	defer unsubscribe()
	c.Header("Referrer-Policy", "no-referrer")
	session.StreamQR(c, sess, events)
}

func translate(err error) error {
	if errors.Is(err, ErrInvalidLink) {
		return apierror.NotFound("link_invalid", "this link is invalid or has expired")
	}
	return err
}
