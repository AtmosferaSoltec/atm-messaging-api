// Package tenant manages the systems that consume the API and authenticates
// their API keys.
package tenant

import (
	"time"

	"github.com/google/uuid"
)

// Tenant is one of our systems (e.g. La Vendimia).
type Tenant struct {
	ID            uuid.UUID
	Name          string
	APIKeyPrefix  string
	WebhookSecret string
	WebhookURL    string // empty when the tenant has not configured webhooks
	IsActive      bool
	CreatedAt     time.Time
}

// Created is the result of creating a tenant. APIKey is the only time the key
// exists in clear; afterwards only its hash is stored.
type Created struct {
	Tenant Tenant
	APIKey string
}
