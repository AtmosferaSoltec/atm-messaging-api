package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

const signaturePrefix = "sha256="

// Sign returns the X-Signature header value: HMAC-SHA256 of "<timestamp>.<body>"
// with the tenant's webhook secret. Including the timestamp lets receivers reject
// replayed requests.
func Sign(secret string, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp.Unix(), 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature matches body, using a constant-time comparison.
// It is what a receiving system does; it is exported so it can be documented and tested.
func Verify(secret string, timestamp time.Time, body []byte, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, timestamp, body)), []byte(signature))
}
