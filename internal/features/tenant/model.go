// Package tenant manages the systems that consume the API. A tenant is created by
// this service the first time a system presents its service token.
package tenant

import (
	"time"

	"github.com/google/uuid"
)

// Tenant is one of our systems (e.g. La Vendimia), identified by its code in ATM-Auth-API.
type Tenant struct {
	ID            uuid.UUID
	Name          string
	SystemCode    string
	WebhookSecret string
	WebhookURL    string // empty when the tenant has not configured webhooks
	IsActive      bool
	CreatedAt     time.Time
}
