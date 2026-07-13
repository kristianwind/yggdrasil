package gameskill

import (
	"fmt"
	"path/filepath"
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

type Startup struct {
	Command string `yaml:"command"    json:"command"`
	// Exec is a raw argv (no shell). Use it for shell-less images (distroless /
	// ko-built, e.g. headscale, portainer) or to pass arguments to the image's own
	// ENTRYPOINT (with keep_entrypoint). When set it takes precedence over Command,
	// and each element is {{TEMPLATED}}. Command (run via /bin/sh -c) is the default.
	Exec      []string `yaml:"exec"       json:"exec,omitempty"`
	DoneRegex string   `yaml:"done_regex" json:"done_regex,omitempty"`
	Stop      string   `yaml:"stop"       json:"stop,omitempty"`
}

type Query struct {
	Type string `yaml:"type" json:"type"`
	Port string `yaml:"port" json:"port,omitempty"`
}

type RCON struct {
	Enabled     bool   `yaml:"enabled"      json:"enabled"`
	Type        string `yaml:"type"         json:"type,omitempty"`
	PortVar     string `yaml:"port_var"     json:"port_var,omitempty"`
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
	// Exact-match denies (but allow subpaths, e.g. /etc/letsencrypt for NPM).
	for _, deny := range []string{"/", "/etc", "/var", "/var/run", "/run", "/home"} {
		if clean == deny {
			return fmt.Errorf("docker.extra_volumes: %q shadows a sensitive container path", p)
		}
	}
	// Prefix denies: shadowing system binaries / kernel pseudo-fs is never legitimate.
	for _, deny := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/proc", "/sys", "/dev", "/boot", "/root"} {
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
