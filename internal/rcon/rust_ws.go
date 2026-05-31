package rcon

import (
	"fmt"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Rust uses WebSocket RCON: connect to ws://host:port/PASSWORD and exchange
// JSON command/response frames.
type rustWSClient struct {
	conn    *websocket.Conn
	timeout time.Duration
	id      int32
}

type rustMessage struct {
	Identifier int    `json:"Identifier"`
	Message    string `json:"Message"`
	Name       string `json:"Name,omitempty"`
	Type       string `json:"Type,omitempty"`
}

func dialRustWS(cfg Config) (Client, error) {
	u := url.URL{
		Scheme: "ws",
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   "/" + cfg.Password,
	}
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("rust rcon dial: %w", err)
	}
	return &rustWSClient{conn: conn, timeout: cfg.Timeout}, nil
}

func (c *rustWSClient) Execute(command string) (string, error) {
	id := int(atomic.AddInt32(&c.id, 1))
	req := rustMessage{Identifier: id, Message: command, Name: "Yggdrasil"}
	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	if err := c.conn.WriteJSON(req); err != nil {
		return "", err
	}
	// Read until we see our identifier (Rust also pushes id=0 chat/console lines).
	deadline := time.Now().Add(c.timeout)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		var resp rustMessage
		if err := c.conn.ReadJSON(&resp); err != nil {
			return "", err
		}
		if resp.Identifier == id {
			return resp.Message, nil
		}
	}
	return "", fmt.Errorf("rust rcon: timed out waiting for response")
}

func (c *rustWSClient) Close() error { return c.conn.Close() }
