// Package session manages the channel connections of accounts. An account has
// at most one session per channel.
package session

import (
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

// Status is the connection state of a session.
type Status string

// Session statuses.
const (
	StatusPendingQR    Status = "pending_qr"
	StatusConnected    Status = "connected"
	StatusDisconnected Status = "disconnected"
	StatusBanned       Status = "banned"
)

// Session is the connection of one account to one channel (e.g. its WhatsApp number).
type Session struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	AccountID   uuid.UUID
	AccountRef  string
	Channel     channel.Type
	Status      Status
	ChannelRef  string
	ConnectedAt *time.Time
	// FirstConnectedAt starts the warm-up of a new number; it never changes.
	FirstConnectedAt *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
