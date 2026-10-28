package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/account"
)

// Errors returned by the Service.
var (
	ErrUnsupportedChannel = errors.New("unsupported channel")
	ErrSessionExists      = ErrAlreadyExists
)

const (
	eventTimeout = 5 * time.Second
	// disconnectGrace is how long a dropped connection may take to recover before
	// the tenant is told. WhatsApp connections blink often and reconnect by themselves.
	disconnectGrace = 60 * time.Second
)

// Webhook event types of a session's life.
const (
	EventConnected    = "session.connected"
	EventDisconnected = "session.disconnected"
)

// Reasons sent with session.disconnected.
const (
	ReasonConnectionLost = "connection_lost"
	ReasonLoggedOut      = "logged_out"
	ReasonBanned         = "banned"
)

// Notifier queues a webhook event for a tenant.
type Notifier interface {
	Notify(ctx context.Context, tenantID uuid.UUID, eventType string, data any) error
}

// EventData is the `data` of every session webhook.
type EventData struct {
	SessionID  string `json:"session_id"`
	AccountRef string `json:"account_ref"`
	Channel    string `json:"channel"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

// AccountResolver finds or creates the account a session belongs to.
type AccountResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID, externalRef string) (account.Account, error)
}

// Service holds the session business rules.
type Service struct {
	repo     Repository
	accounts AccountResolver
	channels map[channel.Type]channel.Channel
	notifier Notifier
	hub      *hub
	log      *slog.Logger

	disconnectGrace time.Duration

	mu       sync.Mutex
	reported map[uuid.UUID]Status      // last connection state told to the tenant
	pending  map[uuid.UUID]*time.Timer // disconnects waiting out the grace period
}

// NewService creates a Service managing sessions on the given channels.
func NewService(repo Repository, accounts AccountResolver, channels []channel.Channel, notifier Notifier, log *slog.Logger) *Service {
	byType := make(map[channel.Type]channel.Channel, len(channels))
	for _, ch := range channels {
		byType[ch.Type()] = ch
	}
	return &Service{
		repo: repo, accounts: accounts, channels: byType, notifier: notifier, hub: newHub(), log: log,
		disconnectGrace: disconnectGrace,
		reported:        make(map[uuid.UUID]Status),
		pending:         make(map[uuid.UUID]*time.Timer),
	}
}

// Create registers the session of an account on a channel and starts pairing.
// It fails with ErrSessionExists when the account already has one for that channel.
func (s *Service) Create(ctx context.Context, tenantID uuid.UUID, accountRef string, chType channel.Type) (Session, error) {
	ch, ok := s.channels[chType]
	if !ok {
		return Session{}, ErrUnsupportedChannel
	}

	acc, err := s.accounts.Resolve(ctx, tenantID, accountRef)
	if err != nil {
		return Session{}, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Session{}, fmt.Errorf("generate session id: %w", err)
	}
	sess := Session{ID: id, TenantID: tenantID, AccountID: acc.ID, AccountRef: acc.ExternalRef, Channel: chType, Status: StatusPendingQR}
	if err := s.repo.Insert(ctx, sess); err != nil {
		return Session{}, err
	}

	if err := ch.StartPairing(ctx, id); err != nil {
		// Leave no half-created session behind: the account must be able to retry.
		if delErr := s.repo.Delete(ctx, tenantID, id); delErr != nil {
			s.log.Error("cleanup failed session", "session_id", id, "error", delErr)
		}
		return Session{}, fmt.Errorf("start pairing: %w", err)
	}
	return s.repo.FindByID(ctx, tenantID, id)
}

// Get returns a session of the tenant.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (Session, error) {
	return s.repo.FindByID(ctx, tenantID, id)
}

// List returns the tenant's sessions, optionally filtered by account reference.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, accountRef string) ([]Session, error) {
	return s.repo.List(ctx, tenantID, accountRef)
}

// Lookup returns a session by ID without tenant scoping. It is for internal
// components (the message dispatcher) that already hold a trusted session ID.
func (s *Service) Lookup(ctx context.Context, id uuid.UUID) (Session, error) {
	return s.repo.FindByIDUnscoped(ctx, id)
}

// Disconnect unlinks the number from its channel but keeps the session, so it
// can be paired again with a new QR code. Unlike Delete, nothing else is removed.
func (s *Service) Disconnect(ctx context.Context, id uuid.UUID) error {
	sess, err := s.repo.FindByIDUnscoped(ctx, id)
	if err != nil {
		return err
	}
	if ch, ok := s.channels[sess.Channel]; ok {
		if err := ch.Logout(ctx, sess.ID, sess.ChannelRef); err != nil {
			return fmt.Errorf("logout session: %w", err)
		}
	}
	// Same path as a logout done from the phone: clears the credentials
	// reference, marks the session disconnected and tells the tenant.
	s.HandleEvent(channel.Event{SessionID: id, Kind: channel.EventLoggedOut, Detail: "disconnected by the operator"})
	return nil
}

// ListAll lists sessions across tenants, for administration.
func (s *Service) ListAll(ctx context.Context, filter Filter) ([]Session, error) {
	filter.Limit = min(max(filter.Limit, 1), 500)
	filter.Offset = max(filter.Offset, 0)
	return s.repo.ListFiltered(ctx, filter)
}

// CountByStatus counts sessions by state, optionally for one tenant.
func (s *Service) CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[Status]int, error) {
	return s.repo.CountByStatus(ctx, tenantID)
}

// DeleteAny logs a session out and removes it whichever tenant owns it, for administration.
func (s *Service) DeleteAny(ctx context.Context, id uuid.UUID) error {
	sess, err := s.repo.FindByIDUnscoped(ctx, id)
	if err != nil {
		return err
	}
	return s.Delete(ctx, sess.TenantID, id)
}

// Find returns the single session of an account on a channel.
func (s *Service) Find(ctx context.Context, tenantID uuid.UUID, accountRef string, chType channel.Type) (Session, error) {
	return s.repo.FindByAccountChannel(ctx, tenantID, accountRef, chType)
}

// Delete logs the session out of its channel and removes it.
func (s *Service) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	sess, err := s.repo.FindByID(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if ch, ok := s.channels[sess.Channel]; ok {
		if err := ch.Logout(ctx, sess.ID, sess.ChannelRef); err != nil {
			return fmt.Errorf("logout session: %w", err)
		}
	}
	if err := s.repo.Delete(ctx, tenantID, id); err != nil {
		return err
	}
	s.hub.forget(id)
	s.forgetReported(id)
	return nil
}

// Watch returns the live events of a session (QR codes, connection changes).
// If the session has no credentials and no pairing running, it starts one, so a
// client can recover from an expired QR by opening the stream again.
func (s *Service) Watch(ctx context.Context, tenantID, id uuid.UUID) (Session, <-chan channel.Event, func(), error) {
	sess, err := s.repo.FindByID(ctx, tenantID, id)
	if err != nil {
		return Session{}, nil, nil, err
	}

	events, unsubscribe := s.hub.subscribe(id)

	if sess.ChannelRef == "" && sess.Status != StatusConnected && sess.Status != StatusBanned {
		ch, ok := s.channels[sess.Channel]
		if !ok {
			unsubscribe()
			return Session{}, nil, nil, ErrUnsupportedChannel
		}
		if err := ch.StartPairing(ctx, id); err != nil && !errors.Is(err, channel.ErrPairingInProgress) {
			unsubscribe()
			return Session{}, nil, nil, fmt.Errorf("start pairing: %w", err)
		}
	}
	return sess, events, unsubscribe, nil
}

// RestoreAll reconnects every paired session on startup. Each one connects in
// its own goroutine, so a slow or failing session does not delay the others.
func (s *Service) RestoreAll(ctx context.Context) error {
	sessions, err := s.repo.ListRestorable(ctx)
	if err != nil {
		return fmt.Errorf("list restorable sessions: %w", err)
	}
	for _, sess := range sessions {
		ch, ok := s.channels[sess.Channel]
		if !ok {
			continue
		}
		go func() {
			if err := ch.Restore(ctx, sess.ID, sess.ChannelRef); err != nil {
				s.log.Error("restore session failed", "session_id", sess.ID, "error", err)
				s.markDisconnected(sess.ID)
			}
		}()
	}
	s.log.Info("restoring sessions", "count", len(sessions))
	return nil
}

// HandleEvent is the channel.EventHandler: it persists the state changes and
// forwards the event to the streams watching the session.
func (s *Service) HandleEvent(evt channel.Event) {
	if evt.Kind == channel.EventMessageDelivered || evt.Kind == channel.EventMessageRead {
		return // message receipts belong to the message feature
	}
	ctx, cancel := context.WithTimeout(context.Background(), eventTimeout)
	defer cancel()

	update, changed := stateFor(evt)
	if changed {
		if err := s.repo.UpdateState(ctx, evt.SessionID, update); err != nil {
			s.log.Error("persist session state", "session_id", evt.SessionID, "event", evt.Kind, "error", err)
		}
	}
	s.hub.publish(evt)
	s.notifyTenant(evt)
}

// notifyTenant turns connection changes into webhooks. It only reports changes:
// repeated connects are silent, and a short drop that recovers within the grace
// period is never reported at all.
func (s *Service) notifyTenant(evt channel.Event) {
	switch evt.Kind {
	case channel.EventConnected:
		s.mu.Lock()
		s.cancelPendingLocked(evt.SessionID)
		changed := s.reported[evt.SessionID] != StatusConnected
		s.reported[evt.SessionID] = StatusConnected
		s.mu.Unlock()
		if changed {
			s.announce(evt.SessionID, EventConnected, StatusConnected, "")
		}

	case channel.EventDisconnected:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.reported[evt.SessionID] != StatusConnected || s.pending[evt.SessionID] != nil {
			return
		}
		id := evt.SessionID
		s.pending[id] = time.AfterFunc(s.disconnectGrace, func() { s.reportLoss(id, StatusDisconnected, ReasonConnectionLost) })

	case channel.EventLoggedOut:
		s.reportLoss(evt.SessionID, StatusDisconnected, ReasonLoggedOut)

	case channel.EventBanned:
		s.reportLoss(evt.SessionID, StatusBanned, ReasonBanned)
	}
}

// reportLoss tells the tenant a session went offline, unless it already knows.
func (s *Service) reportLoss(id uuid.UUID, status Status, reason string) {
	s.mu.Lock()
	s.cancelPendingLocked(id)
	changed := s.reported[id] != status && s.reported[id] != StatusBanned
	s.reported[id] = status
	s.mu.Unlock()
	if changed {
		s.announce(id, EventDisconnected, status, reason)
	}
}

func (s *Service) cancelPendingLocked(id uuid.UUID) {
	if timer := s.pending[id]; timer != nil {
		timer.Stop()
		delete(s.pending, id)
	}
}

// announce queues a session webhook for the session's tenant.
func (s *Service) announce(id uuid.UUID, eventType string, status Status, reason string) {
	if s.notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), eventTimeout)
	defer cancel()

	sess, err := s.repo.FindByIDUnscoped(ctx, id)
	if err != nil {
		s.log.Error("load session for webhook", "session_id", id, "error", err)
		return
	}
	data := EventData{SessionID: id.String(), AccountRef: sess.AccountRef, Channel: string(sess.Channel), Status: string(status), Reason: reason}
	if err := s.notifier.Notify(ctx, sess.TenantID, eventType, data); err != nil {
		s.log.Error("queue webhook", "session_id", id, "event", eventType, "error", err)
	}
}

func (s *Service) forgetReported(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelPendingLocked(id)
	delete(s.reported, id)
}

func (s *Service) markDisconnected(id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), eventTimeout)
	defer cancel()
	if err := s.repo.UpdateState(ctx, id, StateUpdate{Status: StatusDisconnected}); err != nil {
		s.log.Error("mark session disconnected", "session_id", id, "error", err)
	}
}

// stateFor maps a channel event to the state it implies, if any.
func stateFor(evt channel.Event) (StateUpdate, bool) {
	switch evt.Kind {
	case channel.EventPaired:
		return StateUpdate{Status: StatusPendingQR, ChannelRef: &evt.ChannelRef}, true
	case channel.EventConnected:
		now := time.Now()
		update := StateUpdate{Status: StatusConnected, ConnectedAt: &now}
		if evt.ChannelRef != "" {
			update.ChannelRef = &evt.ChannelRef
		}
		return update, true
	case channel.EventDisconnected, channel.EventPairingFailed:
		return StateUpdate{Status: StatusDisconnected}, true
	case channel.EventLoggedOut:
		empty := ""
		return StateUpdate{Status: StatusDisconnected, ChannelRef: &empty}, true
	case channel.EventBanned:
		return StateUpdate{Status: StatusBanned}, true
	}
	return StateUpdate{}, false
}
