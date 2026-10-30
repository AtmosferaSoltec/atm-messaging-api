package tenant

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Errors returned by the Service.
var (
	ErrInvalidName = errors.New("tenant name must be between 1 and 100 characters")
	// ErrInvalidSystemCode: the code is lowercase letters, digits, '-' or '_', starting with a letter.
	ErrInvalidSystemCode = errors.New("system code must be lowercase letters, digits, '-' or '_', starting with a letter (max 100)")
)

// Service holds the tenant business rules.
type Service struct {
	repo Repository
}

// NewService creates a Service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

var systemCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Provision returns the tenant of a system, creating it (active, named after the
// system code) the first time the system is seen. Every system registered in
// ATM-Auth-API may use this service, so nothing has to be set up beforehand.
func (s *Service) Provision(ctx context.Context, systemCode string) (Tenant, error) {
	if len(systemCode) > 100 || !systemCodePattern.MatchString(systemCode) {
		return Tenant{}, ErrInvalidSystemCode
	}
	t, err := s.repo.FindBySystemCode(ctx, systemCode)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Tenant{}, err
	}

	secret, err := generateWebhookSecret()
	if err != nil {
		return Tenant{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Tenant{}, fmt.Errorf("generate tenant id: %w", err)
	}
	if _, err := s.repo.Create(ctx, Tenant{ID: id, Name: systemCode, SystemCode: systemCode, WebhookSecret: secret, IsActive: true}); err != nil {
		return Tenant{}, err
	}
	// Another request may have created it first; either way the stored one wins.
	return s.repo.FindBySystemCode(ctx, systemCode)
}

// Authenticate returns the active tenant of the system a service token was issued to.
func (s *Service) Authenticate(ctx context.Context, systemCode string) (Tenant, error) {
	t, err := s.Provision(ctx, systemCode)
	if errors.Is(err, ErrInvalidSystemCode) {
		return Tenant{}, ErrInvalidCredentials
	}
	if err != nil {
		return Tenant{}, err
	}
	if !t.IsActive {
		return Tenant{}, ErrInvalidCredentials
	}
	return t, nil
}

// ErrInvalidCredentials: the service token is not valid, or its tenant is inactive.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Get returns a tenant by ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Tenant, error) {
	return s.repo.FindByID(ctx, id)
}

// List returns every tenant.
func (s *Service) List(ctx context.Context) ([]Tenant, error) {
	return s.repo.List(ctx)
}

// Update renames a tenant and/or activates or deactivates it. An inactive
// tenant's service token stops being accepted.
func (s *Service) Update(ctx context.Context, id uuid.UUID, name *string, isActive *bool) (Tenant, error) {
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" || len(trimmed) > 100 {
			return Tenant{}, ErrInvalidName
		}
		name = &trimmed
	}
	return s.repo.Update(ctx, id, name, isActive)
}

// WebhookEndpoint returns where and with which secret a tenant's webhooks are
// delivered. The URL is empty when the tenant has not configured one.
func (s *Service) WebhookEndpoint(ctx context.Context, tenantID uuid.UUID) (url, secret string, err error) {
	t, err := s.repo.FindByID(ctx, tenantID)
	if err != nil {
		return "", "", err
	}
	return t.WebhookURL, t.WebhookSecret, nil
}

// SetWebhookURL stores the tenant's webhook URL; an empty URL turns webhooks off.
func (s *Service) SetWebhookURL(ctx context.Context, tenantID uuid.UUID, url string) error {
	return s.repo.UpdateWebhookURL(ctx, tenantID, url)
}
