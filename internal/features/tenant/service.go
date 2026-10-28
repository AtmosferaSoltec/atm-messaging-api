package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Errors returned by the Service.
var (
	ErrInvalidAPIKey = errors.New("invalid api key")
	ErrInvalidName   = errors.New("tenant name must be between 1 and 100 characters")
)

// Service holds the tenant business rules.
type Service struct {
	repo Repository
}

// NewService creates a Service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Create registers a tenant and returns its API key, which is shown only once.
func (s *Service) Create(ctx context.Context, name string) (Created, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Created{}, ErrInvalidName
	}

	key, prefix, hash, err := generateAPIKey()
	if err != nil {
		return Created{}, err
	}
	secret, err := generateWebhookSecret()
	if err != nil {
		return Created{}, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Created{}, fmt.Errorf("generate tenant id: %w", err)
	}
	t := Tenant{ID: id, Name: name, APIKeyPrefix: prefix, WebhookSecret: secret, IsActive: true}
	if err := s.repo.Create(ctx, t, hash); err != nil {
		return Created{}, err
	}
	return Created{Tenant: t, APIKey: key}, nil
}

// Authenticate resolves the tenant that owns an API key.
func (s *Service) Authenticate(ctx context.Context, apiKey string) (Tenant, error) {
	if apiKey == "" {
		return Tenant{}, ErrInvalidAPIKey
	}
	t, err := s.repo.FindByAPIKeyHash(ctx, HashAPIKey(apiKey))
	if errors.Is(err, ErrNotFound) {
		return Tenant{}, ErrInvalidAPIKey
	}
	if err != nil {
		return Tenant{}, err
	}
	if !t.IsActive {
		return Tenant{}, ErrInvalidAPIKey
	}
	return t, nil
}

// Get returns a tenant by ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Tenant, error) {
	return s.repo.FindByID(ctx, id)
}

// List returns every tenant.
func (s *Service) List(ctx context.Context) ([]Tenant, error) {
	return s.repo.List(ctx)
}

// Update renames a tenant and/or activates or deactivates it. An inactive
// tenant's API key stops authenticating.
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

// RotateAPIKey issues a new API key for the tenant. The old key stops working
// immediately, and the new one is returned only here.
func (s *Service) RotateAPIKey(ctx context.Context, id uuid.UUID) (Created, error) {
	key, prefix, hash, err := generateAPIKey()
	if err != nil {
		return Created{}, err
	}
	if err := s.repo.ReplaceAPIKey(ctx, id, prefix, hash); err != nil {
		return Created{}, err
	}
	t, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return Created{}, err
	}
	return Created{Tenant: t, APIKey: key}, nil
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
