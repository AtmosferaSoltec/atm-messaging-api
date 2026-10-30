package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/connect"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/message"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/session"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/webhook"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

const testPassword = "a-long-password-1"

func newTestAuth(t *testing.T) *AuthService {
	t.Helper()
	auth, err := NewAuthService("joel", testPassword, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

// ---- auth service ---------------------------------------------------------

func TestNewAuthServiceOnlyRequiresNonEmptyCredentials(t *testing.T) {
	for _, password := range []string{"1234", "abc", "a", "simple password with spaces"} {
		auth, err := NewAuthService("joel", password, time.Hour)
		if err != nil {
			t.Fatalf("%q: any non-empty password must be accepted, got %v", password, err)
		}
		if _, err := auth.Login(context.Background(), "joel", password); err != nil {
			t.Errorf("%q: login with the configured password must work: %v", password, err)
		}
	}

	if _, err := NewAuthService("joel", "", time.Hour); err == nil {
		t.Error("an empty password must be rejected")
	}
	if _, err := NewAuthService("  ", "1234", time.Hour); err == nil {
		t.Error("an empty user must be rejected")
	}
}

func TestLoginAndAuthenticate(t *testing.T) {
	auth := newTestAuth(t)

	result, err := auth.Login(context.Background(), " joel ", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Token, "adm_") || result.User.Username != "joel" {
		t.Errorf("unexpected login result %+v", result)
	}
	for hash := range auth.tokens {
		if hash == result.Token {
			t.Error("the token itself must never be stored, only its hash")
		}
	}

	if user, err := auth.Authenticate(context.Background(), result.Token); err != nil || user.Username != "joel" {
		t.Fatalf("a fresh token must authenticate, got %v / %v", user, err)
	}
	if err := auth.Logout(context.Background(), result.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(context.Background(), result.Token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a logged-out token must stop working, got %v", err)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	auth := newTestAuth(t)
	cases := map[string][2]string{
		"wrong password": {"joel", "wrong-password-123"},
		"wrong user":     {"someone", testPassword},
		"both wrong":     {"someone", "wrong-password-123"},
		"empty":          {"", ""},
		"oversized":      {strings.Repeat("a", 1000), testPassword},
	}
	for name, c := range cases {
		if _, err := auth.Login(context.Background(), c[0], c[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: expected ErrInvalidCredentials, got %v", name, err)
		}
	}
}

func TestExpiredTokensAndTenantKeysAreRejected(t *testing.T) {
	auth := newTestAuth(t)
	result, _ := auth.Login(context.Background(), "joel", testPassword)

	if _, err := auth.Authenticate(context.Background(), "atm_a-tenant-api-key"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a tenant API key is not an admin token, got %v", err)
	}

	auth.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := auth.Authenticate(context.Background(), result.Token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("an expired token must be rejected, got %v", err)
	}
	if _, err := auth.Login(context.Background(), "joel", testPassword); err != nil {
		t.Fatal(err)
	}
	if len(auth.tokens) != 1 {
		t.Errorf("expired tokens should be purged on the next login, have %d", len(auth.tokens))
	}
}

// ---- limiter --------------------------------------------------------------

func TestLoginLimiter(t *testing.T) {
	now := time.Now()
	limiter := newLoginLimiter()
	limiter.now = func() time.Time { return now }

	for i := 0; i < maxLoginFailures-1; i++ {
		limiter.Fail("ip:1")
	}
	if limiter.Blocked("ip:1") {
		t.Fatal("should not block before the limit")
	}
	limiter.Fail("ip:1")
	if !limiter.Blocked("ip:1") {
		t.Fatal("should block at the limit")
	}
	if limiter.Blocked("ip:2") {
		t.Error("other keys are unaffected")
	}

	now = now.Add(loginWindow + time.Second)
	if limiter.Blocked("ip:1") {
		t.Error("failures older than the window are forgiven")
	}

	for i := 0; i < maxLoginFailures; i++ {
		limiter.Fail("email:a")
	}
	limiter.Reset("email:a")
	if limiter.Blocked("email:a") {
		t.Error("a successful login resets the key")
	}
}

// ---- http -----------------------------------------------------------------

type fakeTenants struct{ tenants map[uuid.UUID]tenant.Tenant }

func (f *fakeTenants) Provision(_ context.Context, systemCode string) (tenant.Tenant, error) {
	for _, t := range f.tenants {
		if t.SystemCode == systemCode {
			return t, nil
		}
	}
	t := tenant.Tenant{ID: uuid.New(), Name: systemCode, SystemCode: systemCode, WebhookSecret: "secret-1", IsActive: true}
	f.tenants[t.ID] = t
	return t, nil
}
func (f *fakeTenants) Get(_ context.Context, id uuid.UUID) (tenant.Tenant, error) {
	t, ok := f.tenants[id]
	if !ok {
		return tenant.Tenant{}, tenant.ErrNotFound
	}
	return t, nil
}
func (f *fakeTenants) List(context.Context) ([]tenant.Tenant, error) {
	out := []tenant.Tenant{}
	for _, t := range f.tenants {
		out = append(out, t)
	}
	return out, nil
}
func (f *fakeTenants) Update(_ context.Context, id uuid.UUID, name *string, active *bool) (tenant.Tenant, error) {
	t, ok := f.tenants[id]
	if !ok {
		return tenant.Tenant{}, tenant.ErrNotFound
	}
	if name != nil {
		t.Name = *name
	}
	if active != nil {
		t.IsActive = *active
	}
	f.tenants[id] = t
	return t, nil
}

type fakeSessions struct {
	disconnected []uuid.UUID
	known        *session.Session
}

func (f *fakeSessions) Create(_ context.Context, tenantID uuid.UUID, accountRef string, ch channel.Type) (session.Session, error) {
	if accountRef == "dup" {
		return session.Session{}, session.ErrSessionExists
	}
	if ch != channel.WhatsApp {
		return session.Session{}, session.ErrUnsupportedChannel
	}
	return session.Session{ID: uuid.New(), TenantID: tenantID, AccountRef: accountRef, Channel: ch, Status: session.StatusPendingQR}, nil
}

func (f *fakeSessions) Disconnect(_ context.Context, id uuid.UUID) error {
	f.disconnected = append(f.disconnected, id)
	return nil
}

func (*fakeSessions) ListAll(context.Context, session.Filter) ([]session.Session, error) {
	return nil, nil
}
func (f *fakeSessions) Lookup(_ context.Context, id uuid.UUID) (session.Session, error) {
	if f.known != nil && f.known.ID == id {
		return *f.known, nil
	}
	return session.Session{}, session.ErrNotFound
}
func (*fakeSessions) DeleteAny(context.Context, uuid.UUID) error { return session.ErrNotFound }
func (*fakeSessions) CountByStatus(context.Context, *uuid.UUID) (map[session.Status]int, error) {
	return map[session.Status]int{session.StatusConnected: 3}, nil
}

type fakeLinks struct {
	labels []string
	err    error
}

func (f *fakeLinks) Create(_ context.Context, _ uuid.UUID, label string) (connect.Created, error) {
	f.labels = append(f.labels, label)
	if f.err != nil {
		return connect.Created{}, f.err
	}
	return connect.Created{Token: "cl_secret-token", Label: label, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type fakeMessages struct {
	sendErr    error
	lastSend   message.SendRequest
	lastTenant uuid.UUID
	messages   []message.Message
	lastFilter message.Filter
	activity   []message.HourlyCount
}

func (f *fakeMessages) Send(_ context.Context, tenantID uuid.UUID, req message.SendRequest) (message.Message, error) {
	f.lastTenant, f.lastSend = tenantID, req
	if f.sendErr != nil {
		return message.Message{}, f.sendErr
	}
	return message.Message{ID: uuid.New(), Status: message.StatusQueued}, nil
}

func (f *fakeMessages) GetAny(_ context.Context, id uuid.UUID) (message.Message, error) {
	for _, m := range f.messages {
		if m.ID == id {
			return m, nil
		}
	}
	return message.Message{}, message.ErrNotFound
}

func (f *fakeMessages) ListAll(_ context.Context, filter message.Filter) ([]message.Message, error) {
	f.lastFilter = filter
	if f.messages != nil {
		return f.messages, nil
	}
	return []message.Message{{ID: uuid.New(), Recipient: "51900000001", Body: "SENSITIVE CONTENT", Status: message.StatusSent}}, nil
}
func (*fakeMessages) CountByStatusSince(context.Context, *uuid.UUID, time.Time) (map[message.Status]int, error) {
	return map[message.Status]int{message.StatusSent: 5}, nil
}
func (*fakeMessages) QueueDepth(context.Context, *uuid.UUID) (int, error) { return 2, nil }
func (f *fakeMessages) HourlyActivity(context.Context, *uuid.UUID, time.Time) ([]message.HourlyCount, error) {
	return f.activity, nil
}

type fakeWebhooks struct {
	url      string
	notified []string
}

func (*fakeWebhooks) SetURL(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeWebhooks) Config(context.Context, uuid.UUID) (string, string, error) {
	return f.url, "whsec_test", nil
}
func (f *fakeWebhooks) Notify(_ context.Context, _ uuid.UUID, eventType string, _ any) error {
	f.notified = append(f.notified, eventType)
	return nil
}
func (*fakeWebhooks) Recent(context.Context, uuid.UUID, int) ([]webhook.Event, error) {
	return nil, nil
}
func (*fakeWebhooks) CountByStatus(context.Context, *uuid.UUID) (map[webhook.Status]int, error) {
	return nil, nil
}

type api struct {
	router   *gin.Engine
	password string
	tenants  *fakeTenants
	sessions *fakeSessions
	links    *fakeLinks
	messages *fakeMessages
	webhooks *fakeWebhooks
}

func newAPI(t *testing.T) *api {
	t.Helper()
	gin.SetMode(gin.TestMode)
	auth := newTestAuth(t)

	tenants := &fakeTenants{tenants: map[uuid.UUID]tenant.Tenant{}}
	sessions := &fakeSessions{}
	links := &fakeLinks{}
	messages := &fakeMessages{}
	webhooks := &fakeWebhooks{}
	handler := NewHandler(auth, tenants, sessions, messages, webhooks, links, "https://panel.example.com/")
	router := gin.New()
	RegisterRoutes(router.Group("/admin/v1"), handler, auth)
	return &api{router: router, password: testPassword, tenants: tenants, sessions: sessions, links: links, messages: messages, webhooks: webhooks}
}

func (a *api) do(method, path, token string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return rec
}

func (a *api) login(t *testing.T) string {
	t.Helper()
	rec := a.do(http.MethodPost, "/admin/v1/auth/login", "", map[string]string{"username": "joel", "password": a.password})
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Token
}

func TestEveryAdminRouteExceptLoginRequiresAToken(t *testing.T) {
	a := newAPI(t)
	id := uuid.NewString()
	routes := []struct{ method, path string }{
		{"POST", "/admin/v1/auth/logout"}, {"GET", "/admin/v1/auth/me"},
		{"POST", "/admin/v1/sessions"}, {"POST", "/admin/v1/sessions/" + id + "/connect-link"}, {"POST", "/admin/v1/sessions/" + id + "/disconnect"},
		{"GET", "/admin/v1/tenants"}, {"POST", "/admin/v1/tenants"}, {"GET", "/admin/v1/tenants/" + id},
		{"PATCH", "/admin/v1/tenants/" + id},
		{"PUT", "/admin/v1/tenants/" + id + "/webhook"}, {"DELETE", "/admin/v1/tenants/" + id + "/webhook"},
		{"GET", "/admin/v1/tenants/" + id + "/webhook/events"},
		{"GET", "/admin/v1/sessions"}, {"GET", "/admin/v1/sessions/" + id}, {"DELETE", "/admin/v1/sessions/" + id},
		{"GET", "/admin/v1/messages"}, {"POST", "/admin/v1/messages"}, {"GET", "/admin/v1/messages/" + id}, {"GET", "/admin/v1/metrics"}, {"GET", "/admin/v1/metrics/activity"},
		{"GET", "/admin/v1/tenants/" + id + "/webhook"}, {"POST", "/admin/v1/tenants/" + id + "/webhook/test"},
	}
	for _, r := range routes {
		if rec := a.do(r.method, r.path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: expected 401, got %d", r.method, r.path, rec.Code)
		}
		if rec := a.do(r.method, r.path, "atm_a-tenant-api-key", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a tenant API key: expected 401, got %d", r.method, r.path, rec.Code)
		}
	}
}

func TestLoginThrottling(t *testing.T) {
	a := newAPI(t)
	bad := map[string]string{"username": "joel", "password": "wrong-password-xx"}

	for i := 0; i < maxLoginFailures; i++ {
		if rec := a.do(http.MethodPost, "/admin/v1/auth/login", "", bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rec.Code)
		}
	}
	if rec := a.do(http.MethodPost, "/admin/v1/auth/login", "", bad); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures expected 429, got %d", maxLoginFailures, rec.Code)
	}
	good := map[string]string{"username": "joel", "password": a.password}
	if rec := a.do(http.MethodPost, "/admin/v1/auth/login", "", good); rec.Code != http.StatusTooManyRequests {
		t.Errorf("even the right password must wait while blocked, got %d", rec.Code)
	}
}

func TestTenantSecretsAreNeverListed(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)

	rec := a.do(http.MethodPost, "/admin/v1/tenants", token, map[string]string{"system_code": "lavendimia"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"system_code":"lavendimia"`) {
		t.Fatalf("provisioning failed: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret-1") {
		t.Errorf("the tenant response must not carry the webhook secret: %s", rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	if again := a.do(http.MethodPost, "/admin/v1/tenants", token, map[string]string{"system_code": "lavendimia"}); !strings.Contains(again.Body.String(), created.ID) {
		t.Errorf("provisioning is idempotent: %s", again.Body)
	}
	if rec := a.do(http.MethodPost, "/admin/v1/tenants", token, map[string]string{}); rec.Code != http.StatusBadRequest {
		t.Errorf("system_code is required, got %d", rec.Code)
	}

	for _, path := range []string{"/admin/v1/tenants", "/admin/v1/tenants/" + created.ID} {
		body := a.do(http.MethodGet, path, token, nil).Body.String()
		if strings.Contains(body, "secret-1") || strings.Contains(body, "api_key") {
			t.Errorf("%s must not expose secrets: %s", path, body)
		}
	}
	if rec := a.do(http.MethodPost, "/admin/v1/tenants/"+created.ID+"/rotate-key", token, nil); rec.Code != http.StatusNotFound {
		t.Errorf("api keys no longer exist, got %d", rec.Code)
	}
}

func TestUpdateTenantNeedsAChange(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	created, _ := a.tenants.Provision(context.Background(), "tenant")
	path := "/admin/v1/tenants/" + created.ID.String()

	if rec := a.do(http.MethodPatch, path, token, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty patch is a bad request, got %d", rec.Code)
	}
	rec := a.do(http.MethodPatch, path, token, map[string]any{"is_active": false})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"is_active":false`) {
		t.Errorf("deactivation failed: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do(http.MethodGet, "/admin/v1/tenants/"+uuid.NewString(), token, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown tenant: expected 404, got %d", rec.Code)
	}
}

func TestMessageListingNeverExposesContent(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)

	rec := a.do(http.MethodGet, "/admin/v1/messages", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SENSITIVE CONTENT") {
		t.Error("the admin API must not return message bodies")
	}
	if rec := a.do(http.MethodGet, "/admin/v1/messages?tenant_id=nope", token, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an invalid filter id is a bad request, got %d", rec.Code)
	}
}

func TestMetricsAlwaysIncludeEveryStatus(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	_, _ = a.tenants.Provision(context.Background(), "t1")

	rec := a.do(http.MethodGet, "/admin/v1/metrics", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	var m struct {
		Tenants  map[string]int `json:"tenants"`
		Sessions map[string]int `json:"sessions"`
		Messages map[string]int `json:"messages_last_24h"`
		Webhooks map[string]int `json:"webhooks"`
		Queue    int            `json:"queue_depth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Sessions["connected"] != 3 || m.Sessions["banned"] != 0 || len(m.Sessions) != 4 {
		t.Errorf("sessions: %v", m.Sessions)
	}
	if m.Messages["sent"] != 5 || m.Messages["failed"] != 0 || len(m.Messages) != 6 {
		t.Errorf("messages: %v", m.Messages)
	}
	if len(m.Webhooks) != 3 || m.Queue != 2 || m.Tenants["total"] != 1 || m.Tenants["active"] != 1 {
		t.Errorf("webhooks %v queue %d tenants %v", m.Webhooks, m.Queue, m.Tenants)
	}

	// Filtering by tenant drops the tenant totals.
	rec = a.do(http.MethodGet, "/admin/v1/metrics?tenant_id="+uuid.NewString(), token, nil)
	if strings.Contains(rec.Body.String(), `"tenants"`) {
		t.Error("tenant totals only make sense without a tenant filter")
	}
}

func TestPreflightIsRoutedSoCORSMiddlewareCanAnswerIt(t *testing.T) {
	a := newAPI(t)
	rec := a.do(http.MethodOptions, "/admin/v1/tenants", "", nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("a preflight must not 404, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("without the CORS middleware or a matching origin there are no CORS headers")
	}
}

func TestOperatorCreatesASessionAndSharesAConnectLink(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	created, _ := a.tenants.Provision(context.Background(), "lavendimia")
	tenantID := created.ID.String()

	rec := a.do(http.MethodPost, "/admin/v1/sessions", token,
		map[string]string{"tenant_id": tenantID, "account_ref": "empresa-1", "channel": "whatsapp"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", rec.Code, rec.Body)
	}
	var sess struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sess)

	rec = a.do(http.MethodPost, "/admin/v1/sessions/"+sess.ID+"/connect-link", token, map[string]string{"label": "Pollería Don José"})
	if rec.Code != http.StatusOK {
		t.Fatalf("connect link: %d %s", rec.Code, rec.Body)
	}
	var link ConnectLinkResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &link)
	if link.URL != "https://panel.example.com/connect/cl_secret-token" || link.Label != "Pollería Don José" {
		t.Errorf("the link must be built on the web URL without a double slash: %+v", link)
	}

	// The label is optional.
	if rec := a.do(http.MethodPost, "/admin/v1/sessions/"+sess.ID+"/connect-link", token, nil); rec.Code != http.StatusOK {
		t.Errorf("a link without a body should work, got %d", rec.Code)
	}
}

func TestCreateSessionValidatesItsInput(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	created, _ := a.tenants.Provision(context.Background(), "lavendimia")
	tenantID := created.ID.String()

	cases := map[string]struct {
		body map[string]string
		code int
	}{
		"unknown tenant":    {map[string]string{"tenant_id": uuid.NewString(), "account_ref": "e1", "channel": "whatsapp"}, http.StatusNotFound},
		"malformed tenant":  {map[string]string{"tenant_id": "x", "account_ref": "e1", "channel": "whatsapp"}, http.StatusBadRequest},
		"missing fields":    {map[string]string{"tenant_id": tenantID}, http.StatusBadRequest},
		"unsupported":       {map[string]string{"tenant_id": tenantID, "account_ref": "e1", "channel": "sms"}, http.StatusBadRequest},
		"already connected": {map[string]string{"tenant_id": tenantID, "account_ref": "dup", "channel": "whatsapp"}, http.StatusConflict},
	}
	for name, tc := range cases {
		if rec := a.do(http.MethodPost, "/admin/v1/sessions", token, tc.body); rec.Code != tc.code {
			t.Errorf("%s: expected %d, got %d (%s)", name, tc.code, rec.Code, rec.Body)
		}
	}
}

func TestConnectLinkErrors(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)

	a.links.err = connect.ErrInvalidLabel
	if rec := a.do(http.MethodPost, "/admin/v1/sessions/"+uuid.NewString()+"/connect-link", token, map[string]string{"label": "x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad label is a bad request, got %d", rec.Code)
	}
	a.links.err = session.ErrNotFound
	if rec := a.do(http.MethodPost, "/admin/v1/sessions/"+uuid.NewString()+"/connect-link", token, nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown session is a 404, got %d", rec.Code)
	}
}

func TestDisconnectKeepsTheSession(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	id := uuid.New()

	if rec := a.do(http.MethodPost, "/admin/v1/sessions/"+id.String()+"/disconnect", token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if len(a.sessions.disconnected) != 1 || a.sessions.disconnected[0] != id {
		t.Errorf("the session should have been disconnected, got %v", a.sessions.disconnected)
	}
}

func TestTenantListIncludesActivityCounters(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	_, _ = a.tenants.Provision(context.Background(), "lavendimia")

	rec := a.do(http.MethodGet, "/admin/v1/tenants", token, nil)
	var body struct {
		Items []struct {
			Name            string `json:"name"`
			SessionsCount   int    `json:"sessions_count"`
			MessagesLast24h int    `json:"messages_last_24h"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
		t.Fatalf("unexpected body %s (%v)", rec.Body, err)
	}
	if body.Items[0].SessionsCount != 3 || body.Items[0].MessagesLast24h != 5 {
		t.Errorf("counters should be summed from the status counts, got %+v", body.Items[0])
	}
}

func TestPhoneFromChannelRef(t *testing.T) {
	cases := map[string]string{
		"51936416623:12@s.whatsapp.net": "51936416623",
		"51936416623@s.whatsapp.net":    "51936416623",
		"51936416623":                   "51936416623",
		"":                              "",
		"abc@s.whatsapp.net":            "",
		"@s.whatsapp.net":               "",
	}
	for ref, want := range cases {
		if got := phoneFromChannelRef(ref); got != want {
			t.Errorf("%q: got %q, want %q", ref, got, want)
		}
	}
}

func TestMessageListingPaginatesAndFilters(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	for i := 0; i < 6; i++ { // one more than the page size of 5 requested below
		a.messages.messages = append(a.messages.messages, message.Message{ID: uuid.New(), Status: message.StatusSent})
	}

	rec := a.do(http.MethodGet, "/admin/v1/messages?limit=5&offset=10&status=failed&priority=promotional&q=%2B51%20936-416", token, nil)
	var body struct {
		Items   []map[string]any `json:"items"`
		HasMore bool             `json:"has_more"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Items) != 5 || !body.HasMore {
		t.Errorf("a sixth row means another page exists: %d items, has_more=%v", len(body.Items), body.HasMore)
	}

	f := a.messages.lastFilter
	if f.Limit != 6 || f.Offset != 10 || f.Status != message.StatusFailed || f.Priority != message.PriorityPromotional || f.Recipient != "51936416" {
		t.Errorf("filters must reach the service (limit+1 for the lookahead, digits-only search): %+v", f)
	}

	a.messages.messages = a.messages.messages[:2]
	rec = a.do(http.MethodGet, "/admin/v1/messages?limit=5", token, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Items) != 2 || body.HasMore {
		t.Errorf("the last page has no more: %d items, has_more=%v", len(body.Items), body.HasMore)
	}
}

func TestActivityAlwaysCoversTheLast24Hours(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	now := time.Now().UTC().Truncate(time.Hour)
	a.messages.activity = []message.HourlyCount{
		{Hour: now, Sent: 7, Failed: 1},
		{Hour: now.Add(-5 * time.Hour), Sent: 3},
		{Hour: now.Add(-48 * time.Hour), Sent: 99}, // outside the window: ignored
	}

	rec := a.do(http.MethodGet, "/admin/v1/metrics/activity", token, nil)
	var body struct {
		Items []ActivityPoint `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 24 {
		t.Fatalf("expected 24 hourly points, got %d", len(body.Items))
	}
	last := body.Items[23]
	if !last.Hour.Equal(now) || last.Sent != 7 || last.Failed != 1 {
		t.Errorf("the last point is the current hour: %+v", last)
	}
	if body.Items[18].Sent != 3 || body.Items[0].Sent != 0 {
		t.Errorf("hours must line up and empty hours be zero: %+v / %+v", body.Items[18], body.Items[0])
	}
}

func TestTenantWebhookConfigAndTest(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	id := uuid.NewString()

	rec := a.do(http.MethodGet, "/admin/v1/tenants/"+id+"/webhook", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "whsec_test") {
		t.Errorf("the operator can read the webhook config: %d %s", rec.Code, rec.Body)
	}

	if rec := a.do(http.MethodPost, "/admin/v1/tenants/"+id+"/webhook/test", token, nil); rec.Code != http.StatusConflict {
		t.Errorf("a test without a configured url is a conflict, got %d", rec.Code)
	}

	a.webhooks.url = "https://example.com/hook"
	if rec := a.do(http.MethodPost, "/admin/v1/tenants/"+id+"/webhook/test", token, nil); rec.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", rec.Code)
	}
	if len(a.webhooks.notified) != 1 || a.webhooks.notified[0] != webhook.TestEventType {
		t.Errorf("a webhook.test event must be queued, got %v", a.webhooks.notified)
	}
}

func TestSessionResponseIncludesThePhone(t *testing.T) {
	s := session.Session{ID: uuid.New(), TenantID: uuid.New(), AccountRef: "e1", ChannelRef: "51936416623:7@s.whatsapp.net"}
	if got := newSessionResponse(s).Phone; got != "51936416623" {
		t.Errorf("got %q", got)
	}
}

func TestOperatorSendsAMessageThroughAConnection(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	tenantID := uuid.New()
	sess := session.Session{ID: uuid.New(), TenantID: tenantID, AccountRef: "empresa-1", Channel: channel.WhatsApp, Status: session.StatusConnected}
	a.sessions.known = &sess

	body := map[string]any{
		"session_id": sess.ID.String(), "to": "51936416623", "type": "document", "body": "Tu boleta", "priority": "promotional",
		"attachment": map[string]string{"filename": "boleta.pdf", "mime_type": "application/pdf", "content_base64": "JVBERi0="},
	}
	rec := a.do(http.MethodPost, "/admin/v1/messages", token, body)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
		t.Fatalf("expected 202 queued, got %d %s", rec.Code, rec.Body)
	}

	got := a.messages.lastSend
	if a.messages.lastTenant != tenantID || got.AccountRef != "empresa-1" || got.Channel != "whatsapp" ||
		got.To != "51936416623" || got.Type != "document" || got.Priority != "promotional" || got.Attachment == nil || got.Attachment.Filename != "boleta.pdf" {
		t.Errorf("the message must go out as the session's tenant and account: tenant=%v %+v", a.messages.lastTenant, got)
	}
}

func TestOperatorSendErrorsAreTranslated(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	sess := session.Session{ID: uuid.New(), TenantID: uuid.New(), AccountRef: "e1", Channel: channel.WhatsApp}
	a.sessions.known = &sess
	send := func(sessionID string) *httptest.ResponseRecorder {
		return a.do(http.MethodPost, "/admin/v1/messages", token, map[string]any{"session_id": sessionID, "to": "51936416623", "type": "text", "body": "x"})
	}

	if rec := send("not-a-uuid"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed session id: %d", rec.Code)
	}
	if rec := send(uuid.NewString()); rec.Code != http.StatusNotFound {
		t.Errorf("unknown session: %d", rec.Code)
	}

	cases := map[error]int{
		message.ErrSessionNotConnected:                                      http.StatusConflict,
		message.ErrRecipientOptedOut:                                        http.StatusUnprocessableEntity,
		&message.ValidationError{Code: "invalid_recipient", Message: "bad"}: http.StatusBadRequest,
	}
	for err, want := range cases {
		a.messages.sendErr = err
		if rec := send(sess.ID.String()); rec.Code != want {
			t.Errorf("%v: expected %d, got %d (%s)", err, want, rec.Code, rec.Body)
		}
	}
}

func TestOperatorCanFollowAMessage(t *testing.T) {
	a := newAPI(t)
	token := a.login(t)
	msg := message.Message{ID: uuid.New(), Status: message.StatusDelivered, Recipient: "51936416623", Body: "SENSITIVE"}
	a.messages.messages = []message.Message{msg}

	rec := a.do(http.MethodGet, "/admin/v1/messages/"+msg.ID.String(), token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"delivered"`) || strings.Contains(rec.Body.String(), "SENSITIVE") {
		t.Errorf("status without content expected, got %d %s", rec.Code, rec.Body)
	}
	if rec := a.do(http.MethodGet, "/admin/v1/messages/"+uuid.NewString(), token, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown message: %d", rec.Code)
	}
}

func TestDuplicateTenantNameIsAConflict(t *testing.T) {
	if got := translate(tenant.ErrNameTaken); got == nil {
		t.Fatal("expected a translated error")
	} else if apiErr := new(apierror.Error); !errors.As(got, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "tenant_name_taken" {
		t.Errorf("unexpected translation %v", got)
	}
}
