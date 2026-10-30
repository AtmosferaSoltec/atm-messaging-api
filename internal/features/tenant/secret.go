package tenant

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const secretBytes = 32

// generateWebhookSecret returns a random secret for signing webhooks.
func generateWebhookSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
