package email

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

// Message is one email handed to a provider.
type Message struct {
	// Template is only for logs; it is not sent to the provider.
	Template string
	To       string
	Subject  string
	HTML     string
	Text     string
}

// Sender delivers an email through a provider. An error that wraps
// ErrPermanent will never succeed on retry; any other error is retryable.
type Sender interface {
	Send(ctx context.Context, message Message) error
}

// ErrPermanent marks a delivery failure that must not be retried.
var ErrPermanent = errors.New("permanent delivery failure")

// LogSender logs template and masked recipient only. For development.
type LogSender struct {
	Log *slog.Logger
}

// Send logs that an email would have been sent. It never logs the body or the code.
func (l LogSender) Send(_ context.Context, message Message) error {
	l.Log.Info("email not sent (log sender, development)",
		"template", message.Template, "to", MaskAddress(message.To))
	return nil
}

// MaskAddress hides most of an address for logs: "ana@example.com" -> "a***@example.com".
func MaskAddress(address string) string {
	local, domain, found := strings.Cut(address, "@")
	if !found || local == "" {
		return "***"
	}
	return local[:1] + "***@" + domain
}
