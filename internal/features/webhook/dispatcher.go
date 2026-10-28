package webhook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/queue"
)

const (
	idleDelay  = time.Minute
	errorDelay = 15 * time.Second
	// retryFactor multiplies the wait on every failure: base, 4x base, 16x base...
	retryFactor      = 4
	maxResponseDrain = 4 << 10
)

// DispatcherConfig tunes delivery.
type DispatcherConfig struct {
	// MaxAttempts is how many deliveries are tried before an event is marked failed.
	MaxAttempts int
	// RetryBase is the wait after the first failure (30s gives 30s, 2m, 8m, 32m, ...).
	RetryBase time.Duration
}

// Dispatcher delivers pending events, with one worker per tenant.
type Dispatcher struct {
	repo     Repository
	settings Settings
	client   *http.Client
	cfg      DispatcherConfig
	log      *slog.Logger
	runner   *queue.Runner
	now      func() time.Time
}

// NewDispatcher creates a Dispatcher. Call Start to resume pending events.
func NewDispatcher(repo Repository, settings Settings, client *http.Client, cfg DispatcherConfig, log *slog.Logger) *Dispatcher {
	d := &Dispatcher{repo: repo, settings: settings, client: client, cfg: cfg, log: log, now: time.Now}
	d.runner = queue.NewRunner(d.process)
	return d
}

// Start wakes the tenants that have events pending from before a restart.
func (d *Dispatcher) Start(ctx context.Context) error {
	tenants, err := d.repo.TenantsWithPending(ctx)
	if err != nil {
		return err
	}
	for _, id := range tenants {
		d.runner.Wake(id)
	}
	d.log.Info("webhook dispatcher started", "tenants_with_pending", len(tenants))
	return nil
}

// Wake tells a tenant's worker that there is an event to deliver.
func (d *Dispatcher) Wake(tenantID uuid.UUID) { d.runner.Wake(tenantID) }

// Stop interrupts the workers and waits for deliveries in progress.
func (d *Dispatcher) Stop() { d.runner.Stop() }

func (d *Dispatcher) process(ctx context.Context, tenantID uuid.UUID) queue.Next {
	event, found, nextDue, err := d.repo.PeekDue(ctx, tenantID)
	if err != nil {
		d.log.Error("peek webhook event", "tenant_id", tenantID, "error", err)
		return queue.Next{Delay: errorDelay}
	}
	if !found {
		if nextDue != nil {
			return queue.Next{Delay: min(max(nextDue.Sub(d.now()), time.Second), time.Hour)}
		}
		return queue.Next{Delay: idleDelay}
	}

	url, secret, err := d.settings.WebhookEndpoint(ctx, tenantID)
	if err != nil {
		d.log.Error("load webhook settings", "tenant_id", tenantID, "error", err)
		return queue.Next{Delay: errorDelay}
	}
	if url == "" {
		d.settle(ctx, func(c context.Context) error {
			return d.repo.MarkFailed(c, event.ID, event.Attempts, "webhook_url_not_set")
		}, event)
		return queue.Next{}
	}

	reason := d.deliver(ctx, url, secret, event)
	switch {
	case reason == "":
		d.settle(ctx, func(c context.Context) error { return d.repo.MarkDelivered(c, event.ID, event.Attempts+1) }, event)
	case ctx.Err() != nil:
		return queue.Next{Stop: true} // shutting down mid-delivery: retry after restart, uncounted
	default:
		attempts := event.Attempts + 1
		d.log.Warn("webhook delivery failed", "tenant_id", tenantID, "event_id", event.ID, "type", event.Type, "attempt", attempts, "reason", reason)
		if attempts >= d.cfg.MaxAttempts {
			d.settle(ctx, func(c context.Context) error { return d.repo.MarkFailed(c, event.ID, attempts, reason) }, event)
		} else {
			dueAt := d.now().Add(retryDelay(d.cfg.RetryBase, attempts))
			d.settle(ctx, func(c context.Context) error { return d.repo.Reschedule(c, event.ID, dueAt, attempts, reason) }, event)
		}
	}
	return queue.Next{}
}

// deliver POSTs the event and returns "" on success or a short reason on failure.
// Reasons never include the response body or the URL.
func (d *Dispatcher) deliver(ctx context.Context, url, secret string, event Event) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(event.Payload))
	if err != nil {
		return "invalid_request"
	}
	timestamp := d.now()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "atm-messaging-webhooks/1")
	req.Header.Set("X-Webhook-Id", event.ID.String())
	req.Header.Set("X-Webhook-Event", event.Type)
	req.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(timestamp.Unix(), 10))
	req.Header.Set("X-Signature", Sign(secret, timestamp, event.Payload))

	resp, err := d.client.Do(req)
	if err != nil {
		return describeError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "status_" + strconv.Itoa(resp.StatusCode)
	}
	return ""
}

// settle records an outcome even during shutdown, so a delivery that happened is not lost.
func (d *Dispatcher) settle(ctx context.Context, record func(context.Context) error, event Event) {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := record(recordCtx); err != nil {
		d.log.Error("record webhook outcome", "event_id", event.ID, "error", err)
	}
}

func retryDelay(base time.Duration, attempts int) time.Duration {
	delay := base
	for i := 1; i < attempts; i++ {
		delay *= retryFactor
	}
	return delay
}

func describeError(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, ErrBlockedAddress):
		return "blocked_address"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "connection_failed"
}
