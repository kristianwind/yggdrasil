package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite writer serialization
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id          TEXT PRIMARY KEY,
	username    TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	role        TEXT NOT NULL DEFAULT 'user',
	disabled    INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sessions (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	token_hash  TEXT UNIQUE NOT NULL,
	expires_at  TEXT NOT NULL,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS realms (
	id          TEXT PRIMARY KEY,
	name        TEXT UNIQUE NOT NULL,
	description TEXT,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS gameskills (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	category    TEXT,
	version     INTEGER NOT NULL DEFAULT 1,
	yaml_blob   TEXT NOT NULL,
	builtin     INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Builtin runes an admin explicitly deleted. Seeding skips these so a deleted
-- default rune stays deleted instead of reappearing on the next boot.
CREATE TABLE IF NOT EXISTS deleted_builtins (
	id          TEXT PRIMARY KEY,
	deleted_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- WebAuthn / passkey credentials (supplement to password+TOTP; passwordless
-- login). cred_id is the raw credential ID for lookup; cred_json is the full
-- serialized go-webauthn Credential (public key, sign count, transports, ...).
CREATE TABLE IF NOT EXISTS webauthn_credentials (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	cred_id     BLOB NOT NULL UNIQUE,
	cred_json   TEXT NOT NULL,
	name        TEXT NOT NULL DEFAULT 'passkey',
	created_at  TEXT NOT NULL DEFAULT (datetime('now')),
	last_used   TEXT
);

CREATE TABLE IF NOT EXISTS servers (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	gameskill_id TEXT NOT NULL REFERENCES gameskills(id),
	realm_id     TEXT REFERENCES realms(id),
	status       TEXT NOT NULL DEFAULT 'stopped',
	container_id TEXT,
	env_json     TEXT NOT NULL DEFAULT '{}',
	ports_json   TEXT NOT NULL DEFAULT '{}',
	cpu_limit    REAL,
	mem_limit_mb INTEGER,
	data_dir     TEXT NOT NULL,
	installed    INTEGER NOT NULL DEFAULT 0,
	install_status TEXT NOT NULL DEFAULT 'pending',
	created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS port_allocations (
	port        INTEGER PRIMARY KEY,
	server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
	protocol    TEXT NOT NULL DEFAULT 'tcp',
	name        TEXT
);

CREATE TABLE IF NOT EXISTS backup_targets (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	type           TEXT NOT NULL,
	config_enc     TEXT NOT NULL,
	created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS backups (
	id          TEXT PRIMARY KEY,
	server_id   TEXT REFERENCES servers(id) ON DELETE SET NULL,
	target_id   TEXT REFERENCES backup_targets(id) ON DELETE SET NULL,
	path        TEXT,
	size_bytes  INTEGER,
	status      TEXT NOT NULL DEFAULT 'pending',
	error_msg   TEXT,
	created_at  TEXT NOT NULL DEFAULT (datetime('now')),
	completed_at TEXT
);

CREATE TABLE IF NOT EXISTS schedules (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	server_id   TEXT REFERENCES servers(id) ON DELETE CASCADE,
	realm_id    TEXT REFERENCES realms(id) ON DELETE CASCADE,
	cron_expr   TEXT NOT NULL,
	action      TEXT NOT NULL,
	args_json   TEXT NOT NULL DEFAULT '{}',
	enabled     INTEGER NOT NULL DEFAULT 1,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS schedule_runs (
	id            TEXT PRIMARY KEY,
	schedule_id   TEXT NOT NULL,
	schedule_name TEXT,
	server_id     TEXT,
	server_name   TEXT,
	action        TEXT,
	status        TEXT,            -- ok | error | skipped
	detail        TEXT,
	ran_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_schedule_runs_sid ON schedule_runs(schedule_id, ran_at);

-- Time-series resource samples per server (CPU%, memory MB, player count),
-- taken every few minutes by the sampler and pruned to a rolling window. Powers
-- the history charts + lets the AI ops digest reason about trends.
CREATE TABLE IF NOT EXISTS metrics (
	server_id   TEXT NOT NULL,
	ts          TEXT NOT NULL DEFAULT (datetime('now')),
	cpu         REAL NOT NULL DEFAULT 0,
	mem_mb      REAL NOT NULL DEFAULT 0,
	players     INTEGER NOT NULL DEFAULT -1
);
CREATE INDEX IF NOT EXISTS idx_metrics_srv ON metrics(server_id, ts);

-- Whole-host resource history (CPU/RAM/disk), sampled alongside per-server metrics
-- so the Dashboard can show trend charts for the machine itself, not just point-in-time.
CREATE TABLE IF NOT EXISTS host_metrics (
	ts             TEXT NOT NULL DEFAULT (datetime('now')),
	cpu            REAL NOT NULL DEFAULT -1,  -- host CPU %, -1 when unavailable (non-Linux)
	mem_used_mb    REAL NOT NULL DEFAULT 0,
	mem_total_mb   REAL NOT NULL DEFAULT 0,
	disk_used_mb   REAL NOT NULL DEFAULT 0,
	disk_total_mb  REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_host_metrics_ts ON host_metrics(ts);

-- Stability log: an unexpected container exit (crash or external stop) the panel
-- caught while it still thought the server was running. Feeds the crash-history UI
-- and the "flapping" badge so a silently-dying server is visible, not a mystery.
CREATE TABLE IF NOT EXISTS server_crashes (
	server_id  TEXT NOT NULL,
	ts         TEXT NOT NULL DEFAULT (datetime('now')),
	exit_code  INTEGER NOT NULL DEFAULT 0,
	reason     TEXT NOT NULL DEFAULT ''   -- tail of the container log at exit time
);
CREATE INDEX IF NOT EXISTS idx_server_crashes ON server_crashes(server_id, ts);

-- Config-file version history: the previous contents of a text file are snapshot
-- here right before it's overwritten via the file editor, so a change that breaks
-- a server can be rolled back. Kept to the last few versions per file.
CREATE TABLE IF NOT EXISTS file_versions (
	id         TEXT PRIMARY KEY,
	server_id  TEXT NOT NULL,
	path       TEXT NOT NULL,
	content    TEXT NOT NULL,
	size       INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_file_versions ON file_versions(server_id, path, created_at);

CREATE TABLE IF NOT EXISTS audit_log (
	id          TEXT PRIMARY KEY,
	user_id     TEXT REFERENCES users(id) ON DELETE SET NULL,
	username    TEXT,
	action      TEXT NOT NULL,
	resource    TEXT,
	detail_json TEXT,
	ip          TEXT,
	ts          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS bans (
	id          TEXT PRIMARY KEY,
	player_name TEXT NOT NULL,
	player_id   TEXT,
	server_id   TEXT REFERENCES servers(id) ON DELETE CASCADE,
	reason      TEXT,
	banned_by   TEXT REFERENCES users(id) ON DELETE SET NULL,
	expires_at  TEXT,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS permissions (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	scope_type  TEXT NOT NULL,
	scope_id    TEXT,
	perms       TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(user_id, scope_type, scope_id)
);

CREATE TABLE IF NOT EXISTS api_tokens (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	token_hash  TEXT UNIQUE NOT NULL,
	last_used_at TEXT,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS notifications (
	id          TEXT PRIMARY KEY,
	type        TEXT NOT NULL,
	config_enc  TEXT NOT NULL,
	enabled     INTEGER NOT NULL DEFAULT 1,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS message_templates (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	body        TEXT NOT NULL,
	builtin     INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- The host's authorized Steam account for games that require ownership (e.g.
-- DayZ). The password is never stored; only the username + the fact that the
-- SteamCMD sentry cache (on disk) has been primed. Single row.
CREATE TABLE IF NOT EXISTS steam_account (
	id            INTEGER PRIMARY KEY CHECK (id = 1),
	username      TEXT NOT NULL,
	authorized    INTEGER NOT NULL DEFAULT 0,
	authorized_at TEXT
);

-- Optional, admin-configured LLM for advisory AI features (the admin-log
-- digest). The operator brings their own provider/endpoint/key; the key is
-- stored encrypted. Advisory only, opt-in (enabled flag). Single row.
CREATE TABLE IF NOT EXISTS ai_config (
	id            INTEGER PRIMARY KEY CHECK (id = 1),
	provider        TEXT NOT NULL DEFAULT 'openai',
	model           TEXT NOT NULL DEFAULT '',
	base_url        TEXT NOT NULL DEFAULT '',
	api_key_enc     TEXT NOT NULL DEFAULT '',
	enabled         INTEGER NOT NULL DEFAULT 0,
	digest_enabled  INTEGER NOT NULL DEFAULT 0,  -- send a daily AI ops digest to notification channels
	digest_hour     INTEGER NOT NULL DEFAULT 8,  -- local hour (0-23) to send it
	digest_last_day TEXT NOT NULL DEFAULT '',     -- YYYY-MM-DD of the last send (once-per-day guard)
	updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Violation-driven auto-actions: watch server logs for a pattern and auto
-- kick/ban a player when it recurs past a threshold within a time window.
CREATE TABLE IF NOT EXISTS violation_rules (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	pattern        TEXT NOT NULL,   -- regex; capture group 1 = player name
	threshold      INTEGER NOT NULL DEFAULT 1,
	window_minutes INTEGER NOT NULL DEFAULT 5,
	action         TEXT NOT NULL DEFAULT 'ban',  -- ban | kick
	scope_global   INTEGER NOT NULL DEFAULT 1,   -- ban everywhere vs only the offending server
	enabled        INTEGER NOT NULL DEFAULT 1,
	created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Generic key/value app settings (e.g. public hostname for connect addresses).
CREATE TABLE IF NOT EXISTS app_settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Beacon: voluntary "I'm running the panel" pings, collected by whichever
-- instance has the receiver enabled. Stores only an anonymous random instance id
-- and the panel version — no IP, no server/user data.
CREATE TABLE IF NOT EXISTS beacon_pings (
	instance_id TEXT PRIMARY KEY,
	version     TEXT NOT NULL DEFAULT '',
	first_seen  TEXT NOT NULL DEFAULT (datetime('now')),
	last_seen   TEXT NOT NULL DEFAULT (datetime('now')),
	ping_count  INTEGER NOT NULL DEFAULT 1
);
`

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	// Idempotent column additions for databases created by older versions.
	addColumnIfMissing(db, "servers", "installed", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing(db, "servers", "install_status", "TEXT NOT NULL DEFAULT 'pending'")
	addColumnIfMissing(db, "backup_targets", "keep_n", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing(db, "backup_targets", "keep_days", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing(db, "users", "totp_secret", "TEXT") // encrypted; pending until enabled
	addColumnIfMissing(db, "users", "totp_enabled", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing(db, "servers", "bm_server_id", "TEXT NOT NULL DEFAULT ''")        // BattleMetrics server id (optional)
	addColumnIfMissing(db, "servers", "auto_forward", "INTEGER NOT NULL DEFAULT 1")      // open firewall ports on start (UPnP/UniFi)
	addColumnIfMissing(db, "servers", "norn_json", "TEXT NOT NULL DEFAULT ''")           // DayZ Norn loot settings (re-applied after reinstall)
	addColumnIfMissing(db, "servers", "subdomain", "TEXT NOT NULL DEFAULT ''")           // NPM subdomain label/full domain for HTTP apps (empty = off)
	addColumnIfMissing(db, "servers", "npm_host_id", "INTEGER NOT NULL DEFAULT 0")       // NPM proxy-host id we created (0 = none)
	addColumnIfMissing(db, "servers", "cf_hostname", "TEXT NOT NULL DEFAULT ''")         // Cloudflare Tunnel hostname we provisioned (ingress + CNAME)
	addColumnIfMissing(db, "servers", "host_mounts", "TEXT NOT NULL DEFAULT ''")         // admin-set host bind mounts (JSON array); read-only by default
	addColumnIfMissing(db, "servers", "autostart", "INTEGER NOT NULL DEFAULT 1")         // start this server when the panel/host boots (default on)
	addColumnIfMissing(db, "users", "token_version", "INTEGER NOT NULL DEFAULT 0")       // bumped to revoke all of a user's JWT sessions (logout/disable/role/password change)
	addColumnIfMissing(db, "users", "totp_last_counter", "INTEGER NOT NULL DEFAULT 0")   // last accepted TOTP step; rejects replay within the validity window
	addColumnIfMissing(db, "schedules", "managed", "TEXT NOT NULL DEFAULT ''")           // non-empty = owned by a per-server convenience toggle (e.g. 'auto-restart'); hidden from the generic schedule list
	addColumnIfMissing(db, "ai_config", "digest_enabled", "INTEGER NOT NULL DEFAULT 0")  // daily AI ops digest to notification channels
	addColumnIfMissing(db, "ai_config", "digest_hour", "INTEGER NOT NULL DEFAULT 8")     // local hour to send the daily digest
	addColumnIfMissing(db, "ai_config", "digest_last_day", "TEXT NOT NULL DEFAULT ''")   // once-per-day guard (YYYY-MM-DD)
	addColumnIfMissing(db, "ai_config", "actions_enabled", "INTEGER NOT NULL DEFAULT 0") // higher tier: let AI PROPOSE server actions (always confirmed); default off
	addColumnIfMissing(db, "servers", "watchdog", "INTEGER NOT NULL DEFAULT 0")          // auto-heal: game query fails repeatedly while the container is up → auto-restart (default off)
	addColumnIfMissing(db, "servers", "status_public", "INTEGER NOT NULL DEFAULT 0")     // show this server on the public /status page (opt-in, default off)
	addColumnIfMissing(db, "backups", "verified_at", "TEXT NOT NULL DEFAULT ''")         // when this backup's archive was last integrity-checked
	addColumnIfMissing(db, "backups", "verify_ok", "INTEGER NOT NULL DEFAULT -1")        // -1 unknown, 0 corrupt, 1 decompresses cleanly
	addColumnIfMissing(db, "servers", "cpu_alarm_pct", "INTEGER NOT NULL DEFAULT 0")     // alert when CPU% stays at/above this (0 = off)
	addColumnIfMissing(db, "servers", "mem_alarm_mb", "INTEGER NOT NULL DEFAULT 0")      // alert when memory MB stays at/above this (0 = off)
	addColumnIfMissing(db, "servers", "disk_alarm_mb", "INTEGER NOT NULL DEFAULT 0")     // alert when the data dir grows to/above this many MB (0 = off)
	addColumnIfMissing(db, "servers", "notes", "TEXT NOT NULL DEFAULT ''")               // free-text admin notes shared across the team
	// Off by default: an existing note keeps rendering exactly as it reads now, so
	// turning this on is a choice rather than a surprise reformat.
	addColumnIfMissing(db, "servers", "notes_markdown", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing(db, "servers", "tags", "TEXT NOT NULL DEFAULT ''")                // normalized comma-separated labels for grouping/filtering
	return nil
}

// addColumnIfMissing adds a column to a table when it does not already exist.
// SQLite has no "ADD COLUMN IF NOT EXISTS", so we inspect the schema first.
func addColumnIfMissing(db *sql.DB, table, column, definition string) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && name == column {
			return // already present
		}
	}
	db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
}
