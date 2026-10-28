// Package whatsapp implements the WhatsApp channel on top of whatsmeow. It is
// the only package allowed to import whatsmeow.
package whatsapp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

const pairingTimeout = 4 * time.Minute

// Manager keeps one whatsmeow client per session, so a failing or slow session
// never affects the others. It implements channel.Channel.
type Manager struct {
	container *sqlstore.Container
	handler   channel.EventHandler
	log       *slog.Logger
	waLogger  waLog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.RWMutex
	clients map[uuid.UUID]*whatsmeow.Client
}

var _ channel.Channel = (*Manager)(nil)

// NewManager opens the whatsmeow store in the same PostgreSQL schema as the
// application and returns a Manager. handler receives every session event.
func NewManager(ctx context.Context, databaseURL string, handler channel.EventHandler, log *slog.Logger) (*Manager, error) {
	waLogger := waLog.Stdout("whatsmeow", "WARN", false)
	container, err := sqlstore.New(ctx, "pgx", databaseURL, waLogger)
	if err != nil {
		// Not wrapped: the error text could include the connection string.
		return nil, fmt.Errorf("open whatsmeow store: connection failed")
	}

	managerCtx, cancel := context.WithCancel(context.Background())
	return &Manager{
		container: container,
		handler:   handler,
		log:       log,
		waLogger:  waLogger,
		ctx:       managerCtx,
		cancel:    cancel,
		clients:   make(map[uuid.UUID]*whatsmeow.Client),
	}, nil
}

// Type reports the channel this manager implements.
func (m *Manager) Type() channel.Type { return channel.WhatsApp }

// StartPairing links a new device. QR codes are emitted as events; the pairing
// lives on the manager's context, not the request's, so it outlives the HTTP call.
func (m *Manager) StartPairing(_ context.Context, sessionID uuid.UUID) error {
	m.mu.Lock()
	if _, exists := m.clients[sessionID]; exists {
		m.mu.Unlock()
		return channel.ErrPairingInProgress
	}
	client := whatsmeow.NewClient(m.container.NewDevice(), m.waLogger)
	m.clients[sessionID] = client
	m.mu.Unlock()

	client.AddEventHandler(m.eventHandler(sessionID, client))

	pairCtx, cancel := context.WithTimeout(m.ctx, pairingTimeout)
	qrChan, err := client.GetQRChannel(pairCtx)
	if err != nil {
		cancel()
		m.drop(sessionID, client)
		return fmt.Errorf("get qr channel: %w", err)
	}
	if err := client.ConnectContext(m.ctx); err != nil {
		cancel()
		m.drop(sessionID, client)
		return fmt.Errorf("connect for pairing: %w", err)
	}

	go m.readQRCodes(pairCtx, cancel, sessionID, client, qrChan)
	return nil
}

func (m *Manager) readQRCodes(_ context.Context, cancel context.CancelFunc, sessionID uuid.UUID, client *whatsmeow.Client, qrChan <-chan whatsmeow.QRChannelItem) {
	defer cancel()
	for item := range qrChan {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventQR, Code: item.Code})
		case whatsmeow.QRChannelSuccess.Event:
			return // PairSuccess/Connected arrive through the client event handler
		case whatsmeow.QRChannelEventError:
			m.failPairing(sessionID, client, fmt.Sprintf("pairing error: %v", item.Error))
			return
		default:
			m.failPairing(sessionID, client, "pairing ended: "+item.Event)
			return
		}
	}
	m.failPairing(sessionID, client, "pairing ended without a scan")
}

func (m *Manager) failPairing(sessionID uuid.UUID, client *whatsmeow.Client, detail string) {
	m.drop(sessionID, client)
	m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventPairingFailed, Detail: detail})
}

// Restore reconnects a paired session from its stored credentials.
func (m *Manager) Restore(ctx context.Context, sessionID uuid.UUID, channelRef string) error {
	m.mu.Lock()
	if _, exists := m.clients[sessionID]; exists {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	jid, err := types.ParseJID(channelRef)
	if err != nil {
		return fmt.Errorf("parse stored device id: %w", err)
	}
	device, err := m.container.GetDevice(ctx, jid)
	if err != nil {
		return fmt.Errorf("load device: %w", err)
	}
	if device == nil {
		return channel.ErrCredentialsMissing
	}

	client := whatsmeow.NewClient(device, m.waLogger)
	client.InitialAutoReconnect = true

	m.mu.Lock()
	if _, exists := m.clients[sessionID]; exists {
		m.mu.Unlock()
		return nil
	}
	m.clients[sessionID] = client
	m.mu.Unlock()

	client.AddEventHandler(m.eventHandler(sessionID, client))
	if err := client.ConnectContext(m.ctx); err != nil {
		m.drop(sessionID, client)
		return fmt.Errorf("connect: %w", err)
	}
	return nil
}

// Logout unlinks the device from the phone and deletes its credentials.
func (m *Manager) Logout(ctx context.Context, sessionID uuid.UUID, channelRef string) error {
	m.mu.Lock()
	client := m.clients[sessionID]
	delete(m.clients, sessionID)
	m.mu.Unlock()

	if client != nil {
		if client.Store.ID != nil {
			if err := client.Logout(ctx); err != nil {
				// The phone may be unreachable; still remove local credentials.
				m.log.Warn("whatsapp logout failed, deleting credentials locally", "session_id", sessionID, "error", err)
				_ = client.Store.Delete(ctx)
			}
		}
		client.Disconnect()
		return nil
	}

	if channelRef == "" {
		return nil
	}
	jid, err := types.ParseJID(channelRef)
	if err != nil {
		return fmt.Errorf("parse stored device id: %w", err)
	}
	device, err := m.container.GetDevice(ctx, jid)
	if err != nil {
		return fmt.Errorf("load device: %w", err)
	}
	if device != nil {
		if err := device.Delete(ctx); err != nil {
			return fmt.Errorf("delete device credentials: %w", err)
		}
	}
	return nil
}

// Close disconnects all sessions and releases the store. Credentials are kept.
func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, client := range m.clients {
		client.Disconnect()
		delete(m.clients, id)
	}
	_ = m.container.Close()
}

func (m *Manager) client(sessionID uuid.UUID) (*whatsmeow.Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, ok := m.clients[sessionID]
	return client, ok
}

// drop disconnects a client and removes it, only if it is still the registered one.
func (m *Manager) drop(sessionID uuid.UUID, client *whatsmeow.Client) {
	m.mu.Lock()
	if m.clients[sessionID] == client {
		delete(m.clients, sessionID)
	}
	m.mu.Unlock()
	client.Disconnect()
}

func (m *Manager) emit(evt channel.Event) {
	if m.handler != nil {
		m.handler(evt)
	}
}

// eventHandler translates whatsmeow events into channel events for one session.
func (m *Manager) eventHandler(sessionID uuid.UUID, client *whatsmeow.Client) whatsmeow.EventHandler {
	return func(raw any) {
		switch evt := raw.(type) {
		case *events.PairSuccess:
			m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventPaired, ChannelRef: evt.ID.String()})
		case *events.Connected:
			ref := ""
			if client.Store.ID != nil {
				ref = client.Store.ID.String()
			}
			m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventConnected, ChannelRef: ref})
		case *events.Disconnected:
			m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventDisconnected})
		case *events.LoggedOut:
			m.drop(sessionID, client)
			m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventLoggedOut, Detail: evt.Reason.String()})
		case *events.TemporaryBan:
			m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventBanned, Detail: evt.String()})
		case *events.ConnectFailure:
			m.handleConnectFailure(sessionID, client, evt)
		case *events.Receipt:
			m.handleReceipt(sessionID, evt)
		}
	}
}

func (m *Manager) handleConnectFailure(sessionID uuid.UUID, client *whatsmeow.Client, evt *events.ConnectFailure) {
	switch evt.Reason {
	case events.ConnectFailureLoggedOut:
		m.drop(sessionID, client)
		m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventLoggedOut, Detail: evt.Reason.String()})
	case events.ConnectFailureTempBanned, events.ConnectFailureUnknownLogout:
		m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventBanned, Detail: evt.Reason.String()})
	default:
		m.log.Warn("whatsapp connect failure", "session_id", sessionID, "reason", evt.Reason.String())
		m.emit(channel.Event{SessionID: sessionID, Kind: channel.EventDisconnected, Detail: evt.Reason.String()})
	}
}

// handleReceipt reports delivery and read receipts of the messages we sent.
func (m *Manager) handleReceipt(sessionID uuid.UUID, evt *events.Receipt) {
	if evt.IsFromMe || len(evt.MessageIDs) == 0 {
		return // our own other devices, not the recipient
	}
	var kind channel.EventKind
	switch evt.Type {
	case types.ReceiptTypeDelivered:
		kind = channel.EventMessageDelivered
	case types.ReceiptTypeRead, types.ReceiptTypePlayed:
		kind = channel.EventMessageRead
	default:
		return
	}
	m.emit(channel.Event{SessionID: sessionID, Kind: kind, ProviderMessageIDs: evt.MessageIDs})
}
