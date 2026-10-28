package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidURL wraps the reason a webhook URL was rejected.
var ErrInvalidURL = errors.New("invalid webhook url")

// Settings is where a tenant's webhook URL and signing secret live.
type Settings interface {
	// WebhookEndpoint returns the URL (empty when not configured) and the secret.
	WebhookEndpoint(ctx context.Context, tenantID uuid.UUID) (url, secret string, err error)
	// SetWebhookURL stores the URL; an empty value turns webhooks off.
	SetWebhookURL(ctx context.Context, tenantID uuid.UUID, url string) error
}

// Waker tells the dispatcher that a tenant has a new event.
type Waker interface {
	Wake(tenantID uuid.UUID)
}

// Service queues events for delivery and manages a tenant's webhook settings.
type Service struct {
	repo          Repository
	settings      Settings
	waker         Waker
	allowInsecure bool
	now           func() time.Time
}

// NewService creates a Service. allowInsecure permits http URLs (development).
func NewService(repo Repository, settings Settings, waker Waker, allowInsecure bool) *Service {
	return &Service{repo: repo, settings: settings, waker: waker, allowInsecure: allowInsecure, now: time.Now}
}

// Notify queues an event for a tenant. It does nothing when the tenant has no
// webhook URL, and the event is not backfilled if one is configured later.
func (s *Service) Notify(ctx context.Context, tenantID uuid.UUID, eventType string, data any) error {
	url, _, err := s.settings.WebhookEndpoint(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("load webhook settings: %w", err)
	}
	if url == "" {
		return nil
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate webhook event id: %w", err)
	}
	createdAt := s.now().UTC()
	body, err := json.Marshal(Envelope{ID: id.String(), Type: eventType, CreatedAt: createdAt, Data: data})
	if err != nil {
		return fmt.Errorf("encode webhook event: %w", err)
	}

	if err := s.repo.Insert(ctx, Event{ID: id, TenantID: tenantID, Type: eventType, Payload: body, Status: StatusPending}); err != nil {
		return err
	}
	s.waker.Wake(tenantID)
	return nil
}

// SetURL validates and stores a tenant's webhook URL; an empty URL disables webhooks.
func (s *Service) SetURL(ctx context.Context, tenantID uuid.UUID, url string) error {
	if url != "" {
		if err := ValidateURL(url, s.allowInsecure); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidURL, err)
		}
	}
	return s.settings.SetWebhookURL(ctx, tenantID, url)
}

// Config returns a tenant's webhook URL and secret.
func (s *Service) Config(ctx context.Context, tenantID uuid.UUID) (url, secret string, err error) {
	return s.settings.WebhookEndpoint(ctx, tenantID)
}

// Recent lists a tenant's latest events, newest first.
func (s *Service) Recent(ctx context.Context, tenantID uuid.UUID, limit int) ([]Event, error) {
	return s.repo.ListRecent(ctx, tenantID, min(max(limit, 1), 200))
}

// CountByStatus counts events by delivery state, for metrics.
func (s *Service) CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[Status]int, error) {
	return s.repo.CountByStatus(ctx, tenantID)
}
