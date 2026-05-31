package totp

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 test vector: ASCII secret "12345678901234567890" at T=59 (counter 1)
// yields 287082 for SHA-1 / 6 digits.
func TestRFC6238Vector(t *testing.T) {
	secret := enc.EncodeToString([]byte("12345678901234567890"))
	got, err := code(secret, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "287082" {
		t.Errorf("code = %s, want 287082", got)
	}
}

func TestValidateRoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	// Compute the current code and validate it.
	cur, _ := code(secret, uint64(time.Now().Unix()/step))
	if !Validate(secret, cur) {
		t.Error("current code should validate")
	}
	if cur != "000000" && Validate(secret, "000000") {
		t.Error("a wrong code should not validate")
	}
}

func TestURI(t *testing.T) {
	u := URI("ABC", "admin", "Yggdrasil")
	if !strings.HasPrefix(u, "otpauth://totp/") || !strings.Contains(u, "secret=ABC") {
		t.Errorf("unexpected URI: %s", u)
	}
}
