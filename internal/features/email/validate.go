package email

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxAddressLength    = 254
	maxIdempotencyKey   = 128
	maxNameLength       = 100
	minExpiresMinutes   = 1
	maxExpiresMinutes   = 60
	codeLength          = 6
	errUnknownTemplate  = "unknown_template"
	errInvalidData      = "invalid_data"
	errInvalidRecipient = "invalid_recipient"
)

var codePattern = regexp.MustCompile(`^[0-9]{6}$`)

// ValidationError is a rejected request; Code is the machine-readable API code.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

func invalid(code, format string, args ...any) *ValidationError {
	return &ValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// NormalizeAddress checks that value is one plain email address and returns it
// lowercased. Display names, lists and control characters are rejected.
func NormalizeAddress(value string) (string, error) {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", invalid(errInvalidRecipient, "to must be a single plain email address")
		}
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxAddressLength {
		return "", invalid(errInvalidRecipient, "to must be a valid email address of at most %d characters", maxAddressLength)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || r == ',' || r == ';' || r == '<' || r == '>' || r == '"' || r == ' ' {
			return "", invalid(errInvalidRecipient, "to must be a single plain email address")
		}
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || parsed.Name != "" {
		return "", invalid(errInvalidRecipient, "to must be a valid email address")
	}
	local, domain, _ := strings.Cut(value, "@")
	if local == "" || !strings.Contains(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", invalid(errInvalidRecipient, "to must be a valid email address")
	}
	return strings.ToLower(value), nil
}

// ValidateIdempotencyKey checks the optional key.
func ValidateIdempotencyKey(key string) error {
	if utf8.RuneCountInString(key) > maxIdempotencyKey {
		return invalid("invalid_request", "idempotency_key must be at most %d characters", maxIdempotencyKey)
	}
	return nil
}

// ValidateData checks data against the template's rules and returns the
// cleaned copy to store (only the fields the template uses).
func ValidateData(template string, data Data) (Data, error) {
	name := strings.TrimSpace(data.Name)
	if utf8.RuneCountInString(name) > maxNameLength {
		return Data{}, invalid(errInvalidData, "data.name must be at most %d characters", maxNameLength)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return Data{}, invalid(errInvalidData, "data.name must not contain control characters")
		}
	}
	switch template {
	case TemplatePasswordResetCode:
		if !codePattern.MatchString(data.Code) {
			return Data{}, invalid(errInvalidData, "data.code must be %d digits", codeLength)
		}
		if data.ExpiresMinutes == nil || *data.ExpiresMinutes < minExpiresMinutes || *data.ExpiresMinutes > maxExpiresMinutes {
			return Data{}, invalid(errInvalidData, "data.expires_minutes must be an integer between %d and %d", minExpiresMinutes, maxExpiresMinutes)
		}
		minutes := *data.ExpiresMinutes
		return Data{Name: name, Code: data.Code, ExpiresMinutes: &minutes}, nil
	case TemplatePasswordChanged:
		return Data{Name: name}, nil
	default:
		return Data{}, invalid(errUnknownTemplate, "unknown template")
	}
}
