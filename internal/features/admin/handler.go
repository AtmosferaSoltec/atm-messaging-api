package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/account"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/connect"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/message"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/session"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/webhook"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

// The admin API composes the other features through these narrow interfaces,
// which their services satisfy.

// TenantService manages tenants.
type TenantService interface {
	Create(ctx context.Context, name string) (tenant.Created, error)
	Get(ctx context.Context, id uuid.UUID) (tenant.Tenant, error)
	List(ctx context.Context) ([]tenant.Tenant, error)
	Update(ctx context.Context, id uuid.UUID, name *string, isActive *bool) (tenant.Tenant, error)
	RotateAPIKey(ctx context.Context, id uuid.UUID) (tenant.Created, error)
	SetSystemCode(ctx context.Context, id uuid.UUID, code string) error
}

// SessionService manages sessions across tenants.
type SessionService interface {
	Create(ctx context.Context, tenantID uuid.UUID, accountRef string, ch channel.Type) (session.Session, error)
	Disconnect(ctx context.Context, id uuid.UUID) error
	ListAll(ctx context.Context, filter session.Filter) ([]session.Session, error)
	Lookup(ctx context.Context, id uuid.UUID) (session.Session, error)
	DeleteAny(ctx context.Context, id uuid.UUID) error
	CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[session.Status]int, error)
}

// MessageService reads messages across tenants.
type MessageService interface {
	Send(ctx context.Context, tenantID uuid.UUID, req message.SendRequest) (message.Message, error)
	GetAny(ctx context.Context, id uuid.UUID) (message.Message, error)
	ListAll(ctx context.Context, filter message.Filter) ([]message.Message, error)
	CountByStatusSince(ctx context.Context, tenantID *uuid.UUID, since time.Time) (map[message.Status]int, error)
	QueueDepth(ctx context.Context, tenantID *uuid.UUID) (int, error)
	HourlyActivity(ctx context.Context, tenantID *uuid.UUID, since time.Time) ([]message.HourlyCount, error)
}

// WebhookService manages a tenant's webhook on its behalf.
type WebhookService interface {
	SetURL(ctx context.Context, tenantID uuid.UUID, url string) error
	Config(ctx context.Context, tenantID uuid.UUID) (url, secret string, err error)
	Notify(ctx context.Context, tenantID uuid.UUID, eventType string, data any) error
	Recent(ctx context.Context, tenantID uuid.UUID, limit int) ([]webhook.Event, error)
	CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[webhook.Status]int, error)
}

// ConnectLinkService issues the public links customers use to scan a QR code.
type ConnectLinkService interface {
	Create(ctx context.Context, sessionID uuid.UUID, label string) (connect.Created, error)
}

// Handler serves the administration endpoints.
type Handler struct {
	auth     *AuthService
	limiter  *loginLimiter
	tenants  TenantService
	sessions SessionService
	messages MessageService
	webhooks WebhookService
	links    ConnectLinkService

	// publicWebURL is where the web app lives; connect links are built on it.
	publicWebURL string
}

// NewHandler creates a Handler. publicWebURL is the web app's base URL.
func NewHandler(auth *AuthService, tenants TenantService, sessions SessionService, messages MessageService, webhooks WebhookService, links ConnectLinkService, publicWebURL string) *Handler {
	return &Handler{
		auth: auth, limiter: newLoginLimiter(), tenants: tenants, sessions: sessions, messages: messages,
		webhooks: webhooks, links: links, publicWebURL: strings.TrimRight(publicWebURL, "/"),
	}
}

// ---- authentication -------------------------------------------------------

// LoginRequest is the body of POST /admin/v1/auth/login.
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// UserResponse is the public representation of the operator.
type UserResponse struct {
	Username string `json:"username"`
}

func newUserResponse(u User) UserResponse {
	return UserResponse(u)
}

// Login handles POST /admin/v1/auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "username and password are required"))
		return
	}

	ipKey := "ip:" + c.ClientIP()
	userKey := "user:" + strings.ToLower(strings.TrimSpace(req.Username))
	if h.limiter.Blocked(ipKey) || h.limiter.Blocked(userKey) {
		apierror.Write(c, apierror.New(http.StatusTooManyRequests, "too_many_attempts", "too many failed attempts; try again later"))
		return
	}

	result, err := h.auth.Login(c.Request.Context(), req.Username, req.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		h.limiter.Fail(ipKey)
		h.limiter.Fail(userKey)
		apierror.Write(c, apierror.Unauthorized("invalid username or password"))
		return
	}
	if err != nil {
		apierror.Write(c, err)
		return
	}

	h.limiter.Reset(userKey)
	c.JSON(http.StatusOK, gin.H{"token": result.Token, "expires_at": result.ExpiresAt, "user": newUserResponse(result.User)})
}

// Logout handles POST /admin/v1/auth/logout.
func (h *Handler) Logout(c *gin.Context) {
	if err := h.auth.Logout(c.Request.Context(), bearerToken(c)); err != nil {
		apierror.Write(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Me handles GET /admin/v1/auth/me.
func (h *Handler) Me(c *gin.Context) {
	c.JSON(http.StatusOK, newUserResponse(FromContext(c)))
}

// ---- tenants --------------------------------------------------------------

// TenantResponse is the public representation of a tenant. The API key and
// webhook secret never appear here after creation.
type TenantResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	APIKeyPrefix string    `json:"api_key_prefix"`
	SystemCode   string    `json:"system_code"`
	IsActive     bool      `json:"is_active"`
	WebhookURL   string    `json:"webhook_url"`
	CreatedAt    time.Time `json:"created_at"`
}

func newTenantResponse(t tenant.Tenant) TenantResponse {
	return TenantResponse{
		ID: t.ID.String(), Name: t.Name, APIKeyPrefix: t.APIKeyPrefix, SystemCode: t.SystemCode,
		IsActive: t.IsActive, WebhookURL: t.WebhookURL, CreatedAt: t.CreatedAt,
	}
}

// CredentialsResponse carries a tenant together with the secrets shown only once.
type CredentialsResponse struct {
	TenantResponse
	APIKey        string `json:"api_key"`
	WebhookSecret string `json:"webhook_secret"`
}

func newCredentialsResponse(created tenant.Created) CredentialsResponse {
	return CredentialsResponse{
		TenantResponse: newTenantResponse(created.Tenant),
		APIKey:         created.APIKey,
		WebhookSecret:  created.Tenant.WebhookSecret,
	}
}

// TenantSummaryResponse is a tenant with its activity counters, for the listing.
type TenantSummaryResponse struct {
	TenantResponse
	SessionsCount   int `json:"sessions_count"`
	MessagesLast24h int `json:"messages_last_24h"`
}

// ListTenants handles GET /admin/v1/tenants.
func (h *Handler) ListTenants(c *gin.Context) {
	ctx := c.Request.Context()
	tenants, err := h.tenants.List(ctx)
	if err != nil {
		apierror.Write(c, err)
		return
	}

	since := time.Now().Add(-24 * time.Hour)
	items := make([]TenantSummaryResponse, 0, len(tenants))
	for _, t := range tenants {
		id := t.ID
		sessionCounts, err := h.sessions.CountByStatus(ctx, &id)
		if err != nil {
			apierror.Write(c, err)
			return
		}
		messageCounts, err := h.messages.CountByStatusSince(ctx, &id, since)
		if err != nil {
			apierror.Write(c, err)
			return
		}
		items = append(items, TenantSummaryResponse{
			TenantResponse:  newTenantResponse(t),
			SessionsCount:   sumCounts(sessionCounts),
			MessagesLast24h: sumCounts(messageCounts),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func sumCounts[K comparable](counts map[K]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// CreateTenant handles POST /admin/v1/tenants.
func (h *Handler) CreateTenant(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "name is required"))
		return
	}
	created, err := h.tenants.Create(c.Request.Context(), req.Name)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusCreated, newCredentialsResponse(created))
}

// GetTenant handles GET /admin/v1/tenants/:id.
func (h *Handler) GetTenant(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	t, err := h.tenants.Get(c.Request.Context(), id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, newTenantResponse(t))
}

// UpdateTenant handles PATCH /admin/v1/tenants/:id.
func (h *Handler) UpdateTenant(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	var req struct {
		Name       *string `json:"name"`
		IsActive   *bool   `json:"is_active"`
		SystemCode *string `json:"system_code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Name == nil && req.IsActive == nil && req.SystemCode == nil) {
		apierror.Write(c, apierror.BadRequest("invalid_request", "send name, is_active and/or system_code"))
		return
	}
	ctx := c.Request.Context()
	if req.SystemCode != nil {
		if err := h.tenants.SetSystemCode(ctx, id, *req.SystemCode); err != nil {
			apierror.Write(c, translate(err))
			return
		}
	}
	var t tenant.Tenant
	var err error
	if req.Name != nil || req.IsActive != nil {
		t, err = h.tenants.Update(ctx, id, req.Name, req.IsActive)
	} else {
		t, err = h.tenants.Get(ctx, id)
	}
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, newTenantResponse(t))
}

// RotateKey handles POST /admin/v1/tenants/:id/rotate-key.
func (h *Handler) RotateKey(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	created, err := h.tenants.RotateAPIKey(c.Request.Context(), id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, newCredentialsResponse(created))
}

// SetTenantWebhook handles PUT /admin/v1/tenants/:id/webhook.
func (h *Handler) SetTenantWebhook(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "url is required"))
		return
	}
	if err := h.webhooks.SetURL(c.Request.Context(), id, req.URL); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// DeleteTenantWebhook handles DELETE /admin/v1/tenants/:id/webhook.
func (h *Handler) DeleteTenantWebhook(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	if err := h.webhooks.SetURL(c.Request.Context(), id, ""); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// WebhookConfigResponse is where a tenant's webhooks go and how they are signed.
type WebhookConfigResponse struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

// GetTenantWebhook handles GET /admin/v1/tenants/:id/webhook. The operator can
// read the signing secret to set up the tenant's receiver.
func (h *Handler) GetTenantWebhook(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	url, secret, err := h.webhooks.Config(c.Request.Context(), id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, WebhookConfigResponse{URL: url, Secret: secret})
}

// TestTenantWebhook handles POST /admin/v1/tenants/:id/webhook/test: it queues a
// webhook.test event so the receiver can be checked.
func (h *Handler) TestTenantWebhook(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	url, _, err := h.webhooks.Config(c.Request.Context(), id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	if url == "" {
		apierror.Write(c, apierror.Conflict("webhook_not_configured", "set a webhook url first"))
		return
	}
	if err := h.webhooks.Notify(c.Request.Context(), id, webhook.TestEventType, gin.H{"message": "webhook test"}); err != nil {
		apierror.Write(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

// TenantWebhookEvents handles GET /admin/v1/tenants/:id/webhook/events.
func (h *Handler) TenantWebhookEvents(c *gin.Context) {
	id, ok := parseUUID(c, "id", "tenant_not_found")
	if !ok {
		return
	}
	events, err := h.webhooks.Recent(c.Request.Context(), id, queryInt(c, "limit", 50))
	if err != nil {
		apierror.Write(c, err)
		return
	}
	items := make([]webhook.EventResponse, 0, len(events))
	for _, e := range events {
		items = append(items, webhook.NewEventResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ---- sessions -------------------------------------------------------------

// SessionResponse is a session as the admin sees it.
type SessionResponse struct {
	ID               string     `json:"id"`
	TenantID         string     `json:"tenant_id"`
	AccountRef       string     `json:"account_ref"`
	Channel          string     `json:"channel"`
	Status           string     `json:"status"`
	Phone            string     `json:"phone"`
	ConnectedAt      *time.Time `json:"connected_at"`
	FirstConnectedAt *time.Time `json:"first_connected_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

func newSessionResponse(s session.Session) SessionResponse {
	return SessionResponse{
		ID: s.ID.String(), TenantID: s.TenantID.String(), AccountRef: s.AccountRef, Channel: string(s.Channel),
		Status: string(s.Status), Phone: phoneFromChannelRef(s.ChannelRef), ConnectedAt: s.ConnectedAt, FirstConnectedAt: s.FirstConnectedAt, CreatedAt: s.CreatedAt,
	}
}

// phoneFromChannelRef extracts the phone number from a WhatsApp device ID such as
// "51936416623:12@s.whatsapp.net". It returns "" when there is no usable number.
func phoneFromChannelRef(ref string) string {
	end := strings.IndexAny(ref, ":@")
	if end < 0 {
		end = len(ref)
	}
	digits := ref[:end]
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return ""
	}
	return digits
}

// ListSessions handles GET /admin/v1/sessions?tenant_id=&status=&account_ref=&limit=&offset=.
func (h *Handler) ListSessions(c *gin.Context) {
	tenantID, ok := optionalUUID(c, "tenant_id")
	if !ok {
		return
	}
	sessions, err := h.sessions.ListAll(c.Request.Context(), session.Filter{
		TenantID: tenantID, Status: session.Status(c.Query("status")), AccountRef: c.Query("account_ref"),
		Limit: queryInt(c, "limit", 100), Offset: queryInt(c, "offset", 0),
	})
	if err != nil {
		apierror.Write(c, err)
		return
	}
	items := make([]SessionResponse, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, newSessionResponse(s))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// GetSession handles GET /admin/v1/sessions/:id.
func (h *Handler) GetSession(c *gin.Context) {
	id, ok := parseUUID(c, "id", "session_not_found")
	if !ok {
		return
	}
	s, err := h.sessions.Lookup(c.Request.Context(), id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, newSessionResponse(s))
}

// CreateSession handles POST /admin/v1/sessions: it creates the session of an
// account on behalf of a tenant and starts pairing, so a connect link can be shared.
func (h *Handler) CreateSession(c *gin.Context) {
	var req struct {
		TenantID   string `json:"tenant_id" binding:"required"`
		AccountRef string `json:"account_ref" binding:"required"`
		Channel    string `json:"channel" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_request", "tenant_id, account_ref and channel are required"))
		return
	}
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_tenant_id", "tenant_id must be a valid id"))
		return
	}
	if _, err := h.tenants.Get(c.Request.Context(), tenantID); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	sess, err := h.sessions.Create(c.Request.Context(), tenantID, req.AccountRef, channel.Type(req.Channel))
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusCreated, newSessionResponse(sess))
}

// ConnectLinkResponse is a freshly issued link.
type ConnectLinkResponse struct {
	URL       string    `json:"url"`
	Label     string    `json:"label"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateConnectLink handles POST /admin/v1/sessions/:id/connect-link. The
// previous link of the session stops working. The URL is shown only here.
func (h *Handler) CreateConnectLink(c *gin.Context) {
	id, ok := parseUUID(c, "id", "session_not_found")
	if !ok {
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			apierror.Write(c, apierror.BadRequest("invalid_request", "label must be text"))
			return
		}
	}
	created, err := h.links.Create(c.Request.Context(), id, req.Label)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, ConnectLinkResponse{
		URL: h.publicWebURL + "/connect/" + created.Token, Label: created.Label, ExpiresAt: created.ExpiresAt,
	})
}

// DisconnectSession handles POST /admin/v1/sessions/:id/disconnect: it unlinks
// the number but keeps the session, ready to be paired again.
func (h *Handler) DisconnectSession(c *gin.Context) {
	id, ok := parseUUID(c, "id", "session_not_found")
	if !ok {
		return
	}
	if err := h.sessions.Disconnect(c.Request.Context(), id); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// DeleteSession handles DELETE /admin/v1/sessions/:id: it logs the number out.
func (h *Handler) DeleteSession(c *gin.Context) {
	id, ok := parseUUID(c, "id", "session_not_found")
	if !ok {
		return
	}
	if err := h.sessions.DeleteAny(c.Request.Context(), id); err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- messages -------------------------------------------------------------

// MessageResponse is a message as the admin sees it. The body and the
// attachments are deliberately left out: the panel is for operations, not for
// reading customers' content.
type MessageResponse struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	SessionID   string     `json:"session_id"`
	Channel     string     `json:"channel"`
	To          string     `json:"to"`
	Type        string     `json:"type"`
	Priority    string     `json:"priority"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	SentAt      *time.Time `json:"sent_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
	ReadAt      *time.Time `json:"read_at"`
}

func newMessageResponse(m message.Message) MessageResponse {
	return MessageResponse{
		ID: m.ID.String(), TenantID: m.TenantID.String(), SessionID: m.SessionID.String(), Channel: string(m.Channel),
		To: m.Recipient, Type: string(m.Kind), Priority: string(m.Priority), Status: string(m.Status),
		Attempts: m.Attempts, Error: m.Error, CreatedAt: m.CreatedAt, SentAt: m.SentAt,
		DeliveredAt: m.DeliveredAt, ReadAt: m.ReadAt,
	}
}

// GetMessage handles GET /admin/v1/messages/:id.
func (h *Handler) GetMessage(c *gin.Context) {
	id, ok := parseUUID(c, "id", "message_not_found")
	if !ok {
		return
	}
	m, err := h.messages.GetAny(c.Request.Context(), id)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusOK, newMessageResponse(m))
}

// SendMessageRequest is the body of POST /admin/v1/messages.
type SendMessageRequest struct {
	SessionID  string                     `json:"session_id"`
	To         string                     `json:"to"`
	Type       string                     `json:"type"`
	Body       string                     `json:"body"`
	Priority   string                     `json:"priority"`
	Attachment *message.AttachmentRequest `json:"attachment"`
}

// maxSendBody bounds the JSON body: a 16 MiB attachment grows by a third in base64.
const maxSendBody = 24 << 20

// SendMessage handles POST /admin/v1/messages: the operator sends a message from a
// connection (to try it out). It goes through the same validation, queue and
// limits as a tenant's message, and counts as that tenant's.
func (h *Handler) SendMessage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSendBody)

	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierror.Write(c, apierror.New(http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large"))
			return
		}
		apierror.Write(c, apierror.BadRequest("invalid_request", "request body is not valid JSON"))
		return
	}
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_session_id", "session_id must be a valid id"))
		return
	}
	sess, err := h.sessions.Lookup(c.Request.Context(), sessionID)
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}

	msg, err := h.messages.Send(c.Request.Context(), sess.TenantID, message.SendRequest{
		AccountRef: sess.AccountRef, Channel: string(sess.Channel), To: req.To, Type: req.Type,
		Body: req.Body, Priority: req.Priority, Attachment: req.Attachment,
	})
	if err != nil {
		apierror.Write(c, translate(err))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message_id": msg.ID.String(), "status": string(msg.Status)})
}

// defaultPageSize is the messages page size when the client does not ask for one.
const defaultPageSize = 20

// ListMessages handles GET /admin/v1/messages?tenant_id=&session_id=&status=&priority=&q=&limit=&offset=.
// It returns one extra row internally to tell whether another page exists.
func (h *Handler) ListMessages(c *gin.Context) {
	tenantID, ok := optionalUUID(c, "tenant_id")
	if !ok {
		return
	}
	sessionID, ok := optionalUUID(c, "session_id")
	if !ok {
		return
	}
	pageSize := min(max(queryInt(c, "limit", defaultPageSize), 1), 200)
	messages, err := h.messages.ListAll(c.Request.Context(), message.Filter{
		TenantID: tenantID, SessionID: sessionID, Status: message.Status(c.Query("status")),
		Priority: message.Priority(c.Query("priority")), Recipient: digitsOnly(c.Query("q")),
		Limit: pageSize + 1, Offset: queryInt(c, "offset", 0),
	})
	if err != nil {
		apierror.Write(c, err)
		return
	}
	hasMore := len(messages) > pageSize
	if hasMore {
		messages = messages[:pageSize]
	}
	items := make([]MessageResponse, 0, len(messages))
	for _, m := range messages {
		items = append(items, newMessageResponse(m))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "has_more": hasMore})
}

func digitsOnly(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- metrics --------------------------------------------------------------

// Metrics handles GET /admin/v1/metrics?tenant_id=. Every status is always
// present (zero when empty), so the panel can render without checking for gaps.
func (h *Handler) Metrics(c *gin.Context) {
	tenantID, ok := optionalUUID(c, "tenant_id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	now := time.Now()

	sessionCounts, err := h.sessions.CountByStatus(ctx, tenantID)
	if err != nil {
		apierror.Write(c, err)
		return
	}
	messageCounts, err := h.messages.CountByStatusSince(ctx, tenantID, now.Add(-24*time.Hour))
	if err != nil {
		apierror.Write(c, err)
		return
	}
	queueDepth, err := h.messages.QueueDepth(ctx, tenantID)
	if err != nil {
		apierror.Write(c, err)
		return
	}
	webhookCounts, err := h.webhooks.CountByStatus(ctx, tenantID)
	if err != nil {
		apierror.Write(c, err)
		return
	}

	response := gin.H{
		"generated_at": now.UTC(),
		"sessions": fillCounts(sessionCounts, []session.Status{
			session.StatusPendingQR, session.StatusConnected, session.StatusDisconnected, session.StatusBanned}),
		"messages_last_24h": fillCounts(messageCounts, []message.Status{
			message.StatusQueued, message.StatusSending, message.StatusSent, message.StatusDelivered, message.StatusRead, message.StatusFailed}),
		"queue_depth": queueDepth,
		"webhooks": fillCounts(webhookCounts, []webhook.Status{
			webhook.StatusPending, webhook.StatusDelivered, webhook.StatusFailed}),
	}
	if tenantID == nil {
		tenants, err := h.tenants.List(ctx)
		if err != nil {
			apierror.Write(c, err)
			return
		}
		active := 0
		for _, t := range tenants {
			if t.IsActive {
				active++
			}
		}
		response["tenants"] = gin.H{"total": len(tenants), "active": active}
	}
	c.JSON(http.StatusOK, response)
}

// ActivityPoint is one hour of message activity.
type ActivityPoint struct {
	Hour   time.Time `json:"hour"`
	Sent   int       `json:"sent"`
	Failed int       `json:"failed"`
}

const activityHours = 24

// Activity handles GET /admin/v1/metrics/activity?tenant_id=: the last 24 hours,
// hour by hour, with zeros for hours without messages so a chart needs no gaps logic.
func (h *Handler) Activity(c *gin.Context) {
	tenantID, ok := optionalUUID(c, "tenant_id")
	if !ok {
		return
	}
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-(activityHours - 1) * time.Hour)

	counts, err := h.messages.HourlyActivity(c.Request.Context(), tenantID, start)
	if err != nil {
		apierror.Write(c, err)
		return
	}
	byHour := make(map[time.Time]message.HourlyCount, len(counts))
	for _, count := range counts {
		byHour[count.Hour.UTC().Truncate(time.Hour)] = count
	}

	items := make([]ActivityPoint, 0, activityHours)
	for hour := start; !hour.After(end); hour = hour.Add(time.Hour) {
		count := byHour[hour]
		items = append(items, ActivityPoint{Hour: hour, Sent: count.Sent, Failed: count.Failed})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func fillCounts[K ~string](counts map[K]int, statuses []K) map[string]int {
	filled := make(map[string]int, len(statuses))
	for _, status := range statuses {
		filled[string(status)] = counts[status]
	}
	return filled
}

// ---- helpers --------------------------------------------------------------

func parseUUID(c *gin.Context, param, notFoundCode string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(param))
	if err != nil {
		apierror.Write(c, apierror.NotFound(notFoundCode, "not found"))
		return uuid.Nil, false
	}
	return id, true
}

// optionalUUID reads a UUID query parameter; absent means "no filter".
func optionalUUID(c *gin.Context, name string) (*uuid.UUID, bool) {
	value := c.Query(name)
	if value == "" {
		return nil, true
	}
	id, err := uuid.Parse(value)
	if err != nil {
		apierror.Write(c, apierror.BadRequest("invalid_"+name, name+" must be a valid id"))
		return nil, false
	}
	return &id, true
}

func queryInt(c *gin.Context, name string, fallback int) int {
	value, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return fallback
	}
	return value
}

func translate(err error) error {
	var validation *message.ValidationError
	switch {
	case errors.As(err, &validation):
		return apierror.BadRequest(validation.Code, validation.Message)
	case errors.Is(err, message.ErrNotFound):
		return apierror.NotFound("message_not_found", "message not found")
	case errors.Is(err, message.ErrSessionNotConnected):
		return apierror.Conflict("session_not_connected", "the session is not connected")
	case errors.Is(err, message.ErrSessionNotFound):
		return apierror.NotFound("session_not_found", "this account has no session for the channel")
	case errors.Is(err, message.ErrRecipientOptedOut):
		return apierror.New(http.StatusUnprocessableEntity, "recipient_opted_out", "the recipient opted out of promotional messages")
	case errors.Is(err, tenant.ErrNotFound):
		return apierror.NotFound("tenant_not_found", "tenant not found")
	case errors.Is(err, tenant.ErrInvalidName):
		return apierror.BadRequest("invalid_name", err.Error())
	case errors.Is(err, tenant.ErrInvalidSystemCode):
		return apierror.BadRequest("invalid_system_code", err.Error())
	case errors.Is(err, tenant.ErrSystemCodeTaken):
		return apierror.Conflict("system_code_taken", err.Error())
	case errors.Is(err, tenant.ErrNameTaken):
		return apierror.Conflict("tenant_name_taken", "a tenant with this name already exists")
	case errors.Is(err, session.ErrNotFound):
		return apierror.NotFound("session_not_found", "session not found")
	case errors.Is(err, session.ErrSessionExists):
		return apierror.Conflict("session_exists", "this account already has a session for this channel")
	case errors.Is(err, session.ErrUnsupportedChannel):
		return apierror.BadRequest("unsupported_channel", "unsupported channel")
	case errors.Is(err, account.ErrInvalidExternalRef):
		return apierror.BadRequest("invalid_account_ref", err.Error())
	case errors.Is(err, connect.ErrInvalidLabel):
		return apierror.BadRequest("invalid_label", err.Error())
	case errors.Is(err, webhook.ErrInvalidURL):
		return apierror.BadRequest("invalid_url", err.Error())
	}
	return err
}
