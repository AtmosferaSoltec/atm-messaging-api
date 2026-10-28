package message

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/limits"
)

// fakeRepository is an in-memory queue with the semantics of the SQL one.
type fakeRepository struct {
	mu          sync.Mutex
	now         func() time.Time
	messages    map[uuid.UUID]*Message
	attachments map[uuid.UUID][]byte
	dueAt       map[uuid.UUID]time.Time
	order       []uuid.UUID
}

func newFakeRepository(now func() time.Time) *fakeRepository {
	return &fakeRepository{
		now: now, messages: map[uuid.UUID]*Message{}, attachments: map[uuid.UUID][]byte{}, dueAt: map[uuid.UUID]time.Time{},
	}
}

func (f *fakeRepository) Insert(_ context.Context, m Message, attachment []byte) (Message, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.IdempotencyKey != "" {
		for _, existing := range f.messages {
			if existing.TenantID == m.TenantID && existing.IdempotencyKey == m.IdempotencyKey {
				return *existing, false, nil
			}
		}
	}
	m.CreatedAt = f.now()
	f.messages[m.ID] = &m
	f.attachments[m.ID] = attachment
	f.dueAt[m.ID] = m.CreatedAt
	f.order = append(f.order, m.ID)
	return m, true, nil
}

func (f *fakeRepository) FindByID(_ context.Context, tenantID, id uuid.UUID) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.messages[id]
	if !ok || m.TenantID != tenantID {
		return Message{}, ErrNotFound
	}
	return *m, nil
}

func (f *fakeRepository) PeekNext(_ context.Context, sessionID uuid.UUID) (Message, bool, *time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var head *Message
	var nextDue *time.Time
	for _, id := range f.order {
		m := f.messages[id]
		if m.SessionID != sessionID || m.Status != StatusQueued {
			continue
		}
		due := f.dueAt[id]
		if due.After(f.now()) {
			if nextDue == nil || due.Before(*nextDue) {
				d := due
				nextDue = &d
			}
			continue
		}
		if head == nil || (head.Priority == PriorityPromotional && m.Priority == PriorityTransactional) {
			head = m
		}
	}
	if head == nil {
		return Message{}, false, nextDue, nil
	}
	return *head, true, nil, nil
}

func (f *fakeRepository) MarkSending(_ context.Context, id uuid.UUID) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.messages[id]
	if m == nil || m.Status != StatusQueued {
		return nil, ErrNotFound
	}
	m.Status = StatusSending
	return f.attachments[id], nil
}

func (f *fakeRepository) Requeue(_ context.Context, id uuid.UUID, dueAt time.Time, attempts int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.messages[id]
	m.Status, m.Attempts = StatusQueued, attempts
	f.dueAt[id] = dueAt
	return nil
}

func (f *fakeRepository) Finish(_ context.Context, id uuid.UUID, u StatusUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.messages[id]
	m.Status, m.Error, m.ProviderMessageID, m.SentAt, m.Attempts = u.Status, u.Error, u.ProviderMessageID, u.SentAt, u.Attempts
	delete(f.attachments, id)
	return nil
}

func (f *fakeRepository) CountSent(_ context.Context, sessionID uuid.UUID, priority Priority, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, m := range f.messages {
		if m.SessionID == sessionID && m.Priority == priority && m.SentAt != nil && !m.SentAt.Before(since) {
			count++
		}
	}
	return count, nil
}

func (f *fakeRepository) ApplyReceipt(_ context.Context, sessionID uuid.UUID, providerIDs []string, status Status, at time.Time) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rank := map[Status]int{StatusSent: 1, StatusDelivered: 2, StatusRead: 3}
	var updated []Message
	for _, m := range f.messages {
		if m.SessionID != sessionID || !slices.Contains(providerIDs, m.ProviderMessageID) || m.ProviderMessageID == "" {
			continue
		}
		if rank[m.Status] < 1 || rank[m.Status] >= rank[status] {
			continue
		}
		m.Status = status
		if m.DeliveredAt == nil {
			m.DeliveredAt = &at
		}
		if status == StatusRead {
			m.ReadAt = &at
		}
		updated = append(updated, *m)
	}
	return updated, nil
}

func (f *fakeRepository) ListFiltered(context.Context, Filter) ([]Message, error) { return nil, nil }
func (f *fakeRepository) CountByStatusSince(context.Context, *uuid.UUID, time.Time) (map[Status]int, error) {
	return nil, nil
}
func (f *fakeRepository) QueueDepth(context.Context, *uuid.UUID) (int, error) { return 0, nil }

func (f *fakeRepository) RecoverSending(context.Context) (int, error) { return 0, nil }
func (f *fakeRepository) SessionsWithQueued(context.Context) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeRepository) get(id uuid.UUID) Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.messages[id]
}

type fakeSessions struct {
	id        uuid.UUID
	connected bool
	missing   bool
	firstConn time.Time
}

func (f *fakeSessions) Resolve(context.Context, uuid.UUID, string, channel.Type) (SessionInfo, error) {
	if f.missing {
		return SessionInfo{}, ErrSessionNotFound
	}
	return SessionInfo{ID: f.id, Connected: f.connected}, nil
}

func (f *fakeSessions) Lookup(context.Context, uuid.UUID) (SessionDetails, error) {
	if f.missing {
		return SessionDetails{}, ErrSessionNotFound
	}
	return SessionDetails{Connected: f.connected, FirstConnectedAt: f.firstConn}, nil
}

type fakeOptOuts map[string]bool

func (f fakeOptOuts) IsOptedOut(_ context.Context, _ uuid.UUID, recipient string) (bool, error) {
	return f[recipient], nil
}

type notified struct {
	tenantID  uuid.UUID
	eventType string
	data      EventData
}

type fakeNotifier struct{ events []notified }

func (f *fakeNotifier) Notify(_ context.Context, tenantID uuid.UUID, eventType string, data any) error {
	f.events = append(f.events, notified{tenantID: tenantID, eventType: eventType, data: data.(EventData)})
	return nil
}

type fakeWaker struct{ woken []uuid.UUID }

func (f *fakeWaker) Wake(id uuid.UUID) { f.woken = append(f.woken, id) }

type fakeChannel struct {
	sent    []channel.OutboundMessage
	results []error // popped per Send; empty means success
}

func (f *fakeChannel) Type() channel.Type                               { return channel.WhatsApp }
func (f *fakeChannel) StartPairing(context.Context, uuid.UUID) error    { return nil }
func (f *fakeChannel) Restore(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeChannel) Logout(context.Context, uuid.UUID, string) error  { return nil }
func (f *fakeChannel) Close()                                           {}
func (f *fakeChannel) Send(_ context.Context, _ uuid.UUID, m channel.OutboundMessage) (channel.SendResult, error) {
	f.sent = append(f.sent, m)
	if len(f.results) > 0 {
		err := f.results[0]
		f.results = f.results[1:]
		if err != nil {
			return channel.SendResult{}, err
		}
	}
	return channel.SendResult{ProviderMessageID: "WA" + string(rune('A'+len(f.sent)))}, nil
}

// harness wires a Service and a Dispatcher over fakes, with a controllable clock.
type harness struct {
	t          *testing.T
	clock      time.Time
	random     float64
	sessionID  uuid.UUID
	repo       *fakeRepository
	sessions   *fakeSessions
	optOuts    fakeOptOuts
	waker      *fakeWaker
	notifier   *fakeNotifier
	channel    *fakeChannel
	service    *Service
	dispatcher *Dispatcher
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t: t, clock: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), random: 0.5,
		sessionID: uuid.New(), optOuts: fakeOptOuts{}, waker: &fakeWaker{}, notifier: &fakeNotifier{}, channel: &fakeChannel{},
	}
	h.repo = newFakeRepository(func() time.Time { return h.clock })
	h.sessions = &fakeSessions{id: h.sessionID, connected: true, firstConn: h.clock.Add(-30 * 24 * time.Hour)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.service = NewService(h.repo, h.sessions, h.optOuts, h.waker, h.notifier, log)

	cfg := DispatcherConfig{
		Policy: limits.Default(), MaxAttempts: 3, RetryBase: 30 * time.Second,
		NotConnectedRetry: 30 * time.Second, MaxAge: time.Hour,
	}
	h.dispatcher = NewDispatcher(h.repo, h.sessions, h.optOuts, []channel.Channel{h.channel}, h.notifier, cfg, log)
	h.dispatcher.now = func() time.Time { return h.clock }
	h.dispatcher.random = func() float64 { return h.random }
	t.Cleanup(h.dispatcher.Stop)
	return h
}

func (h *harness) enqueue(priority, to string) Message {
	h.t.Helper()
	req := textRequest()
	req.Priority, req.To = priority, to
	msg, err := h.service.Send(context.Background(), uuid.New(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return msg
}

func (h *harness) process() (delay time.Duration, stop bool) {
	next := h.dispatcher.process(context.Background(), h.sessionID)
	return next.Delay, next.Stop
}

func (h *harness) advance(d time.Duration) { h.clock = h.clock.Add(d) }
