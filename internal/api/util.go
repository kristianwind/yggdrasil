package api

import (
	"log"
	"runtime/debug"
	"time"

	"github.com/gorilla/websocket"
)

// wsKeepalive pings the WebSocket every 30s until stop is closed. Server→client
// pings keep reverse proxies (NGINX Proxy Manager, etc.) from closing an idle
// console/log stream on their read timeout. WriteControl is safe to call
// concurrently with the handler's normal writes.
func wsKeepalive(conn *websocket.Conn, stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		}
	}
}

// recoverLog recovers a panic in a background goroutine and logs it, so one
// failed operation (install/backup/schedule) can't take down the whole panel
// (which would surface to clients as "Failed to fetch").
func recoverLog(what string) {
	if r := recover(); r != nil {
		log.Printf("recovered panic in %s: %v\n%s", what, r, debug.Stack())
	}
}
