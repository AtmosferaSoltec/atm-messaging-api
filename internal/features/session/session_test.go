package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/account"
)

type fakeRepository struct {
	sessions map[uuid.UUID]Session
}

func newFakeRepository() *fakeRepository { return &fakeRepository{sessions: map[uuid.UUID]Session{}} }

func (f *fakeRepository) Insert(_ context.Context, s Session) error {
	for _, existing := range f.sessions {
		if existing.TenantID == s.TenantID && existing.AccountID == s.AccountID && existing.Channel == s.Channel {
			return ErrAlreadyExists
		}
	}
	f.sessions[s.ID] = s
	return nil
}

func (f *fakeRepository) FindByID(_ context.Context, tenantID, id uuid.UUID) (Session, error) {
	s, ok := f.sessions[id]
	if !ok || s.TenantID != tenantID {
		return Session{}, ErrNotFound
	}
	return s, nil
}

func (f *fakeRepository) FindByIDUnscoped(_ context.Context, id uuid.UUID) (Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return s, nil
}

func (f *fakeRepository) FindByAccountChannel(context.Context, uuid.UUID, string, channel.Type) (Session, error) {
	return Session{}, ErrNotFound
}

func (f *fakeRepository) List(context.Context, uuid.UUID, string) ([]Session, error) { return nil, nil }

func (f *fakeRepository) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	s, ok := f.sessions[id]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	delete(f.sessions, id)
	return nil
}

func (f *fakeRepository) UpdateState(_ context.Context, id uuid.UUID, u StateUpdate) error {
	s := f.sessions[id]
	s.Status = u.Status
	if u.ChannelRef != nil {
		s.ChannelRef = *u.ChannelRef
	}
	f.sessions[id] = s
	return nil
}

func (f *fakeRepository) ListFiltered(context.Context, Filter) ([]Session, error) { return nil, nil }
func (f *fakeRepository) CountByStatus(context.Context, *uuid.UUID) (map[Status]int, error) {
	return nil, nil
}

func (f *fakeRepository) ListRestorable(context.Context) ([]Session, error) { return nil, nil }

// fakeAccounts gives each (tenant, ref) a stable account.
type fakeAccounts struct {
	ids map[string]uuid.UUID
}

func (f *fakeAccounts) Resolve(_ context.Context, tenantID uuid.UUID, ref string) (account.Account, error) {
	key := tenantID.String() + "/" + ref
	if f.ids == nil {
		f.ids = map[string]uuid.UUID{}
	}
	if _, ok := f.ids[key]; !ok {
		f.ids[key] = uuid.New()
	}
	return account.Account{ID: f.ids[key], TenantID: tenantID, ExternalRef: ref}, nil
}

type fakeChannel struct {
	pairingErr error
	paired     []uuid.UUID
}

func (f *fakeChannel) Type() channel.Type { return channel.WhatsApp }
func (f *fakeChannel) StartPairing(_ context.Context, id uuid.UUID) error {
	if f.pairingErr != nil {
		return f.pairingErr
	}
	f.paired = append(f.paired, id)
	return nil
}
func (f *fakeChannel) Restore(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeChannel) Send(context.Context, uuid.UUID, channel.OutboundMessage) (channel.SendResult, error) {
	return channel.SendResult{}, nil
}
func (f *fakeChannel) Logout(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeChannel) Close()                                          {}

func newTestService(ch *fakeChannel) (*Service, *fakeRepository) {
	repo := newFakeRepository()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(repo, &fakeAccounts{}, []channel.Channel{ch}, nil, log), repo
}

func TestCreateAllowsOnlyOneSessionPerAccountAndChannel(t *testing.T) {
	ch := &fakeChannel{}
	service, _ := newTestService(ch)
	tenantID := uuid.New()

	if _, err := service.Create(context.Background(), tenantID, "company-1", channel.WhatsApp); err != nil {
		t.Fatalf("first session should be created: %v", err)
	}
	if _, err := service.Create(context.Background(), tenantID, "company-1", channel.WhatsApp); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("second session for the same account and channel must fail with ErrSessionExists, got %v", err)
	}
	if _, err := service.Create(context.Background(), tenantID, "company-2", channel.WhatsApp); err != nil {
		t.Fatalf("another account of the same tenant should get its own session: %v", err)
	}
	if _, err := service.Create(context.Background(), uuid.New(), "company-1", channel.WhatsApp); err != nil {
		t.Fatalf("the same reference in another tenant is a different account: %v", err)
	}
	if len(ch.paired) != 3 {
		t.Errorf("pairing should start once per created session, got %d", len(ch.paired))
	}
}

func TestCreateRejectsUnsupportedChannel(t *testing.T) {
	service, _ := newTestService(&fakeChannel{})
	if _, err := service.Create(context.Background(), uuid.New(), "company-1", channel.Type("sms")); !errors.Is(err, ErrUnsupportedChannel) {
		t.Fatalf("expected ErrUnsupportedChannel, got %v", err)
	}
}

func TestCreateRemovesSessionWhenPairingFails(t *testing.T) {
	service, repo := newTestService(&fakeChannel{pairingErr: errors.New("boom")})
	tenantID := uuid.New()

	if _, err := service.Create(context.Background(), tenantID, "company-1", channel.WhatsApp); err == nil {
		t.Fatal("expected an error")
	}
	if len(repo.sessions) != 0 {
		t.Fatal("a failed pairing must not leave a session behind, or the account could never retry")
	}
}

func TestSessionsAreScopedToTheirTenant(t *testing.T) {
	service, _ := newTestService(&fakeChannel{})
	owner, other := uuid.New(), uuid.New()
	sess, err := service.Create(context.Background(), owner, "company-1", channel.WhatsApp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), other, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant must not see the session, got %v", err)
	}
	if err := service.Delete(context.Background(), other, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant must not delete the session, got %v", err)
	}
}

func TestStateFor(t *testing.T) {
	ref := "123@s.whatsapp.net"
	cases := []struct {
		kind    channel.EventKind
		changed bool
		status  Status
	}{
		{channel.EventQR, false, ""},
		{channel.EventPaired, true, StatusPendingQR},
		{channel.EventConnected, true, StatusConnected},
		{channel.EventDisconnected, true, StatusDisconnected},
		{channel.EventLoggedOut, true, StatusDisconnected},
		{channel.EventBanned, true, StatusBanned},
	}
	for _, tc := range cases {
		update, changed := stateFor(channel.Event{Kind: tc.kind, ChannelRef: ref})
		if changed != tc.changed || update.Status != tc.status {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.kind, update.Status, changed, tc.status, tc.changed)
		}
	}

	update, _ := stateFor(channel.Event{Kind: channel.EventLoggedOut})
	if update.ChannelRef == nil || *update.ChannelRef != "" {
		t.Error("logging out must clear the stored credentials reference")
	}
}

type recordedWebhook struct {
	eventType string
	data      EventData
}

type fakeNotifier struct {
	mu     sync.Mutex
	events []recordedWebhook
}

func (f *fakeNotifier) Notify(_ context.Context, _ uuid.UUID, eventType string, data any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, recordedWebhook{eventType: eventType, data: data.(EventData)})
	return nil
}

func (f *fakeNotifier) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, e := range f.events {
		out = append(out, e.eventType+":"+e.data.Reason)
	}
	return out
}

func newNotifyingService(t *testing.T) (*Service, *fakeNotifier, Session) {
	t.Helper()
	notifier := &fakeNotifier{}
	repo := newFakeRepository()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(repo, &fakeAccounts{}, []channel.Channel{&fakeChannel{}}, notifier, log)
	service.disconnectGrace = 30 * time.Millisecond
	sess, err := service.Create(context.Background(), uuid.New(), "company-1", channel.WhatsApp)
	if err != nil {
		t.Fatal(err)
	}
	return service, notifier, sess
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestConnectedIsReportedOnceAndWithTheAccount(t *testing.T) {
	service, notifier, sess := newNotifyingService(t)

	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected, ChannelRef: "ref"})
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected, ChannelRef: "ref"})

	if got := notifier.types(); len(got) != 1 || got[0] != "session.connected:" {
		t.Fatalf("expected a single session.connected, got %v", got)
	}
	if data := notifier.events[0].data; data.AccountRef != "company-1" || data.SessionID != sess.ID.String() || data.Status != "connected" {
		t.Errorf("unexpected payload %+v", data)
	}
}

func TestShortDropsAreNotReported(t *testing.T) {
	service, notifier, sess := newNotifyingService(t)
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected})

	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventDisconnected})
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected}) // recovered within the grace period
	time.Sleep(80 * time.Millisecond)

	if got := notifier.types(); len(got) != 1 {
		t.Fatalf("a blink must produce no webhooks beyond the first connect, got %v", got)
	}
}

func TestLongDropsAreReportedAfterTheGracePeriod(t *testing.T) {
	service, notifier, sess := newNotifyingService(t)
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected})

	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventDisconnected})
	eventually(t, func() bool { return len(notifier.types()) == 2 })

	if got := notifier.types()[1]; got != "session.disconnected:connection_lost" {
		t.Errorf("got %s", got)
	}

	// Coming back is reported again.
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected})
	if len(notifier.types()) != 3 {
		t.Errorf("reconnection after a reported loss must be reported, got %v", notifier.types())
	}
}

func TestLogoutAndBanAreReportedImmediately(t *testing.T) {
	service, notifier, sess := newNotifyingService(t)
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected})
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventLoggedOut})

	if got := notifier.types(); len(got) != 2 || got[1] != "session.disconnected:logged_out" {
		t.Fatalf("got %v", got)
	}

	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventBanned})
	if got := notifier.types(); len(got) != 3 || got[2] != "session.disconnected:banned" {
		t.Fatalf("got %v", got)
	}
	if notifier.events[2].data.Status != "banned" {
		t.Errorf("a ban reports the banned status, got %s", notifier.events[2].data.Status)
	}
}

func TestDeletingASessionStopsPendingReports(t *testing.T) {
	service, notifier, sess := newNotifyingService(t)
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected})
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventDisconnected})

	if err := service.Delete(context.Background(), sess.TenantID, sess.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)

	if got := notifier.types(); len(got) != 1 {
		t.Errorf("the tenant deleted the session itself; nothing more to report, got %v", got)
	}
}

func TestDisconnectKeepsTheSessionAndClearsItsCredentials(t *testing.T) {
	notifier := &fakeNotifier{}
	repo := newFakeRepository()
	ch := &fakeChannel{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(repo, &fakeAccounts{}, []channel.Channel{ch}, notifier, log)

	sess, err := service.Create(context.Background(), uuid.New(), "company-1", channel.WhatsApp)
	if err != nil {
		t.Fatal(err)
	}
	service.HandleEvent(channel.Event{SessionID: sess.ID, Kind: channel.EventConnected, ChannelRef: "ref"})

	if err := service.Disconnect(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}

	stored, ok := repo.sessions[sess.ID]
	if !ok {
		t.Fatal("disconnecting must keep the session so it can be paired again")
	}
	if stored.Status != StatusDisconnected || stored.ChannelRef != "" {
		t.Errorf("expected disconnected without credentials, got %s / %q", stored.Status, stored.ChannelRef)
	}
	if got := notifier.types(); len(got) != 2 || got[1] != "session.disconnected:logged_out" {
		t.Errorf("the tenant must be told, got %v", got)
	}
	if err := service.Disconnect(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown session is ErrNotFound, got %v", err)
	}
}
