package message

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

func TestDispatcherSendsAndRecordsTheOutcome(t *testing.T) {
	h := newHarness(t)
	msg := h.enqueue("", "51900000001")

	h.process()

	got := h.repo.get(msg.ID)
	if got.Status != StatusSent || got.ProviderMessageID == "" || got.SentAt == nil || got.Attempts != 1 {
		t.Errorf("expected sent with provider id, time and one attempt, got %+v", got)
	}
	if _, kept := h.repo.attachments[msg.ID]; kept {
		t.Error("attachment bytes must be discarded once the message is settled")
	}
}

func TestDispatcherSendsTransactionalBeforePromotional(t *testing.T) {
	h := newHarness(t)
	promo := h.enqueue("promotional", "51900000001")
	tx := h.enqueue("transactional", "51900000002")

	h.process()

	if h.repo.get(tx.ID).Status != StatusSent || h.repo.get(promo.ID).Status != StatusQueued {
		t.Error("a transactional message must go before an older promotional one")
	}
}

func TestDispatcherPacesTransactionalMessages(t *testing.T) {
	h := newHarness(t)
	h.enqueue("", "51900000001")
	second := h.enqueue("", "51900000002")

	h.process()
	delay, _ := h.process()
	if delay != time.Second || h.repo.get(second.ID).Status != StatusQueued {
		t.Fatalf("second message must wait the transactional interval, got delay %v / %s", delay, h.repo.get(second.ID).Status)
	}

	h.advance(time.Second)
	h.process()
	if h.repo.get(second.ID).Status != StatusSent {
		t.Error("second message should go once the interval passed")
	}
}

func TestDispatcherDelaysPromotionalMessagesRandomly(t *testing.T) {
	h := newHarness(t)
	h.random = 0.5 // 8s + 0.5*12s = 14s
	h.enqueue("promotional", "51900000001")
	second := h.enqueue("promotional", "51900000002")

	h.process()
	delay, _ := h.process()
	if delay != 14*time.Second {
		t.Fatalf("promotional delay should be 14s, got %v", delay)
	}

	// A wake-up must not re-roll the delay: it was chosen once for this message.
	h.advance(5 * time.Second)
	h.random = 0.99
	delay, _ = h.process()
	if delay != 9*time.Second {
		t.Fatalf("the remaining wait should be 9s, got %v", delay)
	}

	h.advance(9 * time.Second)
	h.process()
	if h.repo.get(second.ID).Status != StatusSent {
		t.Error("second promotional message should go after its delay")
	}
}

func TestDispatcherStopsAtTheDailyCap(t *testing.T) {
	h := newHarness(t)
	h.dispatcher.cfg.Policy.TransactionalDailyCap = 2
	h.dispatcher.cfg.MaxAge = 48 * time.Hour // the capped message must wait for tomorrow, not expire
	for i := 0; i < 3; i++ {
		h.enqueue("", "5190000000"+string(rune('1'+i)))
	}

	h.process()
	h.advance(time.Second)
	h.process()
	h.advance(time.Second)
	delay, _ := h.process()

	if len(h.channel.sent) != 2 {
		t.Fatalf("only 2 messages fit in the daily cap, %d were sent", len(h.channel.sent))
	}
	if delay < time.Minute || delay > time.Hour {
		t.Errorf("a capped session should sleep (bounded to an hour), got %v", delay)
	}

	h.clock = h.dispatcher.cfg.Policy.NextDayStart(h.clock).Add(time.Minute)
	h.process()
	if len(h.channel.sent) != 3 {
		t.Error("the third message should go the next day")
	}
}

func TestDispatcherAppliesWarmupToNewNumbers(t *testing.T) {
	h := newHarness(t)
	h.sessions.firstConn = h.clock // connected just now: day one of warm-up
	h.dispatcher.cfg.Policy.PromotionalMinDelay = 0
	h.dispatcher.cfg.Policy.PromotionalMaxDelay = 0
	h.dispatcher.cfg.Policy.PromotionalDailyCap = 7
	h.dispatcher.cfg.Policy.WarmupDays = 7 // day one allows ceil(7*1/7) = 1

	h.enqueue("promotional", "51900000001")
	h.enqueue("promotional", "51900000002")
	h.process()
	h.process()
	if len(h.channel.sent) != 1 {
		t.Fatalf("a new number may send only 1 promotional message on day one, sent %d", len(h.channel.sent))
	}

	h.sessions.firstConn = h.clock.Add(-25 * time.Hour) // day two allows 2
	h.process()
	if len(h.channel.sent) != 2 {
		t.Errorf("day two should allow a second message, sent %d", len(h.channel.sent))
	}
}

func TestDispatcherRetriesWithBackoffThenFails(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("provider error")
	h.channel.results = []error{boom, boom, boom}
	msg := h.enqueue("", "51900000001")

	h.process() // attempt 1 fails, retry in 30s
	if got := h.repo.get(msg.ID); got.Status != StatusQueued || got.Attempts != 1 {
		t.Fatalf("after the first failure: %+v", got)
	}
	if due := h.repo.dueAt[msg.ID].Sub(h.clock); due != 30*time.Second {
		t.Errorf("first retry in 30s, got %v", due)
	}

	delay, _ := h.process()
	if delay <= 0 || len(h.channel.sent) != 1 {
		t.Error("the message must not be retried before it is due")
	}

	h.advance(30 * time.Second)
	h.process() // attempt 2 fails, retry in 60s
	if due := h.repo.dueAt[msg.ID].Sub(h.clock); due != 60*time.Second {
		t.Errorf("second retry in 60s (doubled), got %v", due)
	}

	h.advance(60 * time.Second)
	h.process() // attempt 3 fails: give up
	got := h.repo.get(msg.ID)
	if got.Status != StatusFailed || got.Error != FailureSendFailed || got.Attempts != 3 {
		t.Errorf("after the last attempt expected failed/send_failed/3, got %+v", got)
	}
}

func TestDispatcherDoesNotRetryUnreachableRecipients(t *testing.T) {
	h := newHarness(t)
	h.channel.results = []error{channel.ErrRecipientNotFound}
	msg := h.enqueue("", "51900000001")

	h.process()

	if got := h.repo.get(msg.ID); got.Status != StatusFailed || got.Error != FailureRecipientNotFound {
		t.Errorf("expected failed/%s, got %s/%s", FailureRecipientNotFound, got.Status, got.Error)
	}
}

func TestDispatcherWaitsForAnOfflineSessionWithoutBurningAttempts(t *testing.T) {
	h := newHarness(t)
	msg := h.enqueue("", "51900000001")

	h.sessions.connected = false
	delay, _ := h.process()
	if delay != 30*time.Second || len(h.channel.sent) != 0 {
		t.Fatalf("an offline session waits without sending, got delay %v / %d sends", delay, len(h.channel.sent))
	}

	// The channel itself reports the connection dropped mid-send.
	h.sessions.connected = true
	h.channel.results = []error{channel.ErrSessionNotConnected}
	h.process()
	if got := h.repo.get(msg.ID); got.Status != StatusQueued || got.Attempts != 0 {
		t.Errorf("being offline must not consume attempts, got %+v", got)
	}

	h.advance(30 * time.Second)
	h.process()
	if h.repo.get(msg.ID).Status != StatusSent {
		t.Error("the message should go once the session is back")
	}
}

func TestDispatcherExpiresOldMessages(t *testing.T) {
	h := newHarness(t)
	msg := h.enqueue("", "51900000001")
	h.sessions.connected = false

	h.advance(2 * time.Hour)
	h.process()

	if got := h.repo.get(msg.ID); got.Status != StatusFailed || got.Error != FailureExpired {
		t.Errorf("expected failed/%s, got %s/%s", FailureExpired, got.Status, got.Error)
	}
	if len(h.channel.sent) != 0 {
		t.Error("an expired message must not be sent")
	}
}

func TestDispatcherSkipsPromotionalMessagesToRecipientsWhoOptedOutWhileQueued(t *testing.T) {
	h := newHarness(t)
	msg := h.enqueue("promotional", "51900000001")
	h.optOuts["51900000001"] = true // unsubscribed after the message was accepted

	h.process()

	if got := h.repo.get(msg.ID); got.Status != StatusFailed || got.Error != FailureRecipientOptedOut {
		t.Errorf("expected failed/%s, got %s/%s", FailureRecipientOptedOut, got.Status, got.Error)
	}
	if len(h.channel.sent) != 0 {
		t.Error("nothing may be sent to an opted-out recipient")
	}
}

func TestDispatcherSendsTheStoredAttachment(t *testing.T) {
	h := newHarness(t)
	req := SendRequest{AccountRef: "c1", Channel: "whatsapp", To: "51900000001", Type: "document", Body: "Your invoice",
		Attachment: &AttachmentRequest{Filename: "boleta.pdf", MimeType: "application/pdf", ContentBase64: b64(minimalPDF)}}
	if _, err := h.service.Send(t.Context(), [16]byte{1}, req); err != nil {
		t.Fatal(err)
	}

	h.process()

	if len(h.channel.sent) != 1 {
		t.Fatalf("expected one send, got %d", len(h.channel.sent))
	}
	sent := h.channel.sent[0]
	if sent.Attachment == nil || string(sent.Attachment.Data) != string(minimalPDF) || sent.Attachment.Filename != "boleta.pdf" || sent.Body != "Your invoice" {
		t.Errorf("the queued attachment and caption must reach the channel, got %+v", sent)
	}
}

func TestDispatcherStopsWhenTheSessionIsGone(t *testing.T) {
	h := newHarness(t)
	h.sessions.missing = true

	if _, stop := h.process(); !stop {
		t.Error("the worker of a deleted session must stop")
	}
}

func TestDispatcherIdlesWhenThereIsNothingToSend(t *testing.T) {
	h := newHarness(t)
	if delay, stop := h.process(); stop || delay != idleDelay {
		t.Errorf("an empty queue idles, got delay %v stop %v", delay, stop)
	}
}

func TestDispatcherNotifiesTheTenantOfSentAndFailedMessages(t *testing.T) {
	h := newHarness(t)
	sent := h.enqueue("", "51900000001")
	h.process()

	h.channel.results = []error{channel.ErrRecipientNotFound}
	h.advance(time.Second)
	failed := h.enqueue("", "51900000002")
	h.process()

	if len(h.notifier.events) != 2 {
		t.Fatalf("expected 2 webhook events, got %d", len(h.notifier.events))
	}
	first, second := h.notifier.events[0], h.notifier.events[1]
	if first.eventType != EventSent || first.data.MessageID != sent.ID.String() || first.data.Status != "sent" || first.data.ProviderMessageID == "" {
		t.Errorf("unexpected sent event: %+v", first)
	}
	if second.eventType != EventFailed || second.data.MessageID != failed.ID.String() || second.data.Error != FailureRecipientNotFound {
		t.Errorf("unexpected failed event: %+v", second)
	}
	if first.tenantID != h.repo.get(sent.ID).TenantID {
		t.Error("the event must go to the message's tenant")
	}
}

func TestDispatcherDoesNotNotifyWhileRetrying(t *testing.T) {
	h := newHarness(t)
	h.channel.results = []error{errors.New("transient")}
	h.enqueue("", "51900000001")

	h.process()

	if len(h.notifier.events) != 0 {
		t.Error("a message that will be retried has not failed yet; no webhook")
	}
}

func TestReceiptsAdvanceMessagesAndNotify(t *testing.T) {
	h := newHarness(t)
	msg := h.enqueue("", "51900000001")
	h.process()
	providerID := h.repo.get(msg.ID).ProviderMessageID
	h.notifier.events = nil

	h.service.HandleEvent(channel.Event{SessionID: h.sessionID, Kind: channel.EventMessageDelivered, ProviderMessageIDs: []string{providerID}})
	if got := h.repo.get(msg.ID); got.Status != StatusDelivered || got.DeliveredAt == nil {
		t.Fatalf("expected delivered, got %+v", got)
	}

	h.service.HandleEvent(channel.Event{SessionID: h.sessionID, Kind: channel.EventMessageRead, ProviderMessageIDs: []string{providerID}})
	if got := h.repo.get(msg.ID); got.Status != StatusRead || got.ReadAt == nil {
		t.Fatalf("expected read, got %+v", got)
	}

	// A late delivery receipt must not move the message backwards or re-notify.
	h.service.HandleEvent(channel.Event{SessionID: h.sessionID, Kind: channel.EventMessageDelivered, ProviderMessageIDs: []string{providerID}})
	if got := h.repo.get(msg.ID); got.Status != StatusRead {
		t.Errorf("a message never goes back from read, got %s", got.Status)
	}

	types := []string{}
	for _, e := range h.notifier.events {
		types = append(types, e.eventType)
	}
	if len(types) != 2 || types[0] != EventDelivered || types[1] != EventRead {
		t.Errorf("expected delivered then read, got %v", types)
	}
}

func TestReceiptsIgnoreUnknownMessagesAndOtherSessions(t *testing.T) {
	h := newHarness(t)
	msg := h.enqueue("", "51900000001")
	h.process()
	providerID := h.repo.get(msg.ID).ProviderMessageID
	h.notifier.events = nil

	h.service.HandleEvent(channel.Event{SessionID: h.sessionID, Kind: channel.EventMessageRead, ProviderMessageIDs: []string{"unknown"}})
	h.service.HandleEvent(channel.Event{SessionID: uuid.New(), Kind: channel.EventMessageRead, ProviderMessageIDs: []string{providerID}})
	h.service.HandleEvent(channel.Event{SessionID: h.sessionID, Kind: channel.EventQR})

	if h.repo.get(msg.ID).Status != StatusSent || len(h.notifier.events) != 0 {
		t.Error("only receipts of this session's known messages may change anything")
	}
}
