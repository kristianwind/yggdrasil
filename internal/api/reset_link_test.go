package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// POST /api/auth/forgot is unauthenticated, and the reset link was built from
// the request's own Host header. So anyone who knows a username could send the
// request with Host: attacker.example and have the panel mail the real user a
// working reset link pointing at the attacker.
//
// panelBaseURL is right for the OAuth metadata — a client has to be told the
// address it actually reached — and wrong for something posted into an email,
// which is the distinction this adds.
func TestResetLinkIgnoresTheHostHeaderWhenAHostnameIsConfigured(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	forged := httptest.NewRequest("POST", "http://panel.example/api/auth/forgot", nil)
	forged.Host = "attacker.example"

	// No public hostname set: nothing to check the header against, so it still
	// decides. Asserted rather than left implicit, so the limit is visible.
	if got := s.resetLinkBase(ctx, forged); !strings.Contains(got, "attacker.example") {
		t.Errorf("with no public_hostname the link base was %q; the header is all there is", got)
	}

	for _, stored := range []string{"panel.nolimit.dk", "https://panel.nolimit.dk", "https://panel.nolimit.dk/"} {
		s.setSetting(ctx, "public_hostname", stored)
		got := s.resetLinkBase(ctx, forged)
		if got != "https://panel.nolimit.dk" {
			t.Errorf("public_hostname=%q gave %q, want https://panel.nolimit.dk", stored, got)
		}
		if strings.Contains(got, "attacker") {
			t.Errorf("the forged Host survived into the reset link: %q", got)
		}
	}
}

// A bare hostname has to gain a scheme, or the browser reads it as a path and
// the link goes nowhere — the same mistake the WordPress rune's site-address
// field guards against.
func TestResetLinkAddsAMissingScheme(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.setSetting(ctx, "public_hostname", "panel.nolimit.dk")
	r := httptest.NewRequest("POST", "http://x/api/auth/forgot", nil)

	if got := s.resetLinkBase(ctx, r); !strings.HasPrefix(got, "https://") {
		t.Errorf("link base %q has no scheme", got)
	}
}
