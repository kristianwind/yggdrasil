// Package totp implements RFC 6238 time-based one-time passwords (TOTP) with
// HMAC-SHA1, a 30-second step, and 6 digits — compatible with Google
// Authenticator, Authy, etc. No external dependencies.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	step   = 30
	digits = 6
)

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random base32 secret.
func GenerateSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return enc.EncodeToString(b), nil
}

// code computes the TOTP for a given counter.
func code(secret string, counter uint64) (string, error) {
	key, err := enc.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	val := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%0*d", digits, val%1_000_000), nil
}

// Validate checks a code against the secret, allowing ±1 step for clock skew.
func Validate(secret, input string) bool {
	input = strings.TrimSpace(input)
	if len(input) != digits {
		return false
	}
	counter := uint64(time.Now().Unix() / step)
	for _, c := range []uint64{counter - 1, counter, counter + 1} {
		if got, err := code(secret, c); err == nil && hmacEqual(got, input) {
			return true
		}
	}
	return false
}

// hmacEqual is a constant-time string compare.
func hmacEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// URI builds an otpauth:// URL for enrolling in an authenticator app.
func URI(secret, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("digits", fmt.Sprintf("%d", digits))
	q.Set("period", fmt.Sprintf("%d", step))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
