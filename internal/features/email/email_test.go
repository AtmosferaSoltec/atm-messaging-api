package email

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func intPtr(n int) *int { return &n }

// ---- validation ------------------------------------------------------------

func TestNormalizeAddress(t *testing.T) {
	good := map[string]string{"ana@example.com": "ana@example.com", " Ana.B+x@Example.COM ": "ana.b+x@example.com"}
	for in, want := range good {
		if got, err := NormalizeAddress(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	bad := []string{"", "ana", "ana@", "@example.com", "ana@example", "Ana <ana@example.com>", "a@example.com, b@example.com",
		"a@example.com\r\nBcc: x@example.com", "a b@example.com", "ana@example.com\n", "a@b.com;c@d.com",
		strings.Repeat("a", 250) + "@example.com"}
	for _, in := range bad {
		if _, err := NormalizeAddress(in); err == nil {
			t.Errorf("%q must be rejected", in)
		}
	}
}

func TestValidateData(t *testing.T) {
	if got, err := ValidateData(TemplatePasswordResetCode, Data{Name: " Ana ", Code: "123456", ExpiresMinutes: intPtr(15)}); err != nil || got.Name != "Ana" {
		t.Fatalf("valid reset data: %+v %v", got, err)
	}
	cases := []struct {
		template string
		data     Data
		code     string
	}{
		{TemplatePasswordResetCode, Data{Code: "12345", ExpiresMinutes: intPtr(15)}, "invalid_data"},
		{TemplatePasswordResetCode, Data{Code: "12345a", ExpiresMinutes: intPtr(15)}, "invalid_data"},
		{TemplatePasswordResetCode, Data{Code: "123456"}, "invalid_data"},
		{TemplatePasswordResetCode, Data{Code: "123456", ExpiresMinutes: intPtr(0)}, "invalid_data"},
		{TemplatePasswordResetCode, Data{Code: "123456", ExpiresMinutes: intPtr(61)}, "invalid_data"},
		{TemplatePasswordResetCode, Data{Name: strings.Repeat("a", 101), Code: "123456", ExpiresMinutes: intPtr(5)}, "invalid_data"},
		{TemplatePasswordChanged, Data{Name: "a\nb"}, "invalid_data"},
		{"nope", Data{}, "unknown_template"},
	}
	for _, c := range cases {
		_, err := ValidateData(c.template, c.data)
		var v *ValidationError
		if !errors.As(err, &v) || v.Code != c.code {
			t.Errorf("%s %+v: got %v, want %s", c.template, c.data, err, c.code)
		}
	}
	if got, err := ValidateData(TemplatePasswordChanged, Data{Code: "999999"}); err != nil || got.Code != "" {
		t.Errorf("password_changed must drop unused fields: %+v %v", got, err)
	}
}

// ---- templates -------------------------------------------------------------

func TestRenderEscapesData(t *testing.T) {
	r, err := Render(TemplatePasswordResetCode, Data{Name: `<script>alert("x")</script>`, Code: "123456", ExpiresMinutes: intPtr(15)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<script>") || !strings.Contains(r.HTML, "&lt;script&gt;") {
		t.Error("the name must be escaped in HTML")
	}
	for _, want := range []string{"123456", "15 minutos"} {
		if !strings.Contains(r.HTML, want) || !strings.Contains(r.Text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(r.Subject, "123456") || r.Text == "" {
		t.Errorf("subject/text: %q", r.Subject)
	}

	changed, err := Render(TemplatePasswordChanged, Data{})
	if err != nil || !strings.Contains(changed.HTML, "se cambió") || strings.Contains(changed.Text, "Hola") {
		t.Errorf("password_changed without name: %v", err)
	}
	if _, err := Render("nope", Data{}); err == nil {
		t.Error("unknown template must fail")
	}
}

// ---- fakes -----------------------------------------------------------------

type fakeRepo struct {
	mu      sync.Mutex
	emails  map[uuid.UUID]*Email
	order   []uuid.UUID
	counts  int
	blanked map[uuid.UUID]bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{emails: map[uuid.UUID]*Email{}, blanked: map[uuid.UUID]bool{}}
}

func (f *fakeRepo) Insert(_ context.Context, e Email) (Email, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.IdempotencyKey != "" {
		for _, x := range f.emails {
			if x.TenantID == e.TenantID && x.IdempotencyKey == e.IdempotencyKey {
				return *x, false, nil
			}
		}
	}
	e.Status, e.CreatedAt, e.NextAttemptAt = StatusQueued, time.Now(), time.Now()
	f.emails[e.ID] = &e
	f.order = append(f.order, e.ID)
	return e, true, nil
}
func (f *fakeRepo) FindByKey(_ context.Context, tenantID uuid.UUID, key string) (Email, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.emails {
		if x.TenantID == tenantID && x.IdempotencyKey == key {
			return *x, true, nil
		}
	}
	return Email{}, false, nil
}
func (f *fakeRepo) Get(_ context.Context, tenantID, id uuid.UUID) (Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x, ok := f.emails[id]; ok && x.TenantID == tenantID {
		return *x, nil
	}
	return Email{}, ErrNotFound
}
func (f *fakeRepo) CountToAddressSince(_ context.Context, tenantID uuid.UUID, address string, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.emails {
		if x.TenantID == tenantID && x.To == address {
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) Claim(_ context.Context, _ time.Duration) (Email, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.order {
		x := f.emails[id]
		if (x.Status == StatusQueued || x.Status == StatusSending) && !x.NextAttemptAt.After(time.Now()) {
			x.Status = StatusSending
			x.Attempts++
			return *x, true, nil
		}
	}
	return Email{}, false, nil
}
func (f *fakeRepo) MarkSent(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emails[id].Status = StatusSent
	f.blanked[id] = true
	return nil
}
func (f *fakeRepo) Reschedule(_ context.Context, id uuid.UUID, next time.Time, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.emails[id]
	x.Status, x.NextAttemptAt, x.LastError = StatusQueued, next, reason
	return nil
}
func (f *fakeRepo) MarkFailed(_ context.Context, id uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.emails[id]
	x.Status, x.LastError = StatusFailed, reason
	f.blanked[id] = true
	return nil
}
func (f *fakeRepo) CountByTenantSince(context.Context, time.Time) ([]TenantCounts, error) {
	return nil, nil
}

type fakeSender struct {
	errs  []error // one per call; empty means success
	calls []Message
}

func (s *fakeSender) Send(_ context.Context, m Message) error {
	s.calls = append(s.calls, m)
	if len(s.errs) == 0 {
		return nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

type noWake struct{}

func (noWake) Wake() {}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func queueOne(t *testing.T, repo *fakeRepo) Email {
	t.Helper()
	e, _, _ := repo.Insert(context.Background(), Email{
		ID: uuid.New(), TenantID: uuid.New(), To: "ana@example.com", Template: TemplatePasswordResetCode,
		Data: Data{Code: "123456", ExpiresMinutes: intPtr(15)},
	})
	return e
}

// ---- worker ----------------------------------------------------------------

func TestWorkerSendsAndMarksSent(t *testing.T) {
	repo, sender := newFakeRepo(), &fakeSender{}
	e := queueOne(t, repo)
	w := NewWorker(repo, sender, WorkerConfig{MaxAttempts: 3, RetryBase: time.Second}, quietLog())

	if !w.ProcessOne(context.Background()) || w.ProcessOne(context.Background()) {
		t.Fatal("exactly one email was due")
	}
	if repo.emails[e.ID].Status != StatusSent || !repo.blanked[e.ID] {
		t.Errorf("status %s blanked=%v", repo.emails[e.ID].Status, repo.blanked[e.ID])
	}
	if len(sender.calls) != 1 || sender.calls[0].To != "ana@example.com" || !strings.Contains(sender.calls[0].Text, "123456") {
		t.Errorf("unexpected message: %+v", sender.calls)
	}
}

func TestWorkerRetriesWithBackoffThenFails(t *testing.T) {
	repo := newFakeRepo()
	sender := &fakeSender{errs: []error{errors.New("boom"), errors.New("boom"), errors.New("boom")}}
	e := queueOne(t, repo)
	w := NewWorker(repo, sender, WorkerConfig{MaxAttempts: 3, RetryBase: time.Minute}, quietLog())

	w.ProcessOne(context.Background())
	got := repo.emails[e.ID]
	if got.Status != StatusQueued || got.Attempts != 1 || time.Until(got.NextAttemptAt) < 50*time.Second {
		t.Fatalf("after 1st failure: %+v", got)
	}
	if w.ProcessOne(context.Background()) {
		t.Fatal("not due yet: backoff must delay the retry")
	}
	got.NextAttemptAt = time.Now()
	w.ProcessOne(context.Background())
	if d := time.Until(got.NextAttemptAt); d < 100*time.Second || d > 125*time.Second {
		t.Errorf("second wait should be about 2 minutes, got %s", d)
	}
	got.NextAttemptAt = time.Now()
	w.ProcessOne(context.Background())
	if got.Status != StatusFailed || got.Attempts != 3 || !repo.blanked[e.ID] {
		t.Errorf("after max attempts: %+v blanked=%v", got, repo.blanked[e.ID])
	}
}

func TestWorkerPermanentFailureDoesNotRetry(t *testing.T) {
	repo := newFakeRepo()
	sender := &fakeSender{errs: []error{ErrPermanent}}
	e := queueOne(t, repo)
	NewWorker(repo, sender, WorkerConfig{MaxAttempts: 5, RetryBase: time.Second}, quietLog()).ProcessOne(context.Background())
	if repo.emails[e.ID].Status != StatusFailed || repo.emails[e.ID].Attempts != 1 || !repo.blanked[e.ID] {
		t.Errorf("%+v", repo.emails[e.ID])
	}
}

func TestWorkerGivesUpOnAbandonedEmail(t *testing.T) {
	repo, sender := newFakeRepo(), &fakeSender{}
	e := queueOne(t, repo)
	repo.emails[e.ID].Attempts = 3 // already used every attempt (crashed mid-send); the claim makes it 4
	NewWorker(repo, sender, WorkerConfig{MaxAttempts: 3, RetryBase: time.Second}, quietLog()).ProcessOne(context.Background())
	if repo.emails[e.ID].Status != StatusFailed || len(sender.calls) != 0 {
		t.Errorf("must fail without sending: %+v", repo.emails[e.ID])
	}
}

func TestBackoff(t *testing.T) {
	if backoff(30*time.Second, 1) != 30*time.Second || backoff(30*time.Second, 3) != 2*time.Minute || backoff(time.Hour, 10) != maxBackoff {
		t.Error("unexpected backoff")
	}
}

func TestWorkerStartStop(t *testing.T) {
	repo, sender := newFakeRepo(), &fakeSender{}
	queueOne(t, repo)
	w := NewWorker(repo, sender, WorkerConfig{MaxAttempts: 3, RetryBase: time.Second}, quietLog())
	w.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		n := len(repo.blanked)
		repo.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.Stop()
	if len(sender.calls) != 1 {
		t.Errorf("expected one send, got %d", len(sender.calls))
	}
}

// ---- service ---------------------------------------------------------------

func validRequest(key string) SendRequest {
	return SendRequest{To: "ana@example.com", Template: TemplatePasswordResetCode,
		Data: Data{Code: "123456", ExpiresMinutes: intPtr(15)}, IdempotencyKey: key}
}

func TestEnqueueIdempotent(t *testing.T) {
	repo := newFakeRepo()
	s := NewService(repo, noWake{}, true)
	tenant := uuid.New()
	a, err := s.Enqueue(context.Background(), tenant, validRequest("k1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Enqueue(context.Background(), tenant, validRequest("k1"))
	if err != nil || b.ID != a.ID || len(repo.emails) != 1 {
		t.Fatalf("same key must return the original: %v %v", b.ID, err)
	}
	other, _ := s.Enqueue(context.Background(), uuid.New(), validRequest("k1"))
	if other.ID == a.ID {
		t.Error("keys are per tenant")
	}
}

func TestEnqueueRateLimits(t *testing.T) {
	repo := newFakeRepo()
	s := NewService(repo, noWake{}, true)
	tenant := uuid.New()
	for i := 0; i < addressRateLimit; i++ {
		if _, err := s.Enqueue(context.Background(), tenant, validRequest("")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, err := s.Enqueue(context.Background(), tenant, validRequest("")); !errors.Is(err, ErrRateLimited) {
		t.Errorf("per-address limit: %v", err)
	}
	// A replay of an existing key is not a new send and is never limited.
	if _, err := s.Enqueue(context.Background(), tenant, validRequest("")); !errors.Is(err, ErrRateLimited) {
		t.Fatal("still limited")
	}

	// Per-tenant limit: distinct addresses.
	limited := uuid.New()
	var err error
	for i := 0; i <= tenantRateLimit && err == nil; i++ {
		req := validRequest("")
		req.To = "user" + uuid.NewString()[:8] + "@example.com"
		_, err = s.Enqueue(context.Background(), limited, req)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("per-tenant limit: %v", err)
	}
}

func TestEnqueueUnavailableAndInvalid(t *testing.T) {
	disabled := NewService(newFakeRepo(), noWake{}, false)
	if _, err := disabled.Enqueue(context.Background(), uuid.New(), validRequest("")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v", err)
	}
	s := NewService(newFakeRepo(), noWake{}, true)
	req := validRequest("")
	req.Template = "nope"
	if _, err := s.Enqueue(context.Background(), uuid.New(), req); err == nil {
		t.Error("unknown template must be rejected")
	}
	if _, err := s.Get(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Error("expected not found")
	}
}

// ---- resend ----------------------------------------------------------------

func TestResendSender(t *testing.T) {
	var gotAuth, gotBody string
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"secret-body-echo"}`))
	}))
	defer server.Close()
	sender := NewResendSender("test-key", "Atmosfera <no-reply@example.com>")
	sender.endpoint = server.URL
	msg := Message{To: "ana@example.com", Subject: "Hola", HTML: "<p>x</p>", Text: "x"}

	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" || !strings.Contains(gotBody, `"to":["ana@example.com"]`) ||
		!strings.Contains(gotBody, `"from":"Atmosfera \u003cno-reply@example.com\u003e"`) || !strings.Contains(gotBody, `"text":"x"`) {
		t.Errorf("request: %s / %s", gotAuth, gotBody)
	}

	for code, permanent := range map[int]bool{400: true, 401: true, 403: true, 422: true, 429: false, 500: false, 503: false} {
		status = code
		err := sender.Send(context.Background(), msg)
		if err == nil || errors.Is(err, ErrPermanent) != permanent {
			t.Errorf("status %d: permanent=%v err=%v", code, permanent, err)
		}
		if err != nil && (strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "secret-body-echo")) {
			t.Errorf("error leaks key or body: %v", err)
		}
	}

	server.Close()
	if err := sender.Send(context.Background(), msg); err == nil || errors.Is(err, ErrPermanent) {
		t.Errorf("network errors must be retryable: %v", err)
	}
}

func TestMaskAddressAndLogSender(t *testing.T) {
	if MaskAddress("ana@example.com") != "a***@example.com" || MaskAddress("junk") != "***" {
		t.Error("mask")
	}
	var sb strings.Builder
	log := slog.New(slog.NewTextHandler(&sb, nil))
	_ = LogSender{Log: log}.Send(context.Background(), Message{Template: "password_reset_code", To: "ana@example.com", Text: "code 123456"})
	if out := sb.String(); strings.Contains(out, "123456") || strings.Contains(out, "ana@") || !strings.Contains(out, "a***@example.com") {
		t.Errorf("log output: %s", out)
	}
}
