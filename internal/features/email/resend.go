package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	resendEndpoint = "https://api.resend.com/emails"
	resendTimeout  = 10 * time.Second
)

// ResendSender delivers email with the Resend HTTP API.
type ResendSender struct {
	apiKey   string
	from     string
	endpoint string
	client   *http.Client
}

// NewResendSender creates a ResendSender. from is MAIL_FROM, an address of a
// domain verified in Resend.
func NewResendSender(apiKey, from string) *ResendSender {
	return &ResendSender{apiKey: apiKey, from: from, endpoint: resendEndpoint, client: &http.Client{Timeout: resendTimeout}}
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

// Send posts the message. 429, 5xx and network errors are retryable; other
// non-2xx answers are permanent. Errors never include the key or the content.
func (r *ResendSender) Send(ctx context.Context, message Message) error {
	body, err := json.Marshal(resendRequest{
		From: r.from, To: []string{message.To}, Subject: message.Subject, HTML: message.HTML, Text: message.Text,
	})
	if err != nil {
		return fmt.Errorf("encode resend request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build resend request: %w", ErrPermanent)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("resend request failed (network)")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("resend status %d", resp.StatusCode)
	default:
		return fmt.Errorf("resend status %d: %w", resp.StatusCode, ErrPermanent)
	}
}
