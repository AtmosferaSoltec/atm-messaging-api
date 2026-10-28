// Package phone normalizes phone numbers received from API clients.
package phone

import (
	"errors"
	"strings"
)

const (
	minDigits = 8
	maxDigits = 15
)

// ErrInvalid is returned for numbers that are not in international format.
var ErrInvalid = errors.New("phone must be in international format (8 to 15 digits)")

// Normalize strips a leading "+" and common separators and requires 8 to 15 digits.
func Normalize(value string) (string, error) {
	cleaned := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(value))
	cleaned = strings.TrimPrefix(cleaned, "+")
	if len(cleaned) < minDigits || len(cleaned) > maxDigits || strings.Trim(cleaned, "0123456789") != "" {
		return "", ErrInvalid
	}
	return cleaned, nil
}
