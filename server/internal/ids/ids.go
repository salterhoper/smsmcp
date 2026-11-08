// Package ids generates identifiers and secrets.
//
// Message and event IDs are ULIDs (time-sortable, 26 Crockford base32 chars)
// with a short type prefix, e.g. "msg_01JC7Z...". Secrets are random bytes in
// base64url and are only ever stored as SHA-256 hashes.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ULID returns a 26-character ULID for time t.
func ULID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (40 - 8*i))
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	// 128 bits are encoded as 26 five-bit groups; the first group carries
	// two implicit leading zero bits.
	bit := func(pos int) int {
		if pos < 0 {
			return 0
		}
		return int(b[pos/8]>>(7-pos%8)) & 1
	}
	out := make([]byte, 26)
	for i := range out {
		v := 0
		for j := 0; j < 5; j++ {
			v = v<<1 | bit(i*5-2+j)
		}
		out[i] = crockford[v]
	}
	return string(out)
}

// New returns prefix + "_" + ULID(now).
func New(prefix string) string { return prefix + "_" + ULID(time.Now()) }

// Secret returns prefix + "_" + n random bytes in base64url (no padding).
func Secret(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
}

// WebhookSecret returns a Standard Webhooks signing secret: "whsec_" + base64(24 random bytes).
func WebhookSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}

// Hash returns the hex SHA-256 of s. Used for tokens, keys and dedup keys.
func Hash(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0x1f}) // unit separator keeps ("ab","c") != ("a","bc")
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Digits returns a random numeric code of length n (for number verification).
func Digits(n int) string {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%0*d", n, v)
}
