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
	addColumnIfMissing(db, "servers", "bm_server_id", "TEXT NOT NULL DEFAULT ''")      // BattleMetrics server id (optional)
	addColumnIfMissing(db, "servers", "auto_forward", "INTEGER NOT NULL DEFAULT 1")    // open firewall ports on start (UPnP/UniFi)
	addColumnIfMissing(db, "servers", "norn_json", "TEXT NOT NULL DEFAULT ''")         // DayZ Norn loot settings (re-applied after reinstall)
	addColumnIfMissing(db, "servers", "subdomain", "TEXT NOT NULL DEFAULT ''")         // NPM subdomain label/full domain for HTTP apps (empty = off)
	addColumnIfMissing(db, "servers", "npm_host_id", "INTEGER NOT NULL DEFAULT 0")     // NPM proxy-host id we created (0 = none)
	addColumnIfMissing(db, "servers", "cf_hostname", "TEXT NOT NULL DEFAULT ''")       // Cloudflare Tunnel hostname we provisioned (ingress + CNAME)
	addColumnIfMissing(db, "users", "token_version", "INTEGER NOT NULL DEFAULT 0")     // bumped to revoke all of a user's JWT sessions (logout/disable/role/password change)
	addColumnIfMissing(db, "users", "totp_last_counter", "INTEGER NOT NULL DEFAULT 0") // last accepted TOTP step; rejects replay within the validity window
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
