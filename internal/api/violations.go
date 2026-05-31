package api

import (
	"bufio"
	"context"
	"io"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

// violationRule is a compiled auto-action rule.
type violationRule struct {
	ID          string
	Name        string
	Re          *regexp.Regexp
	Threshold   int
	Window      time.Duration
	Action      string // ban | kick
	ScopeGlobal bool
}

// watcher manager: tails each running server's logs and applies rules.
type violationWatcher struct {
	s       *Server
	mu      sync.Mutex
	active  map[string]context.CancelFunc // serverID -> cancel
	rules   []violationRule
	hits    map[string][]time.Time // key: ruleID|serverID|player -> recent hit times
}

func newViolationWatcher(s *Server) *violationWatcher {
	return &violationWatcher{
		s:      s,
		active: map[string]context.CancelFunc{},
		hits:   map[string][]time.Time{},
	}
}

// Start begins the reconcile loop. It is a no-op if there are no rules, but it
// keeps running so newly added rules take effect.
func (vw *violationWatcher) Start() {
	vw.reloadRules()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			vw.reconcile()
		}
	}()
	vw.reconcile()
}

func (vw *violationWatcher) reloadRules() {
	rows, err := vw.s.db.Query(
		"SELECT id, name, pattern, threshold, window_minutes, action, scope_global FROM violation_rules WHERE enabled=1")
	if err != nil {
		return
	}
	defer rows.Close()
	var rules []violationRule
	for rows.Next() {
		var r violationRule
		var pattern, action string
		var threshold, windowMin, scope int
		if rows.Scan(&r.ID, &r.Name, &pattern, &threshold, &windowMin, &action, &scope) != nil {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			log.Printf("violation rule %s: bad regex: %v", r.Name, err)
			continue
		}
		r.Re = re
		r.Threshold = threshold
		r.Window = time.Duration(windowMin) * time.Minute
		r.Action = action
		r.ScopeGlobal = scope == 1
		rules = append(rules, r)
	}
	vw.mu.Lock()
	vw.rules = rules
	vw.mu.Unlock()
}

// reconcile starts watchers for running servers and stops them for the rest.
func (vw *violationWatcher) reconcile() {
	vw.mu.Lock()
	haveRules := len(vw.rules) > 0
	vw.mu.Unlock()

	running := map[string]string{} // serverID -> containerID
	if haveRules {
		rows, err := vw.s.db.Query(
			"SELECT id, COALESCE(container_id,'') FROM servers WHERE status='running' AND container_id<>''")
		if err == nil {
			for rows.Next() {
				var id, cid string
				if rows.Scan(&id, &cid) == nil {
					running[id] = cid
				}
			}
			rows.Close()
		}
	}

	vw.mu.Lock()
	defer vw.mu.Unlock()
	// Stop watchers for servers no longer running (or when rules were removed).
	for id, cancel := range vw.active {
		if _, ok := running[id]; !ok {
			cancel()
			delete(vw.active, id)
		}
	}
	// Start watchers for newly running servers.
	for id, cid := range running {
		if _, ok := vw.active[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		vw.active[id] = cancel
		go vw.watch(ctx, id, cid)
	}
}

func (vw *violationWatcher) watch(ctx context.Context, serverID, containerID string) {
	rc, err := vw.s.docker.Logs(ctx, containerID, "0") // only new lines
	if err != nil {
		return
	}
	defer rc.Close()

	pr, pw := io.Pipe()
	go func() {
		_ = docker.DemuxCopy(pw, rc)
		pw.Close()
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		vw.processLine(serverID, sc.Text())
	}
}

func (vw *violationWatcher) processLine(serverID, line string) {
	vw.mu.Lock()
	rules := vw.rules
	vw.mu.Unlock()

	for _, r := range rules {
		m := r.Re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		player := ""
		if len(m) > 1 {
			player = m[1]
		}
		if player == "" {
			continue
		}
		if vw.record(r, serverID, player) {
			vw.trigger(r, serverID, player)
		}
	}
}

// record adds a hit and reports whether the threshold is reached in the window.
func (vw *violationWatcher) record(r violationRule, serverID, player string) bool {
	key := r.ID + "|" + serverID + "|" + player
	now := time.Now()
	cutoff := now.Add(-r.Window)

	vw.mu.Lock()
	defer vw.mu.Unlock()
	times := vw.hits[key]
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	vw.hits[key] = kept
	if len(kept) >= r.Threshold {
		delete(vw.hits, key) // reset after firing
		return true
	}
	return false
}

func (vw *violationWatcher) trigger(r violationRule, serverID, player string) {
	s := vw.s
	if r.Action == "kick" {
		// Best-effort kick via the game's console (Minecraft/Rust style).
		s.sendToServer(serverID, "kick "+player+" "+r.Name)
		s.notifyAll("👢 Auto-kicked " + player + " (" + r.Name + ") on " + s.serverName(serverID))
		return
	}
	// Ban: record in the central list (server-scoped or global) and push.
	banScope := serverID
	if r.ScopeGlobal {
		banScope = "" // all servers
	}
	s.db.Exec("INSERT INTO bans (id, player_name, server_id, reason) VALUES (?,?,?,?)",
		uuid.New().String(), player, nullableStr(banScope), "auto: "+r.Name)
	for _, sid := range s.scopeServers(banScope, "") {
		s.pushBan(context.Background(), sid, player, "auto: "+r.Name, true)
	}
	s.notifyAll("🔨 Auto-banned " + player + " (" + r.Name + ")")
	log.Printf("auto-ban: %s for rule %q (server %s)", player, r.Name, serverID)
}
