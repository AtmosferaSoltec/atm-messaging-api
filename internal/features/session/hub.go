package session

import (
	"sync"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

const subscriberBuffer = 16

// hub fans session events out to the SSE streams watching them and remembers
// the latest QR code, so a client that connects late still receives one.
type hub struct {
	mu          sync.Mutex
	subscribers map[uuid.UUID]map[chan channel.Event]struct{}
	latestQR    map[uuid.UUID]channel.Event
}

func newHub() *hub {
	return &hub{
		subscribers: make(map[uuid.UUID]map[chan channel.Event]struct{}),
		latestQR:    make(map[uuid.UUID]channel.Event),
	}
}

// subscribe registers a listener for a session. The latest QR, if any, is
// delivered first. Call the returned function to unsubscribe.
func (h *hub) subscribe(sessionID uuid.UUID) (<-chan channel.Event, func()) {
	ch := make(chan channel.Event, subscriberBuffer)

	h.mu.Lock()
	if h.subscribers[sessionID] == nil {
		h.subscribers[sessionID] = make(map[chan channel.Event]struct{})
	}
	h.subscribers[sessionID][ch] = struct{}{}
	if qr, ok := h.latestQR[sessionID]; ok {
		ch <- qr
	}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subscribers[sessionID], ch)
		if len(h.subscribers[sessionID]) == 0 {
			delete(h.subscribers, sessionID)
		}
	}
}

// publish delivers an event to every listener of its session without blocking
// on slow ones.
func (h *hub) publish(evt channel.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch evt.Kind {
	case channel.EventQR:
		h.latestQR[evt.SessionID] = evt
	case channel.EventConnected, channel.EventPairingFailed, channel.EventLoggedOut, channel.EventBanned:
		delete(h.latestQR, evt.SessionID)
	}

	for ch := range h.subscribers[evt.SessionID] {
		select {
		case ch <- evt:
		default:
		}
	}
}

// forget drops the state of a deleted session.
func (h *hub) forget(sessionID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.latestQR, sessionID)
}
