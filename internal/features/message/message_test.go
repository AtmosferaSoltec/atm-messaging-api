package message

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// minimalPDF is enough for content sniffing to report application/pdf.
var minimalPDF = []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n")

func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

func textRequest() SendRequest {
	return SendRequest{AccountRef: "c1", Channel: "whatsapp", To: "51936416623", Type: "text", Body: "hello"}
}

func TestValidateRequest(t *testing.T) {
	base := textRequest()

	v, err := validate(base)
	if err != nil {
		t.Fatalf("valid text request rejected: %v", err)
	}
	if v.priority != PriorityTransactional {
		t.Errorf("priority defaults to transactional, got %s", v.priority)
	}

	withFormatting := base
	withFormatting.To = "+51 936-416-623"
	if v, err := validate(withFormatting); err != nil || v.recipient != "51936416623" {
		t.Errorf("recipient should be normalized, got %q / %v", v.recipient, err)
	}

	cases := map[string]func(*SendRequest){
		"unsupported channel": func(r *SendRequest) { r.Channel = "sms" },
		"invalid recipient":   func(r *SendRequest) { r.To = "abc" },
		"invalid type":        func(r *SendRequest) { r.Type = "video" },
		"invalid priority":    func(r *SendRequest) { r.Priority = "urgent" },
		"empty text":          func(r *SendRequest) { r.Body = "  " },
		"text too long":       func(r *SendRequest) { r.Body = strings.Repeat("x", maxBodyLength+1) },
		"text with file":      func(r *SendRequest) { r.Attachment = &AttachmentRequest{MimeType: "application/pdf"} },
		"document without file": func(r *SendRequest) {
			r.Type, r.Body = "document", ""
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := base
			mutate(&req)
			var validation *ValidationError
			if _, err := validate(req); !errors.As(err, &validation) {
				t.Fatalf("expected a ValidationError, got %v", err)
			}
		})
	}
}

func TestValidateAttachment(t *testing.T) {
	doc := SendRequest{AccountRef: "c1", Channel: "whatsapp", To: "51936416623", Type: "document"}

	doc.Attachment = &AttachmentRequest{Filename: "../../etc/boleta.pdf", MimeType: "application/pdf", ContentBase64: b64(minimalPDF)}
	v, err := validate(doc)
	if err != nil {
		t.Fatalf("valid pdf rejected: %v", err)
	}
	if v.attachment.Filename != "boleta.pdf" {
		t.Errorf("filename must lose its path, got %q", v.attachment.Filename)
	}

	bad := map[string]AttachmentRequest{
		"mime not allowed":     {MimeType: "application/zip", ContentBase64: b64(minimalPDF)},
		"content is not a pdf": {MimeType: "application/pdf", ContentBase64: b64([]byte("just some text"))},
		"invalid base64":       {MimeType: "application/pdf", ContentBase64: "***"},
		"too large":            {MimeType: "application/pdf", ContentBase64: b64(append([]byte("%PDF-"), make([]byte, maxAttachmentSize)...))},
	}
	for name, attachment := range bad {
		t.Run(name, func(t *testing.T) {
			req := doc
			req.Attachment = &attachment
			var validation *ValidationError
			if _, err := validate(req); !errors.As(err, &validation) {
				t.Fatalf("expected a ValidationError, got %v", err)
			}
		})
	}

	image := doc
	image.Type = "image"
	image.Attachment = &AttachmentRequest{MimeType: "application/pdf", ContentBase64: b64(minimalPDF)}
	if _, err := validate(image); err == nil {
		t.Error("a pdf must not be accepted as an image")
	}
}

func TestSendQueuesWithoutDelivering(t *testing.T) {
	h := newHarness(t)
	tenantID := uuid.New()

	msg, err := h.service.Send(context.Background(), tenantID, textRequest())
	if err != nil {
		t.Fatal(err)
	}
	if msg.Status != StatusQueued {
		t.Errorf("an accepted message is queued, got %s", msg.Status)
	}
	if len(h.channel.sent) != 0 {
		t.Error("accepting a message must not deliver it; the dispatcher does")
	}
	if len(h.waker.woken) != 1 || h.waker.woken[0] != h.sessionID {
		t.Error("accepting a message must wake the session's worker")
	}
}

func TestSendStoresAttachmentBytesForTheQueue(t *testing.T) {
	h := newHarness(t)
	req := SendRequest{AccountRef: "c1", Channel: "whatsapp", To: "51936416623", Type: "document",
		Attachment: &AttachmentRequest{Filename: "boleta.pdf", MimeType: "application/pdf", ContentBase64: b64(minimalPDF)}}

	msg, err := h.service.Send(context.Background(), uuid.New(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(h.repo.attachments[msg.ID]) != string(minimalPDF) {
		t.Error("attachment bytes must be stored so retries and restarts can resend them")
	}
}

func TestSendIsIdempotentPerTenant(t *testing.T) {
	h := newHarness(t)
	tenantID := uuid.New()

	req := textRequest()
	req.IdempotencyKey = "invoice-42"
	first, err := h.service.Send(context.Background(), tenantID, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.Send(context.Background(), tenantID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(h.repo.messages) != 1 {
		t.Error("the same idempotency key must return the original message without a second one")
	}
	if len(h.waker.woken) != 1 {
		t.Error("a replayed request must not wake the worker again")
	}

	other, _ := h.service.Send(context.Background(), uuid.New(), req)
	if other.ID == first.ID || len(h.repo.messages) != 2 {
		t.Error("idempotency keys are scoped to the tenant")
	}
}

func TestSendRequiresAConnectedSession(t *testing.T) {
	h := newHarness(t)
	h.sessions.connected = false
	if _, err := h.service.Send(context.Background(), uuid.New(), textRequest()); !errors.Is(err, ErrSessionNotConnected) {
		t.Fatalf("expected ErrSessionNotConnected, got %v", err)
	}

	h.sessions.missing = true
	if _, err := h.service.Send(context.Background(), uuid.New(), textRequest()); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSendRejectsPromotionalToOptedOutRecipients(t *testing.T) {
	h := newHarness(t)
	h.optOuts["51936416623"] = true

	promo := textRequest()
	promo.Priority = "promotional"
	if _, err := h.service.Send(context.Background(), uuid.New(), promo); !errors.Is(err, ErrRecipientOptedOut) {
		t.Fatalf("expected ErrRecipientOptedOut, got %v", err)
	}

	if _, err := h.service.Send(context.Background(), uuid.New(), textRequest()); err != nil {
		t.Fatalf("transactional messages are not affected by opt-outs: %v", err)
	}
}
