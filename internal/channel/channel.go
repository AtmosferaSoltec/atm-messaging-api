// Package channel defines the provider-agnostic contract every messaging
// channel (WhatsApp today; email and SMS later) implements. Nothing outside a
// provider's own package may import its SDK.
package channel

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Type identifies a channel.
type Type string

// Supported channels.
const (
	WhatsApp Type = "whatsapp"
)

// MessageKind is the content type of an outbound message.
type MessageKind string

// Message kinds.
const (
	KindText     MessageKind = "text"
	KindImage    MessageKind = "image"
	KindVideo    MessageKind = "video"
	KindAudio    MessageKind = "audio"
	KindDocument MessageKind = "document"
)

// Attachment is a file sent with a message.
type Attachment struct {
	Filename string
	MimeType string
	Data     []byte
}

// OutboundMessage is a message to deliver through a channel.
type OutboundMessage struct {
	To         string // digits only, international format
	Kind       MessageKind
	Body       string // text, or caption when there is an attachment
	Attachment *Attachment
}

// SendResult is what the provider returns after accepting a message.
type SendResult struct {
	ProviderMessageID string
}

// EventKind is the type of a session event.
type EventKind string

// Session events emitted by a channel.
const (
	EventQR            EventKind = "qr"             // a new QR code to scan (Code)
	EventPaired        EventKind = "paired"         // the phone scanned the QR (ChannelRef)
	EventConnected     EventKind = "connected"      // the session is online (ChannelRef)
	EventDisconnected  EventKind = "disconnected"   // the connection dropped; it may recover
	EventLoggedOut     EventKind = "logged_out"     // credentials revoked; pairing is needed again
	EventBanned        EventKind = "banned"         // the provider blocked the number
	EventPairingFailed EventKind = "pairing_failed" // the QR expired or pairing errored (Detail)

	EventMessageDelivered EventKind = "message_delivered" // the recipient's device received messages (ProviderMessageIDs)
	EventMessageRead      EventKind = "message_read"      // the recipient read messages (ProviderMessageIDs)
)

// Event is something that happened to a session.
type Event struct {
	SessionID  uuid.UUID
	Kind       EventKind
	Code       string
	ChannelRef string
	Detail     string
	// ProviderMessageIDs are the provider's IDs of the messages a receipt refers to.
	ProviderMessageIDs []string
}

// EventHandler receives the events of every session of a channel.
type EventHandler func(Event)

// Errors returned by channels.
var (
	ErrPairingInProgress   = errors.New("pairing already in progress for this session")
	ErrSessionNotConnected = errors.New("session is not connected")
	ErrRecipientNotFound   = errors.New("recipient is not reachable on this channel")
	ErrCredentialsMissing  = errors.New("stored credentials not found for this session")
)

// Channel is one messaging provider managing one connection per session.
type Channel interface {
	Type() Type
	// StartPairing begins linking a new device. QR codes arrive as events.
	StartPairing(ctx context.Context, sessionID uuid.UUID) error
	// Restore reconnects a previously paired session using its stored credentials.
	Restore(ctx context.Context, sessionID uuid.UUID, channelRef string) error
	// Send delivers a message through the session's connection.
	Send(ctx context.Context, sessionID uuid.UUID, msg OutboundMessage) (SendResult, error)
	// Logout unlinks the device and deletes its credentials. channelRef may be empty.
	Logout(ctx context.Context, sessionID uuid.UUID, channelRef string) error
	// Close disconnects every session without deleting credentials.
	Close()
}
