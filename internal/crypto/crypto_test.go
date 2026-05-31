package crypto

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := New("panel-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"", "hunter2", "a much longer secret with spaces & symbols !@#"} {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if enc == plain && plain != "" {
			t.Error("ciphertext equals plaintext")
		}
		dec, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if dec != plain {
			t.Errorf("round trip: got %q want %q", dec, plain)
		}
	}
}

func TestEncryptIsNondeterministic(t *testing.T) {
	c, _ := New("k")
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Error("two encryptions of the same plaintext are identical (nonce reuse)")
	}
}

func TestWrongKeyFails(t *testing.T) {
	c1, _ := New("key-a")
	c2, _ := New("key-b")
	enc, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Error("decryption with the wrong key should fail")
	}
}
