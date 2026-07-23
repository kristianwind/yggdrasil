package gameskill

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Gameskill is the parsed, validated form of a gameskill YAML file.
type Gameskill struct {
	ID          string     `yaml:"id"          json:"id"`
	Name        string     `yaml:"name"        json:"name"`
	Category    string     `yaml:"category"    json:"category"`
	Description string     `yaml:"description" json:"description"`
	Author      string     `yaml:"author"      json:"author"`
	Version     int        `yaml:"version"     json:"version"`
	Icon        string     `yaml:"icon"        json:"icon"`
	Docker      Docker     `yaml:"docker"      json:"docker"`
	Variables   []Variable `yaml:"variables"   json:"variables"`
	Install     *Install   `yaml:"install"     json:"install,omitempty"`
	Import      *Importer  `yaml:"import,omitempty" json:"import,omitempty"`
	Startup     Startup    `yaml:"startup"     json:"startup"`
	Query       *Query     `yaml:"query"       json:"query,omitempty"`
	RCON        *RCON      `yaml:"rcon"        json:"rcon,omitempty"`
	Steam       *Steam     `yaml:"steam"       json:"steam,omitempty"`
	ConfigFiles []string   `yaml:"config_files" json:"config_files,omitempty"`
	Ports       []Port     `yaml:"ports"       json:"ports"`
	Anticheat   *Anticheat `yaml:"anticheat"   json:"anticheat,omitempty"`
	Bans        *Bans      `yaml:"bans"        json:"bans,omitempty"`
	Backup      *Backup    `yaml:"backup"      json:"backup,omitempty"`
	Wipe        *Wipe      `yaml:"wipe"        json:"wipe,omitempty"`
	Restart     *Restart   `yaml:"restart"     json:"restart,omitempty"`
	Players     *Players   `yaml:"players"     json:"players,omitempty"`
	AdminLog    *AdminLog  `yaml:"admin_log"   json:"admin_log,omitempty"`
	Services    []Service  `yaml:"services,omitempty" json:"services,omitempty"`
	Watchers    []Watcher  `yaml:"watchers,omitempty" json:"watchers,omitempty"`
}

// Service is a sidecar container an app depends on — a database, a cache, a worker
// — that the panel runs alongside the main container on a private per-server bridge
// network. The main container (and other sidecars) reach it by Name, which is its
// DNS alias on that network: an app sets DB_HOST=db when its rune declares a service
// named "db". Sidecars publish no host ports (they're internal), run their own image
// entrypoint, and each get their own persisted directory under the server's data dir.
// A rune with services turns one panel "server" into a small app stack (e.g. Immich =
// server + machine-learning + postgres + redis, Paperless = app + redis).
type Service struct {
	Name     string            `yaml:"name"     json:"name"` // DNS alias on the stack network, e.g. "db", "redis"
	Image    string            `yaml:"image"    json:"image"`
	Env      map[string]string `yaml:"env,omitempty"      json:"env,omitempty"`        // values may reference {{VARS}}
	DataPath string            `yaml:"data_path,omitempty" json:"data_path,omitempty"` // persisted mount inside the sidecar
	Command  []string          `yaml:"command,omitempty"  json:"command,omitempty"`    // optional command override (argv)
}

// Watcher declares a default Kvasir log-watcher the rune ships with — the app
// author's knowledge of what its log looks like when something is wrong ("PHP
// Fatal error", "Can't keep up!", a failed-login burst). Seeded per server at
// create/install as an editable rule (never silently re-enabled once the user
// touches it), so watching works out of the box instead of requiring every
// admin to invent the right regex themselves.
type Watcher struct {
	Name       string `yaml:"name"                  json:"name"`
	Pattern    string `yaml:"pattern"               json:"pattern"`               // regex matched per log line
	Threshold  int    `yaml:"threshold,omitempty"   json:"threshold,omitempty"`   // N matches within the window (default 1)
	WindowSecs int    `yaml:"window_secs,omitempty" json:"window_secs,omitempty"` // default 60, clamped to 3600
	Action     string `yaml:"action,omitempty"      json:"action,omitempty"`      // notify (default) | kvasir
}

// AdminLog declares how to surface a game's admin/activity log as a parsed feed
// (joins, disconnects, deaths, kills, ...) — the deterministic session-history
// base that the later AI "what happened while I was away" digest reads. Path is
// a glob relative to the server's data dir; the most recently modified match is
// tailed. Each Events entry classifies a log line via a regexp with an optional
// (?P<name>...) player-name group; TimeRegex optionally pulls a leading timestamp
// (named group `time`, else the whole match). It's the first game to fill this in
// (DayZ .ADM); the generic feed lights up for any rune that declares it.
type AdminLog struct {
	Path      string         `yaml:"path"                json:"path"`
	TimeRegex string         `yaml:"time_regex,omitempty" json:"time_regex,omitempty"`
	Events    []AdminLogRule `yaml:"events"              json:"events"`
}

type AdminLogRule struct {
	Type  string `yaml:"type"  json:"type"`  // classification, e.g. join | leave | death | kill
	Regex string `yaml:"regex" json:"regex"` // matched per line; optional (?P<name>...) player group
}

// Players declares how to list and moderate connected players over the game's
// RCON, so the panel gets a live Players tab + kick/broadcast/lock. ListCommand's
// textual response is parsed line-by-line with PlayerRegex, a regexp with named
// capture groups — `name` is required, `id`/`ping`/`guid`/`ip` are optional and
// surfaced when present. The action commands are templated: KickCommand with
// {{id}}/{{name}}/{{reason}}, BroadcastCommand with {{message}}; lock/unlock take
// no template. Any action left empty is simply not offered in the UI, so a rune
// can declare read-only listing without kick/lock. Requires an rcon: block.
type Players struct {
	ListCommand      string `yaml:"list_command"      json:"list_command"`
	PlayerRegex      string `yaml:"player_regex"      json:"player_regex"`
	KickCommand      string `yaml:"kick_command,omitempty"      json:"kick_command,omitempty"`
	BroadcastCommand string `yaml:"broadcast_command,omitempty" json:"broadcast_command,omitempty"`
	LockCommand      string `yaml:"lock_command,omitempty"      json:"lock_command,omitempty"`
	UnlockCommand    string `yaml:"unlock_command,omitempty"    json:"unlock_command,omitempty"`
}

// Wipe declares what "reset the world / persistence" means for this rune: the
// data-dir globs to delete when wiping (jailed to the server's data dir). A rune
// with a wipe block gets a manual Wipe button + a schedulable wipe action —
// e.g. DayZ "mpmissions/*/storage_*", a Minecraft "world" folder. Paths are
// relative to the server's data dir; "*" globs are allowed, ".." is not.
type Wipe struct {
	Paths       []string `yaml:"paths" json:"paths"`
	BackupFirst bool     `yaml:"backup_first,omitempty" json:"backup_first,omitempty"`
}

// Restart declares in-game warnings broadcast before a "safe restart": a
// countdown from the largest `at` down to zero, each entry sending `msg` (a full
// console/RCON broadcast command) that many minutes/seconds before the restart.
// Enables warned restarts (manual button + scheduled) so players get notice.
type Restart struct {
	Warnings []RestartWarning `yaml:"warnings" json:"warnings,omitempty"`
}

type RestartWarning struct {
	At  string `yaml:"at" json:"at"`   // time before restart, e.g. "15m", "60s"
	Msg string `yaml:"msg" json:"msg"` // full broadcast command, e.g. "say Restart in 15 min"
}

// Bans declares how to ban/unban a player via the game's console/RCON. Commands
// are templated with {{player}} and {{reason}}. Omitted when the game has no
// console ban (e.g. vanilla Bedrock uses an allowlist instead).
type Bans struct {
	BanCommand   string `yaml:"ban_command"   json:"ban_command,omitempty"`
	UnbanCommand string `yaml:"unban_command" json:"unban_command,omitempty"`
}

type Docker struct {
	Image string `yaml:"image" json:"image"`
	// DataPath is where the persistent volume mounts inside the container. Games
	// use the default /data; many apps store elsewhere (WordPress /var/www/html,
	// Pi-hole /etc/pihole, Uptime Kuma /app/data). Empty = /data.
	DataPath string `yaml:"data_path,omitempty" json:"data_path,omitempty"`
	// User overrides the runtime uid:gid. Empty = the panel's user (keeps files
	// editable). Use "0:0" for images that must start as root to drop to PUID/PGID
	// (e.g. linuxserver.io). Install always runs as root regardless.
	User string `yaml:"user,omitempty" json:"user,omitempty"`
	// KeepEntrypoint runs the image's own ENTRYPOINT (instead of clearing it) so an
	// off-the-shelf app image behaves like a plain `docker run`. With it set, the
	// startup command is optional (empty = the image's default CMD).
	KeepEntrypoint bool `yaml:"keep_entrypoint,omitempty" json:"keep_entrypoint,omitempty"`
	// ExtraVolumes are additional container paths that each get their own persisted
	// directory (a subdir of the server's data dir) — for images that require more
	// than one mount, e.g. Nginx Proxy Manager (/data + /etc/letsencrypt).
	ExtraVolumes []string `yaml:"extra_volumes,omitempty" json:"extra_volumes,omitempty"`
	// Capabilities are Linux capabilities to add (cap_add), e.g. ["NET_ADMIN"] for a
	// Tailscale subnet router. Keep minimal — it widens a container's blast radius.
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	// Devices are host devices to expose, e.g. ["/dev/net/tun"] (kernel networking).
	// Format: "host[:container[:perms]]" (container defaults to host, perms "rwm").
	Devices []string `yaml:"devices,omitempty" json:"devices,omitempty"`
	// Sysctls are kernel params set in the container's namespace, e.g.
	// {"net.ipv4.ip_forward":"1"} for subnet routing / exit nodes.
	Sysctls map[string]string `yaml:"sysctls,omitempty" json:"sysctls,omitempty"`
	// Env is fixed container environment the rune sets that isn't a user Variable —
	// e.g. an app stack's connection strings (REDIS host, DB host) that are fixed by
	// the rune's own service names. Values may reference {{VARS}}. User variables win
	// on a key clash.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

type Variable struct {
	Key      string      `yaml:"key"      json:"key"`
	Name     string      `yaml:"name"     json:"name"`
	Type     string      `yaml:"type"     json:"type"`
	Options  []string    `yaml:"options"  json:"options,omitempty"`
	Default  interface{} `yaml:"default"  json:"default,omitempty"`
	Required bool        `yaml:"required" json:"required,omitempty"`
	Min      *int        `yaml:"min"      json:"min,omitempty"`
	Max      *int        `yaml:"max"      json:"max,omitempty"`
	// Secret marks a value the UI should render as a password field (masked, with
	// the generate/copy controls) — e.g. an API key. The VarForm also auto-detects
	// password/secret/token by name; this is the explicit opt-in for others.
	Secret bool `yaml:"secret" json:"secret,omitempty"`
}

type Install struct {
	Image  string `yaml:"image"  json:"image"`
	Script string `yaml:"script" json:"script"`
}

// Importer declares how to bring an EXISTING deployment of this app into a
// Yggdrasil server — the onboarding counterpart to migration (which moves
// between panels). The admin uploads the app's own data (a site archive, a
// database dump) and the panel runs the declared steps against the server's
// data dir and, for app-stacks, its database sidecar. Everything runs in
// one-shot containers, streamed to the install log; the server is stopped
// during the import and started after. First mover: WordPress (webroot + SQL).
type Importer struct {
	Inputs []ImportInput `yaml:"inputs" json:"inputs"`
	Steps  []ImportStep  `yaml:"steps"  json:"steps"`
}

type ImportInput struct {
	Key      string `yaml:"key"      json:"key"`                        // form field + step reference
	Label    string `yaml:"label"    json:"label"`                      // shown in the UI
	Accept   string `yaml:"accept,omitempty"   json:"accept,omitempty"` // e.g. ".sql,.sql.gz"
	Optional bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// ImportStep is one action. Exactly one verb is set per step:
//
//	unpack:    extract an archive input into the data dir (at `to`, default ".")
//	db_import: pipe a .sql/.sql.gz input into a database sidecar via a one-shot
//	           client container on the stack network (image + command below)
//	script:    run a shell snippet in the app's own image against the data dir
//	           (e.g. rewrite wp-config.php) — {{VARS}} + $YGG_INPUT_<KEY> paths
type ImportStep struct {
	Unpack   string    `yaml:"unpack,omitempty"    json:"unpack,omitempty"` // input key
	To       string    `yaml:"to,omitempty"        json:"to,omitempty"`     // unpack destination within the data dir
	DBImport *DBImport `yaml:"db_import,omitempty" json:"db_import,omitempty"`
	Script   string    `yaml:"script,omitempty"    json:"script,omitempty"`
}

type DBImport struct {
	Input   string `yaml:"input"   json:"input"`   // the dump input key
	Service string `yaml:"service" json:"service"` // stack service (sidecar) name, e.g. "db"
	Image   string `yaml:"image"   json:"image"`   // client image, e.g. "mariadb:11" or "postgres:16"
	Command string `yaml:"command" json:"command"` // shell run inside it; {{VARS}} templated, dump on stdin
}

type Startup struct {
	Command string `yaml:"command"    json:"command"`
	// Exec is a raw argv (no shell). Use it for shell-less images (distroless /
	// ko-built, e.g. headscale, portainer) or to pass arguments to the image's own
	// ENTRYPOINT (with keep_entrypoint). When set it takes precedence over Command,
	// and each element is {{TEMPLATED}}. Command (run via /bin/sh -c) is the default.
	Exec      []string `yaml:"exec"       json:"exec,omitempty"`
	DoneRegex string   `yaml:"done_regex" json:"done_regex,omitempty"`
	// Stop is a console command sent before the container is signalled, to shut the
	// game down cleanly (e.g. Minecraft "stop").
	Stop string `yaml:"stop" json:"stop,omitempty"`
	// SaveCommand is a console command sent (and given a moment to run) *before* the
	// stop command, to flush in-memory state to disk on a restart/stop — e.g.
	// Minecraft "save-all". Games with no such command (DayZ persists on its own CE
	// timer) leave this empty and instead rely on a longer StopTimeout.
	SaveCommand string `yaml:"save_command,omitempty" json:"save_command,omitempty"`
	// StopTimeout is the SIGTERM→SIGKILL grace period (seconds) for the graceful
	// stop. 0 = the panel default. Games that flush persistence on shutdown (DayZ
	// writes its whole Central Economy state) need well more than a couple of
	// seconds, or they get SIGKILL'd mid-save. Capped to a sane maximum.
	StopTimeout int `yaml:"stop_timeout,omitempty" json:"stop_timeout,omitempty"`
}

type Query struct {
	Type string `yaml:"type" json:"type"`
	// No port field: the port to query comes from the `ports` block — a mapping
	// named "query" if the rune declares one, else "game". That mapping is what
	// gets allocated and published, so a second way to say it could only ever
	// disagree with reality.
}

type RCON struct {
	Enabled bool   `yaml:"enabled"      json:"enabled"`
	Type    string `yaml:"type"         json:"type,omitempty"`
	// PasswordVar names the variable holding the RCON password. It also marks that
	// variable as a secret, so it is encrypted at rest and masked in the API.
	//
	// There is no port_var: like the query port, the RCON port comes from the
	// `ports` block — a mapping named "rcon", falling back to "game" for the
	// protocols that share it (BattlEye).
	PasswordVar string `yaml:"password_var" json:"password_var,omitempty"`
}

type Steam struct {
	AppID     int  `yaml:"app_id"    json:"app_id"`
	Anonymous bool `yaml:"anonymous" json:"anonymous"`
}

type Port struct {
	Name     string `yaml:"name"     json:"name"`
	Default  int    `yaml:"default"  json:"default"`
	Protocol string `yaml:"protocol" json:"protocol"`
}

type Anticheat struct {
	Antixray           *AntixrayConfig `yaml:"antixray"              json:"antixray,omitempty"`
	PluginsRecommended []string        `yaml:"plugins_recommended"   json:"plugins_recommended,omitempty"`
	BattlEye           *BattlEyeConfig `yaml:"battleye"              json:"battleye,omitempty"`
}

type AntixrayConfig struct {
	Supported  bool   `yaml:"supported"    json:"supported"`
	ConfigHint string `yaml:"config_hint"  json:"config_hint,omitempty"`
}

type BattlEyeConfig struct {
	Supported  bool   `yaml:"supported"    json:"supported"`
	ConfigHint string `yaml:"config_hint"  json:"config_hint,omitempty"`
}

type Backup struct {
	Include []string `yaml:"include" json:"include"`
}

// file wrapper for top-level "gameskill:" key
type fileWrapper struct {
	Gameskill Gameskill `yaml:"gameskill"`
}

func Parse(data []byte) (*Gameskill, error) {
	var wrapper fileWrapper
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("yaml parse: %w", err)
	}
	gs := &wrapper.Gameskill
	if err := validate(gs); err != nil {
		return nil, err
	}
	return gs, nil
}

// ToYAML serializes a gameskill back to the wrapped YAML document form.
func ToYAML(gs *Gameskill) ([]byte, error) {
	return yaml.Marshal(fileWrapper{Gameskill: *gs})
}

func validate(gs *Gameskill) error {
	if gs.ID == "" {
		return fmt.Errorf("gameskill.id is required")
	}
	if gs.Name == "" {
		return fmt.Errorf("gameskill.name is required")
	}
	if gs.Docker.Image == "" {
		return fmt.Errorf("gameskill.docker.image is required")
	}
	if gs.Startup.Command == "" && len(gs.Startup.Exec) == 0 && !gs.Docker.KeepEntrypoint {
		return fmt.Errorf("gameskill.startup.command (or .exec) is required (unless docker.keep_entrypoint is set)")
	}
	if gs.Startup.StopTimeout < 0 {
		return fmt.Errorf("gameskill.startup.stop_timeout must be >= 0")
	}

	validTypes := map[string]bool{"string": true, "int": true, "bool": true, "select": true}
	for _, v := range gs.Variables {
		if v.Key == "" {
			return fmt.Errorf("variable missing key")
		}
		if !validTypes[v.Type] {
			return fmt.Errorf("variable %q has unknown type %q", v.Key, v.Type)
		}
		if v.Type == "select" && len(v.Options) == 0 {
			return fmt.Errorf("variable %q is type select but has no options", v.Key)
		}
	}

	for _, p := range gs.Ports {
		if p.Name == "" {
			return fmt.Errorf("port entry missing name")
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("port %q has invalid protocol %q", p.Name, p.Protocol)
		}
	}

	// config_files are paths inside the server's own data directory, and the Files
	// tab turns them into one-click shortcuts. The file API resolves them through
	// safeJoin, which clamps anything escaping the data dir — but a rune asking for
	// /etc/passwd or ../../secrets is a rune bug, and saying so at upload beats
	// silently rewriting it into something that then 404s.
	for _, cf := range gs.ConfigFiles {
		if err := validateConfigFile(cf); err != nil {
			return err
		}
	}

	if gs.Wipe != nil {
		if len(gs.Wipe.Paths) == 0 {
			return fmt.Errorf("gameskill.wipe.paths is required when wipe is set")
		}
		for _, p := range gs.Wipe.Paths {
			p = strings.TrimSpace(p)
			// Reject anything that would escape the data dir or nuke it wholesale.
			if p == "" || p == "/" || p == "." || strings.Contains(p, "..") {
				return fmt.Errorf("gameskill.wipe.paths entry %q is invalid", p)
			}
		}
	}

	if gs.Restart != nil {
		for _, rw := range gs.Restart.Warnings {
			if d, err := time.ParseDuration(strings.TrimSpace(rw.At)); err != nil || d <= 0 {
				return fmt.Errorf("gameskill.restart.warnings has invalid 'at' %q (use e.g. 15m, 60s)", rw.At)
			}
		}
	}

	if gs.Players != nil {
		if strings.TrimSpace(gs.Players.ListCommand) == "" {
			return fmt.Errorf("gameskill.players.list_command is required when players is set")
		}
		if gs.RCON == nil || !gs.RCON.Enabled {
			return fmt.Errorf("gameskill.players requires an enabled rcon: block")
		}
		re, err := regexp.Compile(gs.Players.PlayerRegex)
		if err != nil {
			return fmt.Errorf("gameskill.players.player_regex does not compile: %w", err)
		}
		hasName := false
		for _, n := range re.SubexpNames() {
			if n == "name" {
				hasName = true
			}
		}
		if !hasName {
			return fmt.Errorf("gameskill.players.player_regex must have a (?P<name>...) capture group")
		}
	}

	if gs.AdminLog != nil {
		p := strings.TrimSpace(gs.AdminLog.Path)
		if p == "" || strings.Contains(p, "..") {
			return fmt.Errorf("gameskill.admin_log.path is required and must not contain ..")
		}
		if gs.AdminLog.TimeRegex != "" {
			if _, err := regexp.Compile(gs.AdminLog.TimeRegex); err != nil {
				return fmt.Errorf("gameskill.admin_log.time_regex does not compile: %w", err)
			}
		}
		if len(gs.AdminLog.Events) == 0 {
			return fmt.Errorf("gameskill.admin_log.events is required when admin_log is set")
		}
		for _, e := range gs.AdminLog.Events {
			if strings.TrimSpace(e.Type) == "" {
				return fmt.Errorf("gameskill.admin_log.events entry missing type")
			}
			if _, err := regexp.Compile(e.Regex); err != nil {
				return fmt.Errorf("gameskill.admin_log.events %q regex does not compile: %w", e.Type, err)
			}
		}
	}

	if gs.Import != nil {
		if len(gs.Import.Steps) == 0 {
			return fmt.Errorf("gameskill.import.steps is required when import is set")
		}
		keys := map[string]bool{}
		for _, in := range gs.Import.Inputs {
			if strings.TrimSpace(in.Key) == "" {
				return fmt.Errorf("gameskill.import.inputs entry missing key")
			}
			keys[in.Key] = true
		}
		for i, st := range gs.Import.Steps {
			verbs := 0
			if st.Unpack != "" {
				verbs++
				if !keys[st.Unpack] {
					return fmt.Errorf("gameskill.import.steps[%d] unpack references unknown input %q", i, st.Unpack)
				}
				if strings.Contains(st.To, "..") {
					return fmt.Errorf("gameskill.import.steps[%d] unpack 'to' must not contain ..", i)
				}
			}
			if st.DBImport != nil {
				verbs++
				d := st.DBImport
				if !keys[d.Input] {
					return fmt.Errorf("gameskill.import.steps[%d] db_import references unknown input %q", i, d.Input)
				}
				if strings.TrimSpace(d.Service) == "" || strings.TrimSpace(d.Image) == "" || strings.TrimSpace(d.Command) == "" {
					return fmt.Errorf("gameskill.import.steps[%d] db_import needs service, image and command", i)
				}
			}
			if st.Script != "" {
				verbs++
			}
			if verbs != 1 {
				return fmt.Errorf("gameskill.import.steps[%d] must set exactly one of unpack/db_import/script", i)
			}
		}
	}

	for i, w := range gs.Watchers {
		if strings.TrimSpace(w.Name) == "" || strings.TrimSpace(w.Pattern) == "" {
			return fmt.Errorf("gameskill.watchers entry %d needs both name and pattern", i+1)
		}
		if _, err := regexp.Compile(w.Pattern); err != nil {
			return fmt.Errorf("gameskill.watchers %q pattern does not compile: %w", w.Name, err)
		}
		if w.Action != "" && w.Action != "notify" && w.Action != "kvasir" {
			return fmt.Errorf("gameskill.watchers %q action must be notify or kvasir", w.Name)
		}
		if w.Threshold < 0 || w.WindowSecs < 0 || w.WindowSecs > 3600 {
			return fmt.Errorf("gameskill.watchers %q threshold/window_secs out of range (window max 3600)", w.Name)
		}
	}

	// Privilege guardrails. Runes are semi-trusted (imported from GitHub or
	// uploaded), and these fields can escalate a container to host access, so they
	// are restricted to a conservative allowlist — enforced here, which is the
	// single chokepoint for both upload (Parse on POST) and runtime (Parse on load).
	for _, c := range gs.Docker.Capabilities {
		if !allowedCapabilities[strings.ToUpper(strings.TrimSpace(c))] {
			return fmt.Errorf("docker.capabilities: %q is not allowed (permitted: NET_ADMIN, NET_RAW, NET_BIND_SERVICE, SYS_NICE)", c)
		}
	}
	for _, d := range gs.Docker.Devices {
		host := strings.SplitN(strings.TrimSpace(d), ":", 2)[0]
		if !allowedDevices[host] {
			return fmt.Errorf("docker.devices: %q is not allowed (permitted: /dev/net/tun, /dev/dri, /dev/fuse)", d)
		}
	}
	for k := range gs.Docker.Sysctls {
		if !allowedSysctls[k] {
			return fmt.Errorf("docker.sysctls: %q is not allowed (permitted: net.ipv4.ip_forward, net.ipv6.conf.all.forwarding, net.ipv4.conf.all.src_valid_mark)", k)
		}
	}
	for _, vp := range gs.Docker.ExtraVolumes {
		if err := validateExtraVolumeTarget(vp); err != nil {
			return err
		}
	}

	return nil
}

// Allowlists for privilege-bearing Docker fields. Deliberately minimal: only what
// legitimate runes need (e.g. NET_ADMIN + /dev/net/tun for a VPN/subnet router,
// /dev/dri for GPU transcoding). Everything else (SYS_ADMIN, SYS_MODULE, raw block
// devices, arbitrary sysctls, …) is a host-escape risk and is refused.
var allowedCapabilities = map[string]bool{
	"NET_ADMIN": true, "NET_RAW": true, "NET_BIND_SERVICE": true, "SYS_NICE": true,
}
var allowedDevices = map[string]bool{
	"/dev/net/tun": true, "/dev/dri": true, "/dev/fuse": true,
}
var allowedSysctls = map[string]bool{
	"net.ipv4.ip_forward":              true,
	"net.ipv6.conf.all.forwarding":     true,
	"net.ipv4.conf.all.src_valid_mark": true,
}

// validateExtraVolumeTarget rejects mount targets that would shadow sensitive
// container directories (the host source is already confined to the data dir).
func validateExtraVolumeTarget(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("docker.extra_volumes: %q must be an absolute container path", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("docker.extra_volumes: %q must not contain ..", p)
	}
	clean := "/" + strings.Trim(filepath.Clean(p), "/")
	// Exact-match denies (but allow subpaths, e.g. /etc/letsencrypt for NPM). /usr is
	// here (bare /usr would shadow everything) but its data subdirs are allowed below.
	for _, deny := range []string{"/", "/etc", "/var", "/var/run", "/run", "/home", "/usr"} {
		if clean == deny {
			return fmt.Errorf("docker.extra_volumes: %q shadows a sensitive container path", p)
		}
	}
	// Prefix denies: shadowing system binaries / libraries / kernel pseudo-fs is never
	// legitimate. App data dirs under /usr/src or /usr/share are fine (Paperless,
	// Immich and many app images install and store there), so /usr is not blanket-denied
	// — only its executable/library subtrees are. The mount source is always a
	// panel-created empty dir, so this guards against a broken config, not injection.
	for _, deny := range []string{
		"/bin", "/sbin", "/lib", "/lib64", "/proc", "/sys", "/dev", "/boot", "/root",
		"/usr/bin", "/usr/sbin", "/usr/lib", "/usr/lib64", "/usr/libexec",
		"/usr/local/bin", "/usr/local/sbin", "/usr/local/lib",
	} {
		if clean == deny || strings.HasPrefix(clean, deny+"/") {
			return fmt.Errorf("docker.extra_volumes: %q shadows a sensitive container path", p)
		}
	}
	return nil
}

// ApplyTemplate replaces {{KEY}} placeholders with values from env.
func ApplyTemplate(tmpl string, env map[string]string) string {
	result := tmpl
	for k, v := range env {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

// DefaultEnv builds the default env map from gameskill variables.
func DefaultEnv(gs *Gameskill) map[string]string {
	env := make(map[string]string)
	for _, v := range gs.Variables {
		if v.Default != nil {
			env[v.Key] = fmt.Sprintf("%v", v.Default)
		}
	}
	return env
}

// validateConfigFile checks one config_files entry. They name files inside the
// server's data directory, so they must be relative and stay inside it.
func validateConfigFile(p string) error {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return fmt.Errorf("gameskill.config_files has an empty entry")
	}
	if strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("config_files %q must be relative to the server's data directory, not absolute", p)
	}
	if trimmed != path.Clean(trimmed) || strings.HasPrefix(path.Clean(trimmed), "..") {
		return fmt.Errorf("config_files %q must not escape the data directory or contain path traversal", p)
	}
	return nil
}
