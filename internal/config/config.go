package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Auth     AuthConfig     `yaml:"auth"`
	Docker   DockerConfig   `yaml:"docker"`
	Ports    PortConfig     `yaml:"ports"`
	Admin    AdminConfig    `yaml:"admin"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// DemoMode turns this panel into a public demonstration: every request that
	// would change anything is refused, the console becomes read-only and the AI
	// chat is closed. Off unless explicitly set. See internal/api/demo.go for what
	// this does and does not protect.
	DemoMode bool `yaml:"demo_mode"`
	// DemoLogin is shown on the sign-in page when DemoMode is on: the credentials
	// a visitor should use, in whatever form the operator wants. Free text on
	// purpose — it is a hint, not a credential store, and the account it names is
	// public by definition. Empty means the page says it is a demo and nothing more.
	DemoLogin string `yaml:"demo_login"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type AuthConfig struct {
	SecretKey  string `yaml:"secret_key"`
	SessionTTL int    `yaml:"session_ttl_hours"`
}

type DockerConfig struct {
	Socket string `yaml:"socket"`
}

type PortConfig struct {
	RangeMin int `yaml:"range_min"`
	RangeMax int `yaml:"range_max"`
}

type AdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"` // plaintext only on first boot, then cleared
}

func defaults() Config {
	return Config{
		Server:   ServerConfig{Host: "0.0.0.0", Port: 8080},
		Database: DatabaseConfig{Path: "/var/lib/yggdrasil/yggdrasil.db"},
		Auth:     AuthConfig{SessionTTL: 168},
		Docker:   DockerConfig{Socket: "unix:///var/run/docker.sock"},
		Ports:    PortConfig{RangeMin: 25000, RangeMax: 30000},
	}
}

func Load(path string) (*Config, error) {
	cfg := defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", cfg.Server.Port)
	}
	if cfg.Ports.RangeMin >= cfg.Ports.RangeMax {
		return fmt.Errorf("port range invalid: min %d >= max %d", cfg.Ports.RangeMin, cfg.Ports.RangeMax)
	}
	return nil
}

func WriteExample(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	cfg := defaults()
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
