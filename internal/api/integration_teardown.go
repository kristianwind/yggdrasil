package api

import (
	"context"
	"log"
)

// Turning an integration off used to leave everything it had opened in place.
//
// Every per-server removal path begins by checking its own enable flag and
// returning when it is off -- sensible on its own, and it means the moment the
// setting is cleared the panel loses the ability to undo its own work. Enable
// UPnP, start three servers, disable UPnP, stop the servers: the three mappings
// stay on the router with a permanent lease, and the settings page says "off"
// while the router says "open". The same shape for UniFi rules, NPM proxy hosts,
// and Cloudflare ingress rules with their DNS records.
//
// So the teardown runs BEFORE the setting is written, which is the whole trick.
// Call it with the value as it is now and the value about to be saved.
func (s *Server) closeWhatIntegrationOpened(ctx context.Context, name string, was, now bool, remove func(string)) {
	if !was || now {
		return // not a transition from on to off
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM servers")
	if err != nil {
		log.Printf("%s: could not list servers to close what it opened: %v", name, err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	// Collected first, then removed: the remove paths write to the same database
	// and this connection is single-use (SetMaxOpenConns(1)), so removing inside
	// the loop deadlocks against the cursor still being open.
	for _, id := range ids {
		remove(id)
	}
	if len(ids) > 0 {
		log.Printf("%s: disabled — closed what it had opened for %d server(s)", name, len(ids))
	}
}
