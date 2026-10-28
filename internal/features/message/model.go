// Package message validates, stores and delivers outbound messages.
package message

import (
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

// Priority decides which limits apply to a message.
type Priority string

// Message priorities.
const (
	PriorityTransactional Priority = "transactional"
	PriorityPromotional   Priority = "promotional"
)

// Status is the delivery state of a message.
type Status string

// Message statuses.
const (
	StatusQueued    Status = "queued"
	StatusSending   Status = "sending"
	StatusSent      Status = "sent"
	StatusDelivered Status = "delivered"
	StatusRead      Status = "read"
	StatusFailed    Status = "failed"
)

// Failure codes stored in Message.Error. They are stable identifiers, never raw
// provider errors, which could leak internals.
const (
	FailureSessionNotConnected = "session_not_connected"
	FailureRecipientNotFound   = "recipient_not_on_channel"
	FailureSendFailed          = "send_failed"
	FailureRecipientOptedOut   = "recipient_opted_out"
	FailureExpired             = "expired"
)

// Message is an outbound message. Attachment bytes are not stored, only their metadata.
type Message struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	SessionID          uuid.UUID
	Channel            channel.Type
	Recipient          string
	Kind               channel.MessageKind
	Body               string
	AttachmentFilename string
	AttachmentMime     string
	AttachmentSize     int64
	Priority           Priority
	IdempotencyKey     string
	Status             Status
	Attempts           int
	Error              string
	ProviderMessageID  string
	SentAt             *time.Time
	DeliveredAt        *time.Time
	ReadAt             *time.Time
	CreatedAt          time.Time
}
