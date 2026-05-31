// Package rcon provides remote-console clients for the protocols Yggdrasil's
// gameskills use: Source RCON (Minecraft Java and Source games), Rust WebSocket
// RCON, and BattlEye BERcon (DayZ). A single Client interface lets schedules and
// the console UI drive any game uniformly.
package rcon

import (
	"fmt"
	"time"
)

// Client is a connected remote console. Execute sends a command and returns the
// server's textual response. Close releases the connection.
type Client interface {
	Execute(command string) (string, error)
	Close() error
}

// Config describes how to reach a server's console.
type Config struct {
	Type     string // "minecraft" | "source" | "rust-websocket" | "battleye"
	Host     string
	Port     int
	Password string
	Timeout  time.Duration
}

// Dial connects using the protocol named by cfg.Type.
func Dial(cfg Config) (Client, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	switch cfg.Type {
	case "minecraft", "source", "":
		return dialSource(cfg)
	case "rust-websocket":
		return dialRustWS(cfg)
	case "battleye":
		return dialBattlEye(cfg)
	default:
		return nil, fmt.Errorf("unsupported rcon type %q", cfg.Type)
	}
}
