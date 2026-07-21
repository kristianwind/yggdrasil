// Package query implements the read-only status protocols Yggdrasil uses to show
// player counts and liveness on the dashboard: Source A2S (Steam games),
// Minecraft Java Server List Ping, and Minecraft Bedrock unconnected ping.
package query

import (
	"fmt"
	"time"
)

// Status is the normalized result returned by every query protocol.
type Status struct {
	Online     bool   `json:"online"`
	Name       string `json:"name,omitempty"`
	Map        string `json:"map,omitempty"`
	Version    string `json:"version,omitempty"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"max_players"`
}

// Query dispatches to the protocol named by typ ("a2s", "minecraft",
// "minecraft-bedrock").
func Query(typ, host string, port int, timeout time.Duration) (*Status, error) {
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	switch typ {
	case "a2s", "source":
		return queryA2S(host, port, timeout)
	case "minecraft", "minecraft-java":
		return queryMinecraftJava(host, port, timeout)
	case "minecraft-bedrock":
		return queryBedrock(host, port, timeout)
	default:
		return nil, fmt.Errorf("unsupported query type %q", typ)
	}
}

// QueryPlayers returns the connected players' names for protocols that expose them
// (currently A2S / Source games, e.g. DayZ). Minecraft's Server List Ping and
// Bedrock ping only carry counts, so they return an error and callers fall back to
// the count from Query.
func QueryPlayers(typ, host string, port int, timeout time.Duration) ([]string, error) {
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	switch typ {
	case "a2s", "source":
		return queryA2SPlayers(host, port, timeout)
	default:
		return nil, fmt.Errorf("player names not available for %q", typ)
	}
}
