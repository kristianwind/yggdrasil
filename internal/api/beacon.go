package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Beacon: a strictly voluntary "I'm running Yggdrasil Panel" ping, so the project
// can get a rough sense of how many installs are out there. It is OFF by default
// and, when on, sends EXACTLY two fields — a random anonymous instance id and the
// panel version — and nothing else. No IP is stored, no server names, no user
// data, no config. This is a deliberate, opt-in exception to the panel's otherwise
// no-phone-home stance; the UI shows the literal payload so there are no surprises.
//
// The same binary can also BE the collector (receiver, also off by default): the
// maintainer enables the receiver on one public instance and points installs at
// it. Counting is just DISTINCT instance ids over a recent window.

const (
	// The official collector. Overridable per-install via the beacon_url setting.
	defaultBeaconURL = "https://beacon.yggdrasilpanel.com/api/beacon"
	beaconInterval   = 24 * time.Hour
	beaconMaxIDLen   = 64
	beaconMaxVerLen  = 32
)

// beaconPayload is the ENTIRE contents of a beacon ping. Keep it this small — the
// UI promises the user nothing else is sent.
type beaconPayload struct {
	InstanceID string `json:"instance_id"`
	Version    string `json:"version"`
}

// beaconInstanceID returns this panel's stable anonymous id, generating and
// persisting a random one on first use. It identifies nothing — it just lets the
// collector de-duplicate repeat pings into a unique-install count.
func (s *Server) beaconInstanceID() string {
	id := s.getSetting(context.Background(), "beacon_instance_id")
	if id == "" {
		id = uuid.NewString()
		s.setSetting(context.Background(), "beacon_instance_id", id)
	}
	return id
}

func (s *Server) beaconURL() string {
	return firstNonEmpty(s.getSetting(context.Background(), "beacon_url"), defaultBeaconURL)
}

// startBeaconLoop periodically sends a beacon when the user has opted in. Like the
// ops-digest loop it wakes often but acts at most once per day (with catch-up), so
// a panel that isn't up 24/7 still pings roughly daily.
func (s *Server) startBeaconLoop() {
	go func() {
		defer recoverLog("beaconLoop")
		time.Sleep(30 * time.Second) // let startup settle before the first check
		s.maybeSendBeacon()
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.maybeSendBeacon()
		}
	}()
}

func (s *Server) maybeSendBeacon() {
	defer recoverLog("maybeSendBeacon")
	ctx := context.Background()
	if s.getSetting(ctx, "beacon_enabled") != "1" {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	if s.getSetting(ctx, "beacon_last_day") == today {
		return // already pinged today
	}
	if err := s.sendBeacon(ctx); err != nil {
		// Keep the reason where the admin will see it, and log it once per attempt
		// rather than staying quiet.
		s.setSetting(ctx, "beacon_last_error", err.Error())
		s.setSetting(ctx, "beacon_last_error_at", time.Now().UTC().Format(time.RFC3339))
		log.Printf("beacon: ping failed: %v", err)
		return
	}
	s.setSetting(ctx, "beacon_last_day", today)
	s.setSetting(ctx, "beacon_last_error", "")
	s.setSetting(ctx, "beacon_last_error_at", "")
}

// sendBeacon POSTs the two-field payload to the collector, and says why if it
// couldn't.
//
// It used to return a bare bool and drop the reason on the floor. A beacon that
// can't reach its collector then failed in complete silence — no log, no error
// stored, nothing in the UI — and retried every 30 minutes forever. The only way
// to notice was to count the installs on the collector and find one missing,
// which is exactly how this was found. Whatever the collector URL happens to be,
// a ping that never lands has to be visible.
func (s *Server) sendBeacon(ctx context.Context) error {
	body, _ := json.Marshal(beaconPayload{InstanceID: s.beaconInstanceID(), Version: s.version})
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	url := s.beaconURL()
	req, err := http.NewRequestWithContext(c, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("bad collector URL %q: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A 404 here is the classic one: the URL resolves but nothing is routed to
		// a receiver behind it, or the receiver is switched off.
		return fmt.Errorf("%s returned %s — check the collector URL is routed to a panel with the receiver on",
			url, resp.Status)
	}
	return nil
}

// --- Receiver (collector) side ---

func (s *Server) beaconReceiverEnabled(ctx context.Context) bool {
	return s.getSetting(ctx, "beacon_receiver_enabled") == "1"
}

// handleBeaconPing records an incoming beacon (public; 404 when the receiver is
// off so the endpoint isn't advertised). Stores only the anonymous id + version.
func (s *Server) handleBeaconPing(w http.ResponseWriter, r *http.Request) {
	if !s.beaconReceiverEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	var p beaconPayload
	if err := decodeJSON(r, &p); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	p.InstanceID = strings.TrimSpace(p.InstanceID)
	p.Version = strings.TrimSpace(p.Version)
	if p.InstanceID == "" || len(p.InstanceID) > beaconMaxIDLen || len(p.Version) > beaconMaxVerLen {
		jsonError(w, "invalid payload", http.StatusBadRequest)
		return
	}
	// Upsert: first ping inserts, repeats bump last_seen/count and refresh version.
	// Deliberately store NO IP address or any other request metadata.
	s.db.ExecContext(r.Context(), `
		INSERT INTO beacon_pings (instance_id, version, first_seen, last_seen, ping_count)
		VALUES (?, ?, datetime('now'), datetime('now'), 1)
		ON CONFLICT(instance_id) DO UPDATE SET
			version=excluded.version, last_seen=datetime('now'), ping_count=ping_count+1`,
		p.InstanceID, p.Version)
	jsonOK(w, map[string]string{"status": "ok"})
}

type beaconStats struct {
	Total    int              `json:"total"`     // distinct instances ever seen
	Active7d int              `json:"active_7d"` // pinged in the last 7 days
	Active30 int              `json:"active_30d"`
	Versions []beaconVerCount `json:"versions"` // active-30d breakdown by version
}

type beaconVerCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

// handleBeaconStats returns collected install counts (admin).
func (s *Server) handleBeaconStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var st beaconStats
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM beacon_pings").Scan(&st.Total)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM beacon_pings WHERE last_seen >= datetime('now','-7 days')").Scan(&st.Active7d)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM beacon_pings WHERE last_seen >= datetime('now','-30 days')").Scan(&st.Active30)
	st.Versions = []beaconVerCount{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(version,''),'unknown') v, COUNT(*) c
		FROM beacon_pings WHERE last_seen >= datetime('now','-30 days')
		GROUP BY v ORDER BY c DESC, v`)
	if err == nil {
		for rows.Next() {
			var vc beaconVerCount
			if rows.Scan(&vc.Version, &vc.Count) == nil {
				st.Versions = append(st.Versions, vc)
			}
		}
		rows.Close()
	}
	jsonOK(w, st)
}

// --- Settings (admin) ---

func (s *Server) handleGetBeaconSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	jsonOK(w, map[string]any{
		"enabled":          s.getSetting(ctx, "beacon_enabled") == "1",
		"url":              s.beaconURL(),
		"instance_id":      s.beaconInstanceID(),
		"version":          s.version,
		"receiver_enabled": s.beaconReceiverEnabled(ctx),
		"last_sent":        s.getSetting(ctx, "beacon_last_day"), // YYYY-MM-DD of the last successful ping ("" = never)
		"last_error":       s.getSetting(ctx, "beacon_last_error"),
		"last_error_at":    s.getSetting(ctx, "beacon_last_error_at"),
	})
}

// handleTestBeacon sends a one-off ping to a collector URL and reports whether it
// was accepted — so an admin can verify their collector is reachable before (or
// after) enabling the beacon. Uses the posted URL if given, else the saved one.
func (s *Server) handleTestBeacon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	decodeJSON(r, &req)
	target := strings.TrimSpace(req.URL)
	if target == "" {
		target = s.beaconURL()
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		jsonError(w, "collector URL must start with http:// or https://", http.StatusBadRequest)
		return
	}
	body, _ := json.Marshal(beaconPayload{InstanceID: s.beaconInstanceID(), Version: s.version})
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(body))
	if err != nil {
		jsonError(w, "bad URL", http.StatusBadRequest)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		jsonOK(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	out := map[string]any{"ok": ok, "status": resp.StatusCode}
	if !ok {
		if resp.StatusCode == http.StatusNotFound {
			out["error"] = "404 — nothing is collecting at that URL (is the collector enabled + routed there?)"
		} else {
			out["error"] = fmt.Sprintf("collector returned HTTP %d", resp.StatusCode)
		}
	}
	jsonOK(w, out)
}

func (s *Server) handleSetBeaconSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled         *bool   `json:"enabled"`
		URL             *string `json:"url"`
		ReceiverEnabled *bool   `json:"receiver_enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if req.Enabled != nil {
		s.setSetting(ctx, "beacon_enabled", boolStr(*req.Enabled))
		if *req.Enabled {
			s.setSetting(ctx, "beacon_last_day", "") // let it ping right away on opt-in
			go s.maybeSendBeacon()
		}
	}
	if req.URL != nil {
		u := strings.TrimSpace(*req.URL)
		// Only accept a plausible http(s) collector URL; empty falls back to the default.
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			jsonError(w, "collector URL must start with http:// or https://", http.StatusBadRequest)
			return
		}
		s.setSetting(ctx, "beacon_url", u)
	}
	if req.ReceiverEnabled != nil {
		s.setSetting(ctx, "beacon_receiver_enabled", boolStr(*req.ReceiverEnabled))
	}
	s.auditLog(r, "settings.beacon", "beacon", map[string]any{
		"enabled":          req.Enabled != nil && *req.Enabled,
		"receiver_enabled": req.ReceiverEnabled != nil && *req.ReceiverEnabled,
	})
	s.handleGetBeaconSettings(w, r)
}
