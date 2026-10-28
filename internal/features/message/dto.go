package message

import "time"

// AttachmentRequest is a file sent inline, base64-encoded.
type AttachmentRequest struct {
	Filename      string `json:"filename"`
	MimeType      string `json:"mime_type"`
	ContentBase64 string `json:"content_base64"`
}

// SendRequest is the body of POST /v1/messages.
type SendRequest struct {
	AccountRef     string             `json:"account_ref"`
	Channel        string             `json:"channel"`
	To             string             `json:"to"`
	Type           string             `json:"type"`
	Body           string             `json:"body"`
	Priority       string             `json:"priority"`
	IdempotencyKey string             `json:"idempotency_key"`
	Attachment     *AttachmentRequest `json:"attachment"`
}

// SendResponse is returned with 202 once the message is accepted.
type SendResponse struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

// Response is the public representation of a message.
type Response struct {
	ID        string     `json:"id"`
	Channel   string     `json:"channel"`
	To        string     `json:"to"`
	Type      string     `json:"type"`
	Priority  string     `json:"priority"`
	Status    string     `json:"status"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	SentAt    *time.Time `json:"sent_at"`

	DeliveredAt *time.Time `json:"delivered_at"`
	ReadAt      *time.Time `json:"read_at"`
}

// NewResponse maps a Message to its public representation.
func NewResponse(m Message) Response {
	return Response{
		ID:        m.ID.String(),
		Channel:   string(m.Channel),
		To:        m.Recipient,
		Type:      string(m.Kind),
		Priority:  string(m.Priority),
		Status:    string(m.Status),
		Error:     m.Error,
		CreatedAt: m.CreatedAt,
		SentAt:    m.SentAt,

		DeliveredAt: m.DeliveredAt,
		ReadAt:      m.ReadAt,
	}
}
