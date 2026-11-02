package email

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	tenantRateLimit    = 60
	tenantRateWindow   = time.Minute
	addressRateLimit   = 5
	addressRateWindow  = time.Hour
	metricsWindow      = 24 * time.Hour
	metricsWindowLabel = "24h"
)

// Waker nudges the delivery worker that there is something to send.
type Waker interface{ Wake() }

// Service holds the business rules of the email feature.
type Service struct {
	repo    Repository
	waker   Waker
	enabled bool
	limiter *tenantLimiter
	now     func() time.Time
}

// NewService creates a Service. enabled is false when no provider is configured:
// sending is then refused with ErrUnavailable.
func NewService(repo Repository, waker Waker, enabled bool) *Service {
	s := &Service{repo: repo, waker: waker, enabled: enabled, now: time.Now}
	s.limiter = newTenantLimiter(tenantRateLimit, tenantRateWindow, func() time.Time { return s.now() })
	return s
}

// Enqueue validates and stores an email for delivery. Repeating an idempotency
// key returns the original email and sends nothing new.
func (s *Service) Enqueue(ctx context.Context, tenantID uuid.UUID, req SendRequest) (Email, error) {
	if !s.enabled {
		return Email{}, ErrUnavailable
	}
	to, err := NormalizeAddress(req.To)
	if err != nil {
		return Email{}, err
	}
	if err := ValidateIdempotencyKey(req.IdempotencyKey); err != nil {
		return Email{}, err
	}
	data, err := ValidateData(req.Template, req.Data)
	if err != nil {
		return Email{}, err
	}

	if req.IdempotencyKey != "" {
		existing, found, err := s.repo.FindByKey(ctx, tenantID, req.IdempotencyKey)
		if err != nil {
			return Email{}, err
		}
		if found {
			return existing, nil
		}
	}

	if !s.limiter.allow(tenantID) {
		return Email{}, ErrRateLimited
	}
	recent, err := s.repo.CountToAddressSince(ctx, tenantID, to, s.now().Add(-addressRateWindow))
	if err != nil {
		return Email{}, err
	}
	if recent >= addressRateLimit {
		return Email{}, ErrRateLimited
	}

	stored, created, err := s.repo.Insert(ctx, Email{
		ID: uuid.New(), TenantID: tenantID, To: to, Template: req.Template, Data: data, IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return Email{}, err
	}
	if created {
		s.waker.Wake()
	}
	return stored, nil
}

// Get returns an email of the tenant.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (Email, error) {
	return s.repo.Get(ctx, tenantID, id)
}

// Metrics counts the emails of the last 24 hours per tenant and status.
func (s *Service) Metrics(ctx context.Context) (MetricsResponse, error) {
	now := s.now()
	counts, err := s.repo.CountByTenantSince(ctx, now.Add(-metricsWindow))
	if err != nil {
		return MetricsResponse{}, fmt.Errorf("email metrics: %w", err)
	}
	out := MetricsResponse{GeneratedAt: now.UTC(), Window: metricsWindowLabel, Tenants: []TenantMetrics{}}
	for _, c := range counts {
		out.Tenants = append(out.Tenants, TenantMetrics{
			TenantID: c.TenantID.String(), SystemCode: c.SystemCode,
			Queued: c.Queued, Sending: c.Sending, Sent: c.Sent, Failed: c.Failed,
		})
	}
	return out, nil
}

// tenantLimiter is a fixed-window counter per tenant, kept in memory (the
// service runs as a single instance).
type tenantLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	windows map[uuid.UUID]*rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

func newTenantLimiter(limit int, window time.Duration, now func() time.Time) *tenantLimiter {
	return &tenantLimiter{limit: limit, window: window, now: now, windows: map[uuid.UUID]*rateWindow{}}
}

func (l *tenantLimiter) allow(tenantID uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.windows[tenantID]
	if !ok || now.Sub(w.start) >= l.window {
		w = &rateWindow{start: now}
		l.windows[tenantID] = w
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}
