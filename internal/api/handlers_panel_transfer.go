package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Panel-settings transfer — move a host's configuration (integrations, keys,
// channels, users) to another running panel WITHOUT overwriting it. This is the
// selective, mergeable sibling of `yggdrasil migrate` (which moves everything,
// servers and all, but replaces the target). Secrets travel decrypted and are
// re-encrypted with the target's key — the same proven contract as the
// per-server transfer, and the same warning: the bundle is a credential.
//
// Deliberately NOT carried: API tokens (signed by each panel's own secret key —
// re-issue on the target), beacon identity (per-install by design), passkeys
// (bound to user handles and re-registered in seconds), and *_last/message-id
// bookkeeping state.

const panelBundleVersion = 1

type panelChannel struct {
	Type    string `json:"type"`
	Config  string `json:"config"` // decrypted
	Enabled int    `json:"enabled"`
}

type panelAIConfig struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	BaseURL           string `json:"base_url"`
	APIKey            string `json:"api_key"` // decrypted
	Enabled           int    `json:"enabled"`
	DigestEnabled     int    `json:"digest_enabled"`
	DigestHour        int    `json:"digest_hour"`
	ActionsEnabled    int    `json:"actions_enabled"`
	ProactiveLevel    int    `json:"proactive_level"`
	ProactiveTriggers string `json:"proactive_triggers"`
}

type panelSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Enc   bool   `json:"enc"` // stored encrypted at rest — re-encrypt on import
}

type panelUser struct {
	ID           string   `json:"id"` // kept so permission rows stay attached
	Username     string   `json:"username"`
	PasswordHash string   `json:"password_hash"` // argon2id — portable as-is
	Role         string   `json:"role"`
	Disabled     int      `json:"disabled"`
	TOTPEnabled  int      `json:"totp_enabled"`
	TOTPSecret   string   `json:"totp_secret,omitempty"` // decrypted
	Permissions  []string `json:"permissions,omitempty"` // "scope_type|scope_id|perms"
}

type panelBundle struct {
	Version        int            `json:"version"`
	Channels       []panelChannel `json:"channels,omitempty"` // global notification channels
	AIConfig       *panelAIConfig `json:"ai_config,omitempty"`
	Settings       []panelSetting `json:"settings,omitempty"`
	RuneRepos      []runeRepoDTO  `json:"rune_repos,omitempty"`
	GlobalWatchers []watcherDTO   `json:"global_watchers,omitempty"`
	Users          []panelUser    `json:"users,omitempty"`
}

// portableSettings is the curated allowlist of settings keys worth moving
// between hosts, per include-group. Anything stateful, host-bound or
// identity-bound (beacon, message ids, last-run stamps) is deliberately absent.
var portableSettings = map[string][]string{
	"integrations": {
		"steam_web_api_key", "battlemetrics_token",
		"discord_bot_token", "discord_bot_control_channel",
		"discord_status_webhook", "discord_status_enabled", "discord_status_title",
	},
	"network": {
		"npm_enabled", "npm_url", "npm_email", "npm_password", "npm_base_domain", "npm_internal_host", "npm_le_email",
		"cf_enabled", "cf_api_token", "cf_account_id", "cf_base_domain", "cf_internal_host", "cf_tunnel_id",
		"unifi_enabled", "unifi_url", "unifi_user", "unifi_pass", "unifi_site",
		"upnp_enabled", "base_domain", "public_hostname",
	},
}

// handlePanelExport streams the selected configuration groups as a JSON bundle.
// ?include=channels,ai,integrations,network,rune_repos,watchers,users
func (s *Server) handlePanelExport(w http.ResponseWriter, r *http.Request) {
	include := map[string]bool{}
	for _, g := range strings.Split(r.URL.Query().Get("include"), ",") {
		if g = strings.TrimSpace(g); g != "" {
			include[g] = true
		}
	}
	if len(include) == 0 {
		jsonError(w, "pick at least one group to export", http.StatusBadRequest)
		return
	}
	b := s.buildPanelBundle(r.Context(), include)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="panel-settings.yggpanel.json"`)
	json.NewEncoder(w).Encode(b) //nolint:errcheck
	s.auditLog(r, "panel.export", "panel", map[string]any{"groups": r.URL.Query().Get("include")})
}

// buildPanelBundle assembles the selected configuration groups with secrets
// decrypted — shared by the settings-only download and the migration archive.
func (s *Server) buildPanelBundle(ctx context.Context, include map[string]bool) panelBundle {
	b := panelBundle{Version: panelBundleVersion}

	if include["channels"] {
		rows, err := s.db.QueryContext(ctx,
			"SELECT type, config_enc, enabled FROM notifications WHERE COALESCE(server_id,'')=''")
		if err == nil {
			for rows.Next() {
				var c panelChannel
				if rows.Scan(&c.Type, &c.Config, &c.Enabled) == nil {
					if plain, derr := s.cipher.Decrypt(c.Config); derr == nil {
						c.Config = plain
					}
					b.Channels = append(b.Channels, c)
				}
			}
			rows.Close()
		}
	}
	if include["ai"] {
		var a panelAIConfig
		var enc string
		if s.db.QueryRowContext(ctx, `SELECT provider, model, base_url, COALESCE(api_key_enc,''), enabled,
			COALESCE(digest_enabled,0), COALESCE(digest_hour,8), COALESCE(actions_enabled,0),
			COALESCE(proactive_level,0), COALESCE(proactive_triggers,'') FROM ai_config WHERE id=1`).
			Scan(&a.Provider, &a.Model, &a.BaseURL, &enc, &a.Enabled, &a.DigestEnabled, &a.DigestHour,
				&a.ActionsEnabled, &a.ProactiveLevel, &a.ProactiveTriggers) == nil {
			if plain, derr := s.cipher.Decrypt(enc); derr == nil {
				a.APIKey = plain
			}
			b.AIConfig = &a
		}
	}
	for group, keys := range portableSettings {
		if !include[group] {
			continue
		}
		for _, k := range keys {
			v := s.getSetting(ctx, k)
			if v == "" {
				continue
			}
			ps := panelSetting{Key: k, Value: v}
			// Encrypted-at-rest values decrypt cleanly; plaintext ones don't. That
			// asymmetry is the marker for what the import must re-encrypt.
			if plain, err := s.cipher.Decrypt(v); err == nil {
				ps.Value, ps.Enc = plain, true
			}
			b.Settings = append(b.Settings, ps)
		}
	}
	if include["rune_repos"] {
		rows, err := s.db.QueryContext(ctx, "SELECT id, name, repo, path, COALESCE(ref,'main') FROM rune_repos")
		if err == nil {
			for rows.Next() {
				var d runeRepoDTO
				if rows.Scan(&d.ID, &d.Name, &d.Repo, &d.Path, &d.Ref) == nil {
					b.RuneRepos = append(b.RuneRepos, d)
				}
			}
			rows.Close()
		}
	}
	if include["watchers"] {
		rows, err := s.db.QueryContext(ctx,
			`SELECT id, server_id, name, pattern, threshold, window_secs, action, enabled, COALESCE(last_fired,''), COALESCE(source,'')
			 FROM log_watchers WHERE server_id=''`)
		if err == nil {
			for rows.Next() {
				var d watcherDTO
				var en int
				if rows.Scan(&d.ID, &d.ServerID, &d.Name, &d.Pattern, &d.Threshold, &d.WindowSecs, &d.Action, &en, &d.LastFired, &d.Source) == nil {
					d.Enabled = en == 1
					b.GlobalWatchers = append(b.GlobalWatchers, d)
				}
			}
			rows.Close()
		}
	}
	if include["users"] {
		// Two passes: collect users, close the rows, THEN their permissions. The DB
		// pool is capped at one connection (SQLite writer serialization), so a
		// nested query inside an open rows iterator deadlocks the whole panel.
		rows, err := s.db.QueryContext(ctx,
			`SELECT id, username, password_hash, role, disabled, COALESCE(totp_enabled,0), COALESCE(totp_secret,'') FROM users`)
		if err == nil {
			for rows.Next() {
				var u panelUser
				if rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Disabled, &u.TOTPEnabled, &u.TOTPSecret) != nil {
					continue
				}
				if u.TOTPSecret != "" {
					if plain, derr := s.cipher.Decrypt(u.TOTPSecret); derr == nil {
						u.TOTPSecret = plain
					}
				}
				b.Users = append(b.Users, u)
			}
			rows.Close()
		}
		for i := range b.Users {
			prows, perr := s.db.QueryContext(ctx,
				"SELECT scope_type, COALESCE(scope_id,''), perms FROM permissions WHERE user_id=?", b.Users[i].ID)
			if perr != nil {
				continue
			}
			for prows.Next() {
				var st, sid, pm string
				if prows.Scan(&st, &sid, &pm) == nil {
					b.Users[i].Permissions = append(b.Users[i].Permissions, st+"|"+sid+"|"+pm)
				}
			}
			prows.Close()
		}
	}

	return b
}

// handlePanelImport merges a settings bundle into this panel. Additive by
// design: existing rows are never overwritten or deleted — a channel/repo/
// watcher/user that already exists here is skipped and reported, not clobbered.
// The two exceptions that ARE overwritten when present in the bundle: the
// single-row AI config and the curated settings keys, because "move my Kvasir
// setup / Steam key over" is the entire point of including them.
func (s *Server) handlePanelImport(w http.ResponseWriter, r *http.Request) {
	var b panelBundle
	if decodeJSON(r, &b) != nil || b.Version < 1 || b.Version > panelBundleVersion {
		jsonError(w, "not a valid panel-settings bundle", http.StatusBadRequest)
		return
	}
	summary := s.applyPanelBundle(r.Context(), b)
	if b.AIConfig != nil {
		s.startDiscordBot() // the config may carry a bot token — reconnect with it
	}
	s.auditLog(r, "panel.import", "panel", map[string]any{"summary": fmt.Sprintf("%v", summary)})
	jsonOK(w, summary)
}

// applyPanelBundle merges a bundle into this panel's database. Additive: rows
// that already exist are skipped and counted, never overwritten — except the
// single-row AI config and the allowlisted settings keys, where applying the
// incoming value is the point.
func (s *Server) applyPanelBundle(ctx context.Context, b panelBundle) map[string]int {
	summary := map[string]int{}

	existingChannels := map[string]bool{}
	if rows, err := s.db.QueryContext(ctx, "SELECT type, config_enc FROM notifications WHERE COALESCE(server_id,'')=''"); err == nil {
		for rows.Next() {
			var typ, enc string
			if rows.Scan(&typ, &enc) == nil {
				plain := enc
				if p, derr := s.cipher.Decrypt(enc); derr == nil {
					plain = p
				}
				existingChannels[typ+"|"+plain] = true
			}
		}
		rows.Close()
	}
	for _, c := range b.Channels {
		if existingChannels[c.Type+"|"+c.Config] {
			summary["channels_skipped"]++
			continue
		}
		enc, err := s.cipher.Encrypt(c.Config)
		if err != nil {
			continue
		}
		s.db.ExecContext(ctx, "INSERT INTO notifications (id, type, config_enc, enabled, server_id) VALUES (?,?,?,?,'')",
			uuid.New().String(), c.Type, enc, c.Enabled)
		summary["channels_added"]++
	}

	if a := b.AIConfig; a != nil {
		keyEnc := ""
		if a.APIKey != "" {
			keyEnc, _ = s.cipher.Encrypt(a.APIKey)
		}
		s.db.ExecContext(ctx, `INSERT INTO ai_config (id, provider, model, base_url, api_key_enc, enabled, digest_enabled,
			digest_hour, actions_enabled, proactive_level, proactive_triggers, updated_at)
			VALUES (1,?,?,?,?,?,?,?,?,?,?,datetime('now'))
			ON CONFLICT(id) DO UPDATE SET provider=excluded.provider, model=excluded.model, base_url=excluded.base_url,
			api_key_enc=excluded.api_key_enc, enabled=excluded.enabled, digest_enabled=excluded.digest_enabled,
			digest_hour=excluded.digest_hour, actions_enabled=excluded.actions_enabled,
			proactive_level=excluded.proactive_level, proactive_triggers=excluded.proactive_triggers, updated_at=excluded.updated_at`,
			a.Provider, a.Model, a.BaseURL, keyEnc, a.Enabled, a.DigestEnabled, a.DigestHour,
			a.ActionsEnabled, a.ProactiveLevel, a.ProactiveTriggers)
		summary["ai_config"] = 1
	}

	allowed := map[string]bool{}
	for _, keys := range portableSettings {
		for _, k := range keys {
			allowed[k] = true
		}
	}
	for _, ps := range b.Settings {
		if !allowed[ps.Key] {
			summary["settings_rejected"]++ // never let a bundle write arbitrary keys
			continue
		}
		v := ps.Value
		if ps.Enc {
			enc, err := s.cipher.Encrypt(v)
			if err != nil {
				continue
			}
			v = enc
		}
		s.setSetting(ctx, ps.Key, v)
		summary["settings_applied"]++
	}

	for _, d := range b.RuneRepos {
		var have int
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rune_repos WHERE repo=? AND path=? AND COALESCE(ref,'main')=?",
			d.Repo, d.Path, d.Ref).Scan(&have)
		if have > 0 {
			summary["rune_repos_skipped"]++
			continue
		}
		s.db.ExecContext(ctx, "INSERT INTO rune_repos (id, name, repo, path, ref) VALUES (?,?,?,?,?)",
			uuid.New().String(), d.Name, d.Repo, d.Path, d.Ref)
		summary["rune_repos_added"]++
	}

	for _, d := range b.GlobalWatchers {
		var have int
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM log_watchers WHERE server_id='' AND name=?", d.Name).Scan(&have)
		if have > 0 {
			summary["watchers_skipped"]++
			continue
		}
		s.db.ExecContext(ctx,
			"INSERT INTO log_watchers (id, server_id, name, pattern, threshold, window_secs, action, enabled, source) VALUES (?,'',?,?,?,?,?,?,?)",
			uuid.New().String(), d.Name, d.Pattern, d.Threshold, d.WindowSecs, d.Action, boolToInt(d.Enabled), d.Source)
		summary["watchers_added"]++
	}

	for _, u := range b.Users {
		var have int
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE username=? OR id=?", u.Username, u.ID).Scan(&have)
		if have > 0 {
			summary["users_skipped"]++ // never touch an existing account
			continue
		}
		totpEnc := ""
		if u.TOTPSecret != "" {
			totpEnc, _ = s.cipher.Encrypt(u.TOTPSecret)
		}
		if _, err := s.db.ExecContext(ctx,
			"INSERT INTO users (id, username, password_hash, role, disabled, totp_enabled, totp_secret) VALUES (?,?,?,?,?,?,?)",
			u.ID, u.Username, u.PasswordHash, u.Role, u.Disabled, u.TOTPEnabled, totpEnc); err != nil {
			continue
		}
		for _, p := range u.Permissions {
			parts := strings.SplitN(p, "|", 3)
			if len(parts) != 3 {
				continue
			}
			s.db.ExecContext(ctx, "INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES (?,?,?,?,?)",
				uuid.New().String(), u.ID, parts[0], nullableStr(parts[1]), parts[2])
		}
		summary["users_added"]++
	}
	return summary
}
