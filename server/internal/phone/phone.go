// Package phone normalises and compares phone numbers.
package phone

import (
	"errors"
	"strings"
)

var ErrInvalid = errors.New("phone number must be 3-15 digits, optionally starting with +")

// Normalize strips spaces, dashes, dots and brackets and validates the result.
// It does not add a country code: "+255712345678" and "0712345678" stay distinct,
// use SameNumber to compare them.
func Normalize(s string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", ErrInvalid
		}
	}
	out := b.String()
	digits := strings.TrimPrefix(out, "+")
	if len(digits) < 3 || len(digits) > 15 {
		return "", ErrInvalid
	}
	return out, nil
}

// SameNumber reports whether a and b are probably the same subscriber, by
// comparing the last 9 digits. This tolerates "+255 712..." vs "0712...".
func SameNumber(a, b string) bool {
	da, db := digitsOnly(a), digitsOnly(b)
	if len(da) < 7 || len(db) < 7 {
		return da == db
	}
	n := min(9, len(da), len(db))
	return da[len(da)-n:] == db[len(db)-n:]
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
