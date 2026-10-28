package message

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/limits"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/queue"
)

const (
	// idleDelay is how long an idle worker sleeps; any new message wakes it sooner.
	idleDelay = time.Minute
	// errorDelay is the pause after an infrastructure error (database, lookup).
	errorDelay  = 15 * time.Second
	sendTimeout = 2 * time.Minute
)

// DispatcherConfig tunes retries and expiry. The sending limits live in limits.Policy.
type DispatcherConfig struct {
	Policy limits.Policy
	// MaxAttempts is how many failed sends a message gets before it fails for good.
	MaxAttempts int
	// RetryBase is the wait after the first failure; it doubles on each retry.
	RetryBase time.Duration
	// NotConnectedRetry is how often a message is retried while its session is offline.
	NotConnectedRetry time.Duration
	// MaxAge is how long a message may wait in the queue before it expires.
	MaxAge time.Duration
}

// SessionDetails is what the dispatcher needs to know about a session.
type SessionDetails struct {
	Connected        bool
	FirstConnectedAt time.Time // zero until the number connects for the first time
}

// SessionLookup finds a session by ID. It returns ErrSessionNotFound when it no longer exists.
type SessionLookup interface {
	Lookup(ctx context.Context, sessionID uuid.UUID) (SessionDetails, error)
}

// OptOutChecker tells whether a recipient unsubscribed from a session's promotions.
type OptOutChecker interface {
	IsOptedOut(ctx context.Context, sessionID uuid.UUID, recipient string) (bool, error)
}

// pacing is the in-memory sending rhythm of one session.
type pacing struct {
	lastSentAt     time.Time
	promoNotBefore time.Time // chosen once per promotional message, so a wake-up does not re-roll it
}

// Dispatcher delivers queued messages with one worker per session, applying the
// sending limits and retries. The queue itself is the messages table, so it
// survives restarts. It assumes a single running instance.
type Dispatcher struct {
	repo     Repository
	sessions SessionLookup
	optOuts  OptOutChecker
	channels map[channel.Type]channel.Channel
	notifier Notifier
	cfg      DispatcherConfig
	log      *slog.Logger
	runner   *queue.Runner

	now    func() time.Time
	random func() float64

	mu     sync.Mutex
	pacing map[uuid.UUID]*pacing
}

// NewDispatcher creates a Dispatcher. Call Start to resume pending work.
func NewDispatcher(repo Repository, sessions SessionLookup, optOuts OptOutChecker, channels []channel.Channel, notifier Notifier, cfg DispatcherConfig, log *slog.Logger) *Dispatcher {
	byType := make(map[channel.Type]channel.Channel, len(channels))
	for _, ch := range channels {
		byType[ch.Type()] = ch
	}
	d := &Dispatcher{
		repo: repo, sessions: sessions, optOuts: optOuts, channels: byType, notifier: notifier, cfg: cfg, log: log,
		now: time.Now, random: rand.Float64, pacing: make(map[uuid.UUID]*pacing),
	}
	d.runner = queue.NewRunner(d.process)
	return d
}

// Start requeues what a crash left half-sent and wakes the sessions with pending messages.
func (d *Dispatcher) Start(ctx context.Context) error {
	recovered, err := d.repo.RecoverSending(ctx)
	if err != nil {
		return err
	}
	sessionIDs, err := d.repo.SessionsWithQueued(ctx)
	if err != nil {
		return err
	}
	for _, id := range sessionIDs {
		d.runner.Wake(id)
	}
	d.log.Info("dispatcher started", "sessions_with_queue", len(sessionIDs), "recovered_sending", recovered)
	return nil
}

// Wake tells a session's worker that there is something to send.
func (d *Dispatcher) Wake(sessionID uuid.UUID) { d.runner.Wake(sessionID) }

// Stop interrupts the workers and waits for the send in progress to be recorded.
func (d *Dispatcher) Stop() { d.runner.Stop() }

// process does one unit of work for a session: it sends at most one message and
// tells the runner how long to wait before the next one.
func (d *Dispatcher) process(ctx context.Context, sessionID uuid.UUID) queue.Next {
	session, err := d.sessions.Lookup(ctx, sessionID)
	if errors.Is(err, ErrSessionNotFound) {
		d.forget(sessionID)
		return queue.Next{Stop: true}
	}
	if err != nil {
		d.log.Error("lookup session", "session_id", sessionID, "error", err)
		return queue.Next{Delay: errorDelay}
	}

	head, found, nextDue, err := d.repo.PeekNext(ctx, sessionID)
	if err != nil {
		d.log.Error("peek queue", "session_id", sessionID, "error", err)
		return queue.Next{Delay: errorDelay}
	}
	now := d.now()
	if !found {
		if nextDue != nil {
			return queue.Next{Delay: clampDelay(nextDue.Sub(now))}
		}
		return queue.Next{Delay: idleDelay}
	}

	// Messages that can no longer be sent are settled without touching the channel.
	if now.Sub(head.CreatedAt) > d.cfg.MaxAge {
		return d.fail(ctx, head, FailureExpired)
	}
	if head.Priority == PriorityPromotional {
		optedOut, err := d.optOuts.IsOptedOut(ctx, sessionID, head.Recipient)
		if err != nil {
			d.log.Error("check opt-out", "session_id", sessionID, "error", err)
			return queue.Next{Delay: errorDelay}
		}
		if optedOut {
			return d.fail(ctx, head, FailureRecipientOptedOut)
		}
	}

	if !session.Connected {
		return queue.Next{Delay: d.cfg.NotConnectedRetry}
	}
	if delay := d.waitFor(ctx, sessionID, head, session, now); delay > 0 {
		return queue.Next{Delay: delay}
	}
	return d.send(ctx, sessionID, head)
}

// waitFor returns how long the session must wait before sending head (zero if it can go now).
func (d *Dispatcher) waitFor(ctx context.Context, sessionID uuid.UUID, head Message, session SessionDetails, now time.Time) time.Duration {
	policy := d.cfg.Policy
	priority := limits.Priority(head.Priority)

	sentToday, err := d.repo.CountSent(ctx, sessionID, head.Priority, policy.DayStart(now))
	if err != nil {
		d.log.Error("count sent messages", "session_id", sessionID, "error", err)
		return errorDelay
	}
	if sentToday >= policy.DailyCap(priority, session.FirstConnectedAt, now) {
		// Daily cap reached: sleep until tomorrow, or until a higher-priority message arrives.
		return clampDelay(policy.NextDayStart(now).Sub(now))
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.pacingFor(sessionID)
	if state.lastSentAt.IsZero() {
		return 0
	}
	notBefore := state.lastSentAt.Add(policy.Gap(priority, 0))
	if head.Priority == PriorityPromotional {
		if state.promoNotBefore.IsZero() {
			state.promoNotBefore = state.lastSentAt.Add(policy.Gap(priority, d.random()))
		}
		notBefore = state.promoNotBefore
	}
	return max(notBefore.Sub(now), 0)
}

func (d *Dispatcher) send(ctx context.Context, sessionID uuid.UUID, head Message) queue.Next {
	ch, ok := d.channels[head.Channel]
	if !ok {
		return d.fail(ctx, head, FailureSendFailed)
	}

	attachmentData, err := d.repo.MarkSending(ctx, head.ID)
	if errors.Is(err, ErrNotFound) {
		return queue.Next{} // someone else settled it; look at the queue again
	}
	if err != nil {
		d.log.Error("claim message", "message_id", head.ID, "error", err)
		return queue.Next{Delay: errorDelay}
	}

	outbound := channel.OutboundMessage{To: head.Recipient, Kind: head.Kind, Body: head.Body}
	if head.AttachmentSize > 0 {
		outbound.Attachment = &channel.Attachment{Filename: head.AttachmentFilename, MimeType: head.AttachmentMime, Data: attachmentData}
	}

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	result, sendErr := ch.Send(sendCtx, sessionID, outbound)
	cancel()

	now := d.now()
	switch {
	case sendErr == nil:
		d.recordSent(sessionID, now)
		finish(ctx, d, head, StatusUpdate{Status: StatusSent, ProviderMessageID: result.ProviderMessageID, SentAt: &now, Attempts: head.Attempts + 1})
		return queue.Next{}

	case ctx.Err() != nil:
		// Shutting down mid-send: leave it queued without counting an attempt.
		d.requeue(head, now, head.Attempts)
		return queue.Next{Stop: true}

	case errors.Is(sendErr, channel.ErrSessionNotConnected):
		d.requeue(head, now.Add(d.cfg.NotConnectedRetry), head.Attempts)
		return queue.Next{Delay: d.cfg.NotConnectedRetry}

	case errors.Is(sendErr, channel.ErrRecipientNotFound):
		return d.fail(ctx, head, FailureRecipientNotFound)
	}

	attempts := head.Attempts + 1
	d.log.Warn("send failed", "message_id", head.ID, "session_id", sessionID, "attempt", attempts, "error", sendErr)
	if attempts >= d.cfg.MaxAttempts {
		head.Attempts = attempts
		return d.fail(ctx, head, FailureSendFailed)
	}
	d.requeue(head, now.Add(d.cfg.RetryBase<<(attempts-1)), attempts)
	return queue.Next{}
}

// fail settles a message as failed for good and moves on to the next one.
func (d *Dispatcher) fail(ctx context.Context, msg Message, code string) queue.Next {
	finish(ctx, d, msg, StatusUpdate{Status: StatusFailed, Error: code, Attempts: msg.Attempts})
	return queue.Next{}
}

func finish(ctx context.Context, d *Dispatcher, msg Message, update StatusUpdate) {
	// A shutdown must not lose the outcome of a send that already happened.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := d.repo.Finish(recordCtx, msg.ID, update); err != nil {
		d.log.Error("record message outcome", "message_id", msg.ID, "status", update.Status, "error", err)
		return
	}

	msg.Status, msg.Error, msg.ProviderMessageID, msg.SentAt, msg.Attempts = update.Status, update.Error, update.ProviderMessageID, update.SentAt, update.Attempts
	eventType := EventSent
	if update.Status == StatusFailed {
		eventType = EventFailed
	}
	notify(recordCtx, d.notifier, d.log, msg, eventType)
}

func (d *Dispatcher) requeue(msg Message, dueAt time.Time, attempts int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.repo.Requeue(ctx, msg.ID, dueAt, attempts); err != nil {
		d.log.Error("requeue message", "message_id", msg.ID, "error", err)
	}
}

func (d *Dispatcher) recordSent(sessionID uuid.UUID, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.pacingFor(sessionID)
	state.lastSentAt = at
	state.promoNotBefore = time.Time{}
}

func (d *Dispatcher) forget(sessionID uuid.UUID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pacing, sessionID)
}

// pacingFor returns the session's rhythm; the caller must hold d.mu.
func (d *Dispatcher) pacingFor(sessionID uuid.UUID) *pacing {
	state, ok := d.pacing[sessionID]
	if !ok {
		state = &pacing{}
		d.pacing[sessionID] = state
	}
	return state
}

// clampDelay keeps a computed wait within sane bounds: never zero (which would
// spin) and never longer than an hour (so config or clock changes are noticed).
func clampDelay(delay time.Duration) time.Duration {
	return min(max(delay, time.Second), time.Hour)
}
