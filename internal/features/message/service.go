package message

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

// Errors returned by the Service.
var (
	ErrSessionNotFound     = errors.New("account has no session for this channel")
	ErrSessionNotConnected = errors.New("session is not connected")
	ErrRecipientOptedOut   = errors.New("recipient opted out of promotional messages")
)

const receiptTimeout = 10 * time.Second

// SessionInfo is what accepting a message needs to know about a session.
type SessionInfo struct {
	ID        uuid.UUID
	Connected bool
}

// SessionResolver finds the single session of an account on a channel. It
// returns ErrSessionNotFound when there is none.
type SessionResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID, accountRef string, ch channel.Type) (SessionInfo, error)
}

// Waker tells the dispatcher that a session has a new message to send.
type Waker interface {
	Wake(sessionID uuid.UUID)
}

// Service accepts messages and queues them; the Dispatcher delivers them.
type Service struct {
	repo     Repository
	sessions SessionResolver
	optOuts  OptOutChecker
	waker    Waker
	notifier Notifier
	log      *slog.Logger
}

// NewService creates a Service.
func NewService(repo Repository, sessions SessionResolver, optOuts OptOutChecker, waker Waker, notifier Notifier, log *slog.Logger) *Service {
	return &Service{repo: repo, sessions: sessions, optOuts: optOuts, waker: waker, notifier: notifier, log: log}
}

// Send validates a message and puts it in the session's queue. Repeating a
// request with the same idempotency key returns the original message.
func (s *Service) Send(ctx context.Context, tenantID uuid.UUID, req SendRequest) (Message, error) {
	v, err := validate(req)
	if err != nil {
		return Message{}, err
	}
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	if req.AccountRef == "" {
		return Message{}, invalid("invalid_account_ref", "account_ref is required")
	}
	if len(req.IdempotencyKey) > 100 {
		return Message{}, invalid("invalid_idempotency_key", "idempotency_key must be at most 100 characters")
	}

	sess, err := s.sessions.Resolve(ctx, tenantID, req.AccountRef, v.channel)
	if err != nil {
		return Message{}, err
	}
	if !sess.Connected {
		return Message{}, ErrSessionNotConnected
	}

	if v.priority == PriorityPromotional {
		optedOut, err := s.optOuts.IsOptedOut(ctx, sess.ID, v.recipient)
		if err != nil {
			return Message{}, fmt.Errorf("check opt-out: %w", err)
		}
		if optedOut {
			return Message{}, ErrRecipientOptedOut
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("generate message id: %w", err)
	}
	msg := Message{
		ID: id, TenantID: tenantID, SessionID: sess.ID, Channel: v.channel,
		Recipient: v.recipient, Kind: v.kind, Body: v.body, Priority: v.priority,
		IdempotencyKey: req.IdempotencyKey, Status: StatusQueued,
	}
	var attachmentData []byte
	if v.attachment != nil {
		msg.AttachmentFilename = v.attachment.Filename
		msg.AttachmentMime = v.attachment.MimeType
		msg.AttachmentSize = int64(len(v.attachment.Data))
		attachmentData = v.attachment.Data
	}

	stored, created, err := s.repo.Insert(ctx, msg, attachmentData)
	if err != nil {
		return Message{}, err
	}
	if created {
		s.waker.Wake(sess.ID)
	}
	return stored, nil
}

// Get returns a message of the tenant.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (Message, error) {
	return s.repo.FindByID(ctx, tenantID, id)
}

// HandleEvent is the channel.EventHandler for message receipts: it moves messages
// forward to delivered or read and tells the tenant. Other events are ignored.
func (s *Service) HandleEvent(evt channel.Event) {
	var status Status
	var eventType string
	switch evt.Kind {
	case channel.EventMessageDelivered:
		status, eventType = StatusDelivered, EventDelivered
	case channel.EventMessageRead:
		status, eventType = StatusRead, EventRead
	default:
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), receiptTimeout)
	defer cancel()

	updated, err := s.repo.ApplyReceipt(ctx, evt.SessionID, evt.ProviderMessageIDs, status, time.Now())
	if err != nil {
		s.log.Error("apply receipt", "session_id", evt.SessionID, "status", status, "error", err)
		return
	}
	for _, msg := range updated {
		notify(ctx, s.notifier, s.log, msg, eventType)
	}
}

// ListAll lists messages across tenants, for administration. Bodies are not exposed by the API.
func (s *Service) ListAll(ctx context.Context, filter Filter) ([]Message, error) {
	filter.Limit = min(max(filter.Limit, 1), 500)
	filter.Offset = max(filter.Offset, 0)
	return s.repo.ListFiltered(ctx, filter)
}

// CountByStatusSince counts messages created since a time, by status.
func (s *Service) CountByStatusSince(ctx context.Context, tenantID *uuid.UUID, since time.Time) (map[Status]int, error) {
	return s.repo.CountByStatusSince(ctx, tenantID, since)
}

// QueueDepth counts messages waiting to be sent or being sent.
func (s *Service) QueueDepth(ctx context.Context, tenantID *uuid.UUID) (int, error) {
	return s.repo.QueueDepth(ctx, tenantID)
}

// HourlyActivity groups the messages created since a time by hour.
func (s *Service) HourlyActivity(ctx context.Context, tenantID *uuid.UUID, since time.Time) ([]HourlyCount, error) {
	return s.repo.HourlyActivity(ctx, tenantID, since)
}
