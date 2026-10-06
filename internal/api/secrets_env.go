package api

import (
	"strings"
	"unicode"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// Secret-typed environment variables (password fields + the RCON password) are
// stored ENCRYPTED at rest in servers.env_json, matching how provider/backup
// creds are protected. They're encrypted just before persisting and decrypted
// only in loadRuntime (the single path that feeds container env + RCON), and
// masked in API responses. Non-secret env stays plaintext.

// credentialWords are the names a variable goes by when it holds one.
//
// Matched as WHOLE WORDS after splitting the key, not as substrings: "PASS"
// inside BYPASS and "KEY" inside KEYBOARD or MONKEY are not credentials, and a
// substring match would mask them while teaching nobody anything. The cost of a
// wrong match is a value the operator cannot read back in the API; the cost of a
// miss is a password in a log, an LLM prompt, or a delegate's browser.
var credentialWords = map[string]bool{
	"PASSWORD": true, "PASSWD": true, "PASS": true,
	"SECRET": true, "TOKEN": true, "KEY": true, "APIKEY": true,
	"CREDENTIAL": true, "CREDENTIALS": true, "PSK": true,
}

// looksSecret reports whether a variable NAME promises a credential.
//
// This is the floor, and it exists because the ceiling was wrong: masking and
// at-rest encryption keyed off the rune's `secret: true` flag alone, so a rune
// that simply did not set it — 20 variables across 15 of the shipped ones,
// cloudflared's TUNNEL_TOKEN and WordPress's DB_ROOT_PASSWORD among them — had
// its credentials stored in the clear, returned by GET /api/servers/{id} to
// anyone with server.view, and put verbatim into the prompt sent to an external
// model. The flag is a promise the rune author has to remember; the name is
// evidence that is already there.
//
// Same rule as everywhere else here: mask by KEY NAME, in every branch. A check
// on the rune's topic or type is a check on what somebody meant, not on what is
// in the map.
func looksSecret(key string) bool {
	for _, w := range splitKeyWords(key) {
		if credentialWords[w] {
			return true
		}
	}
	return false
}

// splitKeyWords breaks DB_ROOT_PASSWORD, apiKey and FTLCONF_webserver_api_password
// into upper-case words.
func splitKeyWords(key string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToUpper(cur.String()))
			cur.Reset()
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(runes[i-1]):
			// camelCase: apiKey -> api, Key
			flush()
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}

// secretEnvKeys returns the env var keys whose values are secrets for gs.
func secretEnvKeys(gs *gameskill.Gameskill) map[string]bool {
	keys := map[string]bool{}
	if gs == nil {
		return keys
	}
	for _, v := range gs.Variables {
		if v.Secret || looksSecret(v.Key) {
			keys[v.Key] = true
		}
	}
	if gs.RCON != nil && gs.RCON.PasswordVar != "" {
		keys[gs.RCON.PasswordVar] = true
	}
	return keys
}

// encryptSecretEnv encrypts secret-typed values in env (in place) before they're
// written to env_json. It's idempotent: a value that already decrypts (i.e. is
// already ciphertext) is left untouched, so re-saving without decrypting first —
// as the update-merge path does — never double-encrypts.
func (s *Server) encryptSecretEnv(env map[string]string, gs *gameskill.Gameskill) {
	if s.cipher == nil {
		return
	}
	for k := range secretEnvKeys(gs) {
		v := env[k]
		if v == "" {
			continue
		}
		if _, err := s.cipher.Decrypt(v); err == nil {
			continue // already ciphertext
		}
		if enc, err := s.cipher.Encrypt(v); err == nil {
			env[k] = enc
		}
	}
}

// decryptSecretEnv decrypts secret-typed values in env (in place) after reading
// env_json. Legacy plaintext values (written before at-rest encryption) don't
// decrypt and are left as-is, so they still work and get encrypted on next save
// (lazy migration).
func (s *Server) decryptSecretEnv(env map[string]string, gs *gameskill.Gameskill) {
	if s.cipher == nil {
		return
	}
	for k := range secretEnvKeys(gs) {
		if v := env[k]; v != "" {
			if dec, err := s.cipher.Decrypt(v); err == nil {
				env[k] = dec
			}
		}
	}
}

// maskSecretEnv replaces every credential value in env with secretMask.
//
// It walks the ENV MAP, not the rune's variable list, because the two are not
// the same set: an imported egg, a migrated server or a rune that changed its
// variables can leave keys in env_json that no Variable declares, and a loop
// over the declarations never sees them. gs is optional -- passing nil applies
// the name-based floor alone, which is what a caller that could not load the
// runtime still owes the operator.
//
// The update handler reads secretMask as "keep the existing value", so a masked
// field round-trips through the edit form without clobbering the real one.
func maskSecretEnv(env map[string]string, gs *gameskill.Gameskill) {
	declared := secretEnvKeys(gs)
	for k, v := range env {
		if v == "" {
			continue
		}
		if declared[k] || looksSecret(k) {
			env[k] = secretMask
		}
	}
}
