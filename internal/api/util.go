package api

import (
	"log"
	"runtime/debug"
)

// recoverLog recovers a panic in a background goroutine and logs it, so one
// failed operation (install/backup/schedule) can't take down the whole panel
// (which would surface to clients as "Failed to fetch").
func recoverLog(what string) {
	if r := recover(); r != nil {
		log.Printf("recovered panic in %s: %v\n%s", what, r, debug.Stack())
	}
}
