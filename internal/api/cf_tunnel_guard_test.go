package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// makeConnectorToken builds a token in the shape cloudflared actually takes:
// base64 over {"a":account,"t":tunnel,"s":secret}. The real ones are unpadded,
// but a token pasted from somewhere else can arrive padded, so both are tested.
func makeConnectorToken(t *testing.T, tunnelID string, padded bool) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"a": "0123456789abcdef0123456789abcdef",
		"t": tunnelID,
		"s": "not-a-real-secret",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if padded {
		return base64.StdEncoding.EncodeToString(b)
	}
	return base64.RawStdEncoding.EncodeToString(b)
}

func TestTunnelIDFromToken(t *testing.T) {
	const want = "788ba5df-f620-41f7-9b5a-80f0f088e974"

	for _, padded := range []bool{false, true} {
		got := tunnelIDFromToken(makeConnectorToken(t, want, padded))
		if got != want {
			t.Errorf("padded=%v: got %q, want %q", padded, got, want)
		}
	}

	// A token is a credential. Whatever goes wrong, the answer is "no id" — never
	// a partial parse and never the raw input echoed back.
	for _, junk := range []string{"", "   ", "not-base64!!", "YWJj" /* "abc" */, "e30" /* "{}" */} {
		if got := tunnelIDFromToken(junk); got != "" {
			t.Errorf("tunnelIDFromToken(%q) = %q, want empty", junk, got)
		}
	}
}

// seedConnector inserts a cloudflared server holding a connector token for
// tunnelID, the way the builtin rune stores it.
func seedConnector(t *testing.T, s *Server, name, tunnelID string) {
	t.Helper()
	env, err := json.Marshal(map[string]string{"TUNNEL_TOKEN": makeConnectorToken(t, tunnelID, false)})
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, env_json, data_dir)
		 VALUES (?,?,'cloudflared','running',?,?)`,
		name, name, string(env), "/tmp/"+name); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
}

// The failure this guards against is silent by construction: a wrong tunnel id
// makes every Cloudflare call succeed and every hostname report "provisioned",
// while the traffic goes to another machine entirely. Measured on a live fleet —
// one panel carried another panel's tunnel id and wrote ingress rules into a
// tunnel it did not own.
func TestCFTunnelWarning(t *testing.T) {
	ctx := context.Background()
	const ours = "788ba5df-f620-41f7-9b5a-80f0f088e974"
	const theirs = "44146829-46e1-446f-9d2e-c9f786a2a156"

	t.Run("agrees", func(t *testing.T) {
		s := testServer(t)
		s.setSetting(ctx, "cf_tunnel_id", ours)
		seedConnector(t, s, "Cloudflare", ours)
		if got := s.cfTunnelWarning(ctx); got != "" {
			t.Errorf("want no warning when the ids agree, got %q", got)
		}
	})

	t.Run("disagrees", func(t *testing.T) {
		s := testServer(t)
		s.setSetting(ctx, "cf_tunnel_id", theirs)
		seedConnector(t, s, "Cloudflare", ours)
		got := s.cfTunnelWarning(ctx)
		if got == "" {
			t.Fatal("want a warning when the panel is pointed at a tunnel this host does not run")
		}
		// Both ids have to appear, or the operator cannot tell which to change.
		for _, want := range []string{ours, theirs} {
			if !strings.Contains(got, want) {
				t.Errorf("warning does not name %s: %q", want, got)
			}
		}
		if !strings.Contains(got, "Cloudflare") {
			t.Errorf("warning does not name the connector server: %q", got)
		}
	})

	t.Run("no connector on this host", func(t *testing.T) {
		s := testServer(t)
		s.setSetting(ctx, "cf_tunnel_id", theirs)
		if got := s.cfTunnelWarning(ctx); got != "" {
			t.Errorf("a panel behind someone else's connector is a normal setup, not a fault: %q", got)
		}
	})

	t.Run("nothing configured", func(t *testing.T) {
		s := testServer(t)
		seedConnector(t, s, "Cloudflare", ours)
		if got := s.cfTunnelWarning(ctx); got != "" {
			t.Errorf("no tunnel id set means Cloudflare is not in use here: %q", got)
		}
	})

	// A connector whose token is blank or unreadable must not produce a warning:
	// "I cannot tell" and "these disagree" are different answers, and only one of
	// them should send someone to change a setting.
	t.Run("unreadable token", func(t *testing.T) {
		s := testServer(t)
		s.setSetting(ctx, "cf_tunnel_id", theirs)
		s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, env_json, data_dir)
		           VALUES ('c2','Cloudflare','cloudflared','running','{"TUNNEL_TOKEN":"garbage"}','/tmp/c2')`)
		if got := s.cfTunnelWarning(ctx); got != "" {
			t.Errorf("want silence when the token cannot be read, got %q", got)
		}
	})

	// The token field holding a tunnel id is its own mistake and gets its own
	// answer. Measured on a live panel: 36 characters in the token field, no
	// connector container at all, and a Cloudflare integration that had quietly
	// never worked.
	t.Run("tunnel id pasted into the token field", func(t *testing.T) {
		s := testServer(t)
		s.setSetting(ctx, "cf_tunnel_id", ours)
		env, _ := json.Marshal(map[string]string{"TUNNEL_TOKEN": ours})
		s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, env_json, data_dir)
		           VALUES ('c3','Cloudflare (158)','cloudflared','stopped',?,'/tmp/c3')`, string(env))
		got := s.cfTunnelWarning(ctx)
		if !strings.Contains(got, "connector token") {
			t.Fatalf("want a warning naming the token field, got %q", got)
		}
		if strings.Contains(got, "serves tunnel") {
			t.Errorf("must not be reported as a mismatch — nothing is being compared: %q", got)
		}
		// The token is a credential even when it is the wrong one.
		if strings.Contains(got, ours) {
			t.Errorf("warning must not echo the field's contents back: %q", got)
		}
	})
}
