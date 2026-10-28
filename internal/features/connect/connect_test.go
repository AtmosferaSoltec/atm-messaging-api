package connect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/session"
)

type link struct {
	hash, label string
	expires     time.Time
}

type fakeRepository struct {
	links map[uuid.UUID]link
}

func (f *fakeRepository) Replace(_ context.Context, sessionID uuid.UUID, hash, label string, expires time.Time) error {
	f.links[sessionID] = link{hash, label, expires}
	return nil
}

func (f *fakeRepository) FindByTokenHash(_ context.Context, hash string, now time.Time) (uuid.UUID, string, time.Time, error) {
	for id, l := range f.links {
		if l.hash == hash && l.expires.After(now) {
			return id, l.label, l.expires, nil
		}
	}
	return uuid.Nil, "", time.Time{}, ErrInvalidLink
}

type fakeSessions struct {
	sessions map[uuid.UUID]session.Session
	watched  []uuid.UUID
	events   chan channel.Event
}

func (f *fakeSessions) Lookup(_ context.Context, id uuid.UUID) (session.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return session.Session{}, session.ErrNotFound
	}
	return s, nil
}

func (f *fakeSessions) Watch(_ context.Context, tenantID, id uuid.UUID) (session.Session, <-chan channel.Event, func(), error) {
	s, ok := f.sessions[id]
	if !ok || s.TenantID != tenantID {
		return session.Session{}, nil, nil, session.ErrNotFound
	}
	f.watched = append(f.watched, id)
	return s, f.events, func() {}, nil
}

type fixture struct {
	service  *Service
	repo     *fakeRepository
	sessions *fakeSessions
	session  session.Session
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	sess := session.Session{ID: uuid.New(), TenantID: uuid.New(), AccountRef: "empresa-1", Channel: channel.WhatsApp, Status: session.StatusPendingQR}
	f := &fixture{
		repo:     &fakeRepository{links: map[uuid.UUID]link{}},
		sessions: &fakeSessions{sessions: map[uuid.UUID]session.Session{sess.ID: sess}, events: make(chan channel.Event, 4)},
		session:  sess,
		now:      time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
	}
	f.service = NewService(f.repo, f.sessions, 24*time.Hour)
	f.service.now = func() time.Time { return f.now }
	return f
}

func TestCreateStoresOnlyTheHashAndDefaultsTheLabel(t *testing.T) {
	f := newFixture(t)

	created, err := f.service.Create(context.Background(), f.session.ID, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "cl_") || len(created.Token) < 40 {
		t.Errorf("unexpected token %q", created.Token)
	}
	if created.Label != "empresa-1" || !created.ExpiresAt.Equal(f.now.Add(24*time.Hour)) {
		t.Errorf("label should default to the account and expire in 24h: %+v", created)
	}
	stored := f.repo.links[f.session.ID]
	if stored.hash == created.Token || stored.hash != hashToken(created.Token) {
		t.Error("only the token's hash may be stored")
	}
}

func TestCreateRejectsUnknownSessionsAndLongLabels(t *testing.T) {
	f := newFixture(t)
	if _, err := f.service.Create(context.Background(), uuid.New(), ""); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if _, err := f.service.Create(context.Background(), f.session.ID, strings.Repeat("x", 101)); !errors.Is(err, ErrInvalidLabel) {
		t.Errorf("expected ErrInvalidLabel, got %v", err)
	}
}

func TestANewLinkReplacesThePreviousOne(t *testing.T) {
	f := newFixture(t)
	first, _ := f.service.Create(context.Background(), f.session.ID, "")
	second, _ := f.service.Create(context.Background(), f.session.ID, "")

	if _, _, err := f.service.Resolve(context.Background(), first.Token); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("the replaced link must stop working, got %v", err)
	}
	if _, _, err := f.service.Resolve(context.Background(), second.Token); err != nil {
		t.Errorf("the new link must work: %v", err)
	}
}

func TestLinksExpire(t *testing.T) {
	f := newFixture(t)
	created, _ := f.service.Create(context.Background(), f.session.ID, "")

	f.now = f.now.Add(23 * time.Hour)
	if _, _, err := f.service.Resolve(context.Background(), created.Token); err != nil {
		t.Errorf("a link is valid until it expires: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	if _, _, err := f.service.Resolve(context.Background(), created.Token); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("an expired link must be rejected, got %v", err)
	}
}

func TestInvalidTokensAllLookTheSame(t *testing.T) {
	f := newFixture(t)
	created, _ := f.service.Create(context.Background(), f.session.ID, "")

	for _, token := range []string{"", "nope", "cl_unknown", "atm_" + created.Token[3:], created.Token + "x"} {
		if _, _, err := f.service.Resolve(context.Background(), token); !errors.Is(err, ErrInvalidLink) {
			t.Errorf("%q: expected ErrInvalidLink, got %v", token, err)
		}
		if _, _, _, err := f.service.Watch(context.Background(), token); !errors.Is(err, ErrInvalidLink) {
			t.Errorf("%q: Watch expected ErrInvalidLink, got %v", token, err)
		}
	}

	delete(f.sessions.sessions, f.session.ID)
	if _, _, err := f.service.Resolve(context.Background(), created.Token); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("the link of a deleted session must look invalid, got %v", err)
	}
}

func TestWatchFollowsTheLinkedSessionOnly(t *testing.T) {
	f := newFixture(t)
	other := session.Session{ID: uuid.New(), TenantID: uuid.New()}
	f.sessions.sessions[other.ID] = other
	created, _ := f.service.Create(context.Background(), f.session.ID, "")

	if _, _, _, err := f.service.Watch(context.Background(), created.Token); err != nil {
		t.Fatal(err)
	}
	if len(f.sessions.watched) != 1 || f.sessions.watched[0] != f.session.ID {
		t.Errorf("only the linked session may be watched, got %v", f.sessions.watched)
	}
}

// streamRecorder adds the CloseNotify that Gin's streaming expects from a real connection.
type streamRecorder struct{ *httptest.ResponseRecorder }

func (streamRecorder) CloseNotify() <-chan bool { return make(chan bool) }

func newRouter(f *fixture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/public"), NewHandler(f.service))
	return router
}

func TestPublicInfoExposesNothingInternal(t *testing.T) {
	f := newFixture(t)
	created, _ := f.service.Create(context.Background(), f.session.ID, "Pollería Don José")
	router := newRouter(f)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/connect/"+created.Token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{f.session.ID.String(), f.session.TenantID.String(), "empresa-1"} {
		if strings.Contains(body, secret) {
			t.Errorf("the public page must not reveal %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "Pollería Don José") || !strings.Contains(body, `"status":"pending_qr"`) {
		t.Errorf("unexpected body %s", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("link info must not be cached")
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/connect/cl_wrong", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unknown link is a 404, got %d", rec.Code)
	}
}

func TestPublicQRStream(t *testing.T) {
	f := newFixture(t)
	created, _ := f.service.Create(context.Background(), f.session.ID, "")
	router := newRouter(f)

	f.sessions.events <- channel.Event{Kind: channel.EventQR, Code: "QR-CODE-1"}
	f.sessions.events <- channel.Event{Kind: channel.EventConnected}

	rec := streamRecorder{httptest.NewRecorder()}
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/connect/"+created.Token+"/qr/stream", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "event:qr") || !strings.Contains(body, "QR-CODE-1") || !strings.Contains(body, "event:connected") {
		t.Errorf("expected the qr and connected events, got %q", body)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("unexpected content type %q", rec.Header().Get("Content-Type"))
	}

	notFound := httptest.NewRecorder()
	router.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/public/connect/cl_wrong/qr/stream", nil))
	if notFound.Code != http.StatusNotFound {
		t.Errorf("an unknown link cannot stream, got %d", notFound.Code)
	}
}

func TestPublicRoutesOfferNothingElse(t *testing.T) {
	f := newFixture(t)
	created, _ := f.service.Create(context.Background(), f.session.ID, "")
	router := newRouter(f)

	for _, tc := range []struct{ method, path string }{
		{"DELETE", "/public/connect/" + created.Token},
		{"POST", "/public/connect/" + created.Token},
		{"GET", "/public/sessions"},
		{"GET", "/public/connect/" + created.Token + "/disconnect"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: expected 404, got %d", tc.method, tc.path, rec.Code)
		}
	}
}
