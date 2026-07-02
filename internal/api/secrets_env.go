package api

import "github.com/kristianwind/yggdrasil/internal/gameskill"

// Secret-typed environment variables (password fields + the RCON password) are
// stored ENCRYPTED at rest in servers.env_json, matching how provider/backup
// creds are protected. They're encrypted just before persisting and decrypted
// only in loadRuntime (the single path that feeds container env + RCON), and
// masked in API responses. Non-secret env stays plaintext.

// secretEnvKeys returns the env var keys whose values are secrets for gs.
func secretEnvKeys(gs *gameskill.Gameskill) map[string]bool {
	keys := map[string]bool{}
	if gs == nil {
		return keys
	}
	for _, v := range gs.Variables {
		if v.Secret {
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
