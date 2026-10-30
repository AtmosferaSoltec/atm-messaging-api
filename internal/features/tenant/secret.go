package tenant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

const (
	apiKeyPrefix     = "atm_"
	apiKeyBytes      = 32
	apiKeyShownChars = 12
	secretBytes      = 32
)

// generateAPIKey returns a new random key, its display prefix and its hash.
func generateAPIKey() (key, prefix, hash string, err error) {
	buf := make([]byte, apiKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate api key: %w", err)
	}
	key = apiKeyPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return key, key[:apiKeyShownChars], HashAPIKey(key), nil
}

// generateWebhookSecret returns a random secret for signing webhooks.
func generateWebhookSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// HashAPIKey returns the SHA-256 hex digest stored in place of the key. Keys
// carry 256 bits of randomness, so a fast hash is enough and allows lookup by hash.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
