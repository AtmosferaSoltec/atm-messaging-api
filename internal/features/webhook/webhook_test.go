package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSignAndVerify(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	body := []byte(`{"id":"1"}`)
	signature := Sign("secret", at, body)

	if !Verify("secret", at, body, signature) {
		t.Fatal("a signature must verify with the same secret, time and body")
	}
	if Verify("other-secret", at, body, signature) {
		t.Error("a different secret must not verify")
	}
	if Verify("secret", at.Add(time.Second), body, signature) {
		t.Error("a different timestamp must not verify (replay protection)")
	}
	if Verify("secret", at, []byte(`{"id":"2"}`), signature) {
		t.Error("a tampered body must not verify")
	}
	if len(signature) != len("sha256=")+64 {
		t.Errorf("unexpected signature format %q", signature)
	}
}

func TestValidateURL(t *testing.T) {
	valid := []struct {
		url      string
		insecure bool
	}{
		{"https://example.com/hooks", false},
		{"http://localhost:3000/hooks", true},
	}
	for _, tc := range valid {
		if err := ValidateURL(tc.url, tc.insecure); err != nil {
			t.Errorf("%s should be valid: %v", tc.url, err)
		}
	}

	invalid := []struct {
		url      string
		insecure bool
	}{
		{"http://example.com/hooks", false},
		{"ftp://example.com", true},
		{"example.com/hooks", false},
		{"https://user:pass@example.com", false},
		{"https://", false},
		{"https://example.com/" + string(make([]byte, 3000)), false},
	}
	for _, tc := range invalid {
		if err := ValidateURL(tc.url, tc.insecure); err == nil {
			t.Errorf("%q should be rejected", tc.url)
		}
	}
}

func TestHTTPClientBlocksInternalAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close() // listens on 127.0.0.1

	blocked := NewHTTPClient(2*time.Second, false)
	resp, err := blocked.Get(server.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("a loopback target must be blocked, got %v", err)
	}

	allowed := NewHTTPClient(2*time.Second, true)
	resp, err = allowed.Get(server.URL)
	if err != nil {
		t.Fatalf("with private addresses allowed the request should work: %v", err)
	}
	_ = resp.Body.Close()
}

func TestHTTPClientDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := NewHTTPClient(2*time.Second, true).Get(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("redirects must not be followed, got status %d", resp.StatusCode)
	}
}

// fakeRepository is an in-memory outbox.
type fakeRepository struct {
	mu     sync.Mutex
	events []*Event
}

func (f *fakeRepository) Insert(_ context.Context, e Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e.NextAttemptAt = time.Time{}
	f.events = append(f.events, &e)
	return nil
}

func (f *fakeRepository) PeekDue(_ context.Context, tenantID uuid.UUID) (Event, bool, *time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var next *time.Time
	for _, e := range f.events {
		if e.TenantID != tenantID || e.Status != StatusPending {
			continue
		}
		if e.NextAttemptAt.After(clock) {
			due := e.NextAttemptAt
			next = &due
			continue
		}
		return *e, true, nil, nil
	}
	return Event{}, false, next, nil
}

// clock is the fake time shared by the repository and the dispatcher.
var clock = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func (f *fakeRepository) update(id uuid.UUID, change func(*Event)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.ID == id {
			change(e)
		}
	}
}

func (f *fakeRepository) MarkDelivered(_ context.Context, id uuid.UUID, attempts int) error {
	f.update(id, func(e *Event) { e.Status, e.Attempts = StatusDelivered, attempts })
	return nil
}

func (f *fakeRepository) Reschedule(_ context.Context, id uuid.UUID, dueAt time.Time, attempts int, reason string) error {
	f.update(id, func(e *Event) { e.NextAttemptAt, e.Attempts, e.LastError = dueAt, attempts, reason })
	return nil
}

func (f *fakeRepository) MarkFailed(_ context.Context, id uuid.UUID, attempts int, reason string) error {
	f.update(id, func(e *Event) { e.Status, e.Attempts, e.LastError = StatusFailed, attempts, reason })
	return nil
}

func (f *fakeRepository) ListRecent(context.Context, uuid.UUID, int) ([]Event, error) {
	return nil, nil
}
func (f *fakeRepository) TenantsWithPending(context.Context) ([]uuid.UUID, error) { return nil, nil }
func (f *fakeRepository) CountByStatus(context.Context, *uuid.UUID) (map[Status]int, error) {
	return nil, nil
}

type fakeSettings struct{ url, secret string }

func (f *fakeSettings) WebhookEndpoint(context.Context, uuid.UUID) (string, string, error) {
	return f.url, f.secret, nil
}
func (f *fakeSettings) SetWebhookURL(_ context.Context, _ uuid.UUID, url string) error {
	f.url = url
	return nil
}

type fakeWaker struct{ woken int }

func (f *fakeWaker) Wake(uuid.UUID) { f.woken++ }

func TestNotifySkipsTenantsWithoutAWebhook(t *testing.T) {
	repo := &fakeRepository{}
	waker := &fakeWaker{}
	service := NewService(repo, &fakeSettings{}, waker, false)

	if err := service.Notify(context.Background(), uuid.New(), "message.sent", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.events) != 0 || waker.woken != 0 {
		t.Error("a tenant without a webhook url must not accumulate events")
	}
}

func TestNotifyQueuesASelfDescribingEnvelope(t *testing.T) {
	repo := &fakeRepository{}
	waker := &fakeWaker{}
	service := NewService(repo, &fakeSettings{url: "https://example.com/h", secret: "s"}, waker, false)
	tenantID := uuid.New()

	if err := service.Notify(context.Background(), tenantID, "message.sent", map[string]string{"message_id": "m1"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.events) != 1 || waker.woken != 1 {
		t.Fatalf("expected one queued event and one wake-up, got %d / %d", len(repo.events), waker.woken)
	}

	var envelope struct {
		ID   string            `json:"id"`
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(repo.events[0].Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ID != repo.events[0].ID.String() || envelope.Type != "message.sent" || envelope.Data["message_id"] != "m1" {
		t.Errorf("unexpected envelope %+v", envelope)
	}
}

func TestSetURLValidates(t *testing.T) {
	settings := &fakeSettings{}
	service := NewService(&fakeRepository{}, settings, &fakeWaker{}, false)

	if err := service.SetURL(context.Background(), uuid.New(), "http://example.com"); !errors.Is(err, ErrInvalidURL) {
		t.Errorf("plain http must be rejected in production, got %v", err)
	}
	if err := service.SetURL(context.Background(), uuid.New(), "https://example.com/h"); err != nil || settings.url == "" {
		t.Errorf("a valid url should be stored, got %v / %q", err, settings.url)
	}
	if err := service.SetURL(context.Background(), uuid.New(), ""); err != nil || settings.url != "" {
		t.Errorf("an empty url turns webhooks off, got %v / %q", err, settings.url)
	}
}

type dispatcherHarness struct {
	repo       *fakeRepository
	dispatcher *Dispatcher
	tenantID   uuid.UUID
	event      *Event
}

func newDispatcherHarness(t *testing.T, url string) *dispatcherHarness {
	t.Helper()
	clock = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepository{}
	settings := &fakeSettings{url: url, secret: "topsecret"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDispatcher(repo, settings, NewHTTPClient(2*time.Second, true), DispatcherConfig{MaxAttempts: 3, RetryBase: 30 * time.Second}, log)
	d.now = func() time.Time { return clock }
	t.Cleanup(d.Stop)

	h := &dispatcherHarness{repo: repo, dispatcher: d, tenantID: uuid.New()}
	service := NewService(repo, settings, d, true)
	service.now = func() time.Time { return clock }
	if err := service.Notify(context.Background(), h.tenantID, "message.sent", map[string]string{"message_id": "m1"}); err != nil {
		t.Fatal(err)
	}
	h.event = repo.events[0]
	// Notify woke a real worker; stop it so the test drives process() itself.
	d.Stop()
	return h
}

func (h *dispatcherHarness) process() { h.dispatcher.process(context.Background(), h.tenantID) }

func TestDispatcherDeliversASignedRequest(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	h := newDispatcherHarness(t, server.URL)

	h.process()

	if h.event.Status != StatusDelivered || h.event.Attempts != 1 {
		t.Fatalf("expected delivered after one attempt, got %s / %d", h.event.Status, h.event.Attempts)
	}
	if string(gotBody) != string(h.event.Payload) {
		t.Error("the body sent must be the stored payload")
	}
	timestamp, err := strconv.ParseInt(gotHeaders.Get("X-Webhook-Timestamp"), 10, 64)
	if err != nil {
		t.Fatalf("missing timestamp header: %v", err)
	}
	if !Verify("topsecret", time.Unix(timestamp, 0), gotBody, gotHeaders.Get("X-Signature")) {
		t.Error("the receiver must be able to verify X-Signature with the tenant's secret")
	}
	if gotHeaders.Get("X-Webhook-Id") != h.event.ID.String() || gotHeaders.Get("X-Webhook-Event") != "message.sent" {
		t.Errorf("event identification headers missing: %v", gotHeaders)
	}
}

func TestDispatcherRetriesWithBackoffThenGivesUp(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	h := newDispatcherHarness(t, server.URL)

	h.process() // attempt 1
	if h.event.Status != StatusPending || h.event.Attempts != 1 || h.event.LastError != "status_500" {
		t.Fatalf("after the first failure: %+v", h.event)
	}
	if wait := h.event.NextAttemptAt.Sub(clock); wait != 30*time.Second {
		t.Errorf("first retry in 30s, got %v", wait)
	}

	h.process() // not due yet
	if hits != 1 {
		t.Fatal("an event must not be retried before it is due")
	}

	clock = clock.Add(30 * time.Second)
	h.process() // attempt 2
	if wait := h.event.NextAttemptAt.Sub(clock); wait != 2*time.Minute {
		t.Errorf("second retry in 2m (4x), got %v", wait)
	}

	clock = clock.Add(2 * time.Minute)
	h.process() // attempt 3: give up
	if h.event.Status != StatusFailed || h.event.Attempts != 3 || hits != 3 {
		t.Errorf("expected failed after 3 attempts, got %s / %d / %d hits", h.event.Status, h.event.Attempts, hits)
	}
}

func TestDispatcherFailsEventsOfTenantsWhoRemovedTheirURL(t *testing.T) {
	h := newDispatcherHarness(t, "https://example.com")
	h.dispatcher.settings.(*fakeSettings).url = ""

	h.process()

	if h.event.Status != StatusFailed || h.event.LastError != "webhook_url_not_set" {
		t.Errorf("got %s / %q", h.event.Status, h.event.LastError)
	}
}

func TestDispatcherRefusesInternalTargetsWhenNotAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	h := newDispatcherHarness(t, server.URL)
	h.dispatcher.client = NewHTTPClient(2*time.Second, false) // production setting

	h.process()

	if h.event.Status != StatusPending || h.event.LastError != "blocked_address" {
		t.Errorf("a blocked target must fail with blocked_address, got %s / %q", h.event.Status, h.event.LastError)
	}
}

func TestRetryDelay(t *testing.T) {
	want := []time.Duration{30 * time.Second, 2 * time.Minute, 8 * time.Minute, 32 * time.Minute}
	for i, expected := range want {
		if got := retryDelay(30*time.Second, i+1); got != expected {
			t.Errorf("attempt %d: got %v, want %v", i+1, got, expected)
		}
	}
}
