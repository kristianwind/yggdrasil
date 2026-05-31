package gameskill

import (
	"fmt"
	"strings"

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
}

type Install struct {
	Image  string `yaml:"image"  json:"image"`
	Script string `yaml:"script" json:"script"`
}

type Startup struct {
	Command   string `yaml:"command"    json:"command"`
	DoneRegex string `yaml:"done_regex" json:"done_regex,omitempty"`
	Stop      string `yaml:"stop"       json:"stop,omitempty"`
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
	Antixray            *AntixrayConfig `yaml:"antixray"              json:"antixray,omitempty"`
	PluginsRecommended  []string        `yaml:"plugins_recommended"   json:"plugins_recommended,omitempty"`
	BattlEye            *BattlEyeConfig `yaml:"battleye"              json:"battleye,omitempty"`
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
	if gs.Startup.Command == "" {
		return fmt.Errorf("gameskill.startup.command is required")
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
