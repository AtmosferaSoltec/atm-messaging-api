// Package webhook delivers events to the tenants' systems: signed with
// HMAC-SHA256, stored in an outbox so they survive restarts, and retried with
// backoff. A slow or failing tenant endpoint never delays another tenant's.
package webhook

import (
	"time"

	"github.com/google/uuid"
)

// Status is the delivery state of a webhook event.
type Status string

// Webhook event statuses.
const (
	StatusPending   Status = "pending"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
)

// TestEventType is the event sent by the "send test event" endpoint.
const TestEventType = "webhook.test"

// Event is one webhook to deliver.
type Event struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Type          string
	Payload       []byte // the exact JSON body that is signed and sent
	Status        Status
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	DeliveredAt   *time.Time
}

// Envelope is the JSON body of every webhook.
type Envelope struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Data      any       `json:"data"`
}
