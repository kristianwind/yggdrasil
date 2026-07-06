package api

import (
	"net/http/httptest"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestRPParams(t *testing.T) {
	cases := []struct {
		host, xfProto, wantID, wantOrigin string
	}{
		{"dinesen.nolimit.dk", "https", "dinesen.nolimit.dk", "https://dinesen.nolimit.dk"},
		{"yggdrasil.nolimit.dk", "", "yggdrasil.nolimit.dk", "https://yggdrasil.nolimit.dk"}, // no TLS, no XFP handled below
		{"localhost:8080", "http", "localhost", "http://localhost:8080"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Host = c.host
		if c.xfProto != "" {
			r.Header.Set("X-Forwarded-Proto", c.xfProto)
		}
		id, origin := rpParams(r)
		if id != c.wantID {
			t.Errorf("host %q: rpID=%q want %q", c.host, id, c.wantID)
		}
		// The middle case has no XFP and no TLS on httptest -> scheme http.
		if c.xfProto == "" {
			if origin != "http://"+c.host {
				t.Errorf("host %q: origin=%q want http://%s", c.host, origin, c.host)
			}
			continue
		}
		if origin != c.wantOrigin {
			t.Errorf("host %q: origin=%q want %q", c.host, origin, c.wantOrigin)
		}
	}
}

func TestWASessionStore(t *testing.T) {
	st := &waSessionStore{m: map[string]waSessionEntry{}}
	data := &webauthn.SessionData{Challenge: "abc"}
	id := st.put(data)
	if got := st.take(id); got == nil || got.Challenge != "abc" {
		t.Fatal("session not returned")
	}
	if st.take(id) != nil {
		t.Fatal("session should be single-use (consumed on take)")
	}
	if st.take("nonexistent") != nil {
		t.Fatal("unknown session id must return nil")
	}
}
