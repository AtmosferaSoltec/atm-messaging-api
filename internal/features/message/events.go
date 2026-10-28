package message

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Webhook event types of a message's life.
const (
	EventSent      = "message.sent"
	EventDelivered = "message.delivered"
	EventRead      = "message.read"
	EventFailed    = "message.failed"
)

// Notifier queues a webhook event for a tenant.
type Notifier interface {
	Notify(ctx context.Context, tenantID uuid.UUID, eventType string, data any) error
}

// EventData is the `data` of every message webhook.
type EventData struct {
	MessageID         string     `json:"message_id"`
	IdempotencyKey    string     `json:"idempotency_key,omitempty"`
	SessionID         string     `json:"session_id"`
	Channel           string     `json:"channel"`
	To                string     `json:"to"`
	Type              string     `json:"type"`
	Priority          string     `json:"priority"`
	Status            string     `json:"status"`
	Error             string     `json:"error,omitempty"`
	ProviderMessageID string     `json:"provider_message_id,omitempty"`
	SentAt            *time.Time `json:"sent_at"`
	DeliveredAt       *time.Time `json:"delivered_at"`
	ReadAt            *time.Time `json:"read_at"`
}

func newEventData(m Message) EventData {
	return EventData{
		MessageID: m.ID.String(), IdempotencyKey: m.IdempotencyKey, SessionID: m.SessionID.String(),
		Channel: string(m.Channel), To: m.Recipient, Type: string(m.Kind), Priority: string(m.Priority),
		Status: string(m.Status), Error: m.Error, ProviderMessageID: m.ProviderMessageID,
		SentAt: m.SentAt, DeliveredAt: m.DeliveredAt, ReadAt: m.ReadAt,
	}
}

// notify tells the tenant about a message event. A failure is logged and never
// affects the message itself: webhooks are best effort next to delivery.
func notify(ctx context.Context, notifier Notifier, log *slog.Logger, m Message, eventType string) {
	if notifier == nil {
		return
	}
	if err := notifier.Notify(ctx, m.TenantID, eventType, newEventData(m)); err != nil {
		log.Error("queue webhook", "message_id", m.ID, "event", eventType, "error", err)
	}
}
