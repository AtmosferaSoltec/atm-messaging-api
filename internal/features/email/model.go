// Package email sends transactional email (password reset codes and the like)
// from a persistent queue. It is independent of the WhatsApp session model:
// a message only needs a tenant, a recipient and a template.
package email

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status is the delivery state of an email.
type Status string

// Delivery states.
const (
	StatusQueued  Status = "queued"
	StatusSending Status = "sending"
	StatusSent    Status = "sent"
	StatusFailed  Status = "failed"
)

// Template names (a closed set).
const (
	TemplatePasswordResetCode = "password_reset_code"
	TemplatePasswordChanged   = "password_changed"
)

// Domain errors.
var (
	ErrNotFound    = errors.New("email not found")
	ErrUnavailable = errors.New("email provider is not configured")
	ErrRateLimited = errors.New("email rate limit exceeded")
)

// Data holds the template variables. Which fields are required depends on the template.
type Data struct {
	Name           string `json:"name,omitempty"`
	Code           string `json:"code,omitempty"`
	ExpiresMinutes *int   `json:"expires_minutes,omitempty"`
}

// Email is a stored message.
type Email struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	To             string
	Template       string
	Data           Data
	IdempotencyKey string
	Status         Status
	Attempts       int
	LastError      string
	NextAttemptAt  time.Time
	CreatedAt      time.Time
	SentAt         *time.Time
}

// TenantCounts is the number of emails per status for one tenant.
type TenantCounts struct {
	TenantID   uuid.UUID
	SystemCode string
	Queued     int
	Sending    int
	Sent       int
	Failed     int
}
