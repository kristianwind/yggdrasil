package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/llm"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
)

// Kvasir proactive monitoring. When switched on, panel events — a crash, a slow
// start, a resource alarm, a host problem — are handed to the configured AI, which
// explains what happened and, depending on the level, proposes or (safely) applies
// a fix:
//
//	1 passive        — explain the event in plain language, suggest a fix. Read-only.
//	2 active-observe — same, plus a concrete proposed action to confirm.
//	3 active-help    — apply SAFE, reversible fixes itself (restart/safe_restart);
//	                   config/env changes and anything destructive are only proposed.
//
// Hard limits regardless of level: never destructive autonomously (delete/wipe/
// reinstall are proposed, never applied), rate-limited per server, always audited
// (actor "kvasir") and announced to the server's notification channels.

const (
	kvasirMaxActionsPerWindow = 3
	kvasirActionWindow        = 30 * time.Minute
	kvasirReactMaxTokens      = 1200
)

type kvasirState struct {
	mu   sync.Mutex
	acts map[string][]time.Time // serverID -> recent auto-action times
}

func newKvasirState() *kvasirState { return &kvasirState{acts: map[string][]time.Time{}} }

// allowAction reports whether a server is still under its auto-action rate limit,
// recording the action when allowed. Prevents an AI fix-loop.
func (k *kvasirState) allowAction(serverID string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	cut := now.Add(-kvasirActionWindow)
	kept := k.acts[serverID][:0:0]
	for _, t := range k.acts[serverID] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= kvasirMaxActionsPerWindow {
		k.acts[serverID] = kept
		return false
	}
	k.acts[serverID] = append(kept, now)
	return true
}

type kvasirDecision struct {
	Explanation string `json:"explanation"`
	Action      string `json:"action"` // none | restart | safe_restart | set_memory | set_env | other
	Args        string `json:"args"`   // e.g. "6144" (MB) or "EULA=true"
	Reason      string `json:"reason"`
}

// kvasirReact is the entry point every watched event calls. Cheap-exits fast when
// proactive monitoring is off or this event isn't watched. Runs the AI in the
// background — callers fire it with `go`.
func (s *Server) kvasirReact(serverID, event, detail, logTail string) {
	defer recoverLog("kvasirReact")
	if s.kvasir == nil {
		return
	}
	cfg := s.loadAIConfig(context.Background())
	if !cfg.Enabled || cfg.APIKey == "" || cfg.ProactiveLevel == 0 {
		return
	}
	if !kvasirTriggerOn(cfg.ProactiveTriggers, event) {
		return
	}
	name := s.serverName(serverID)
	if name == "" {
		name = "the host"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := llm.Complete(ctx,
		llm.Config{Provider: cfg.Provider, Model: cfg.Model, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey},
		buildKvasirMessages(name, event, detail, logTail), kvasirReactMaxTokens)
	if err != nil {
		return
	}
	dec := parseKvasirDecision(out)
	if dec.Explanation == "" {
		return
	}

	body := fmt.Sprintf("🧠 Kvasir · %s — %s\n%s", name, event, dec.Explanation)
	if dec.Action != "" && dec.Action != "none" {
		body += fmt.Sprintf("\n\n**Suggested fix:** `%s%s` — %s", dec.Action, kvasirArgSuffix(dec.Args), dec.Reason)
	}

	applied := false
	applyStatus := ""
	switch cfg.ProactiveLevel {
	case 1: // passive
		s.notifyServer(serverID, body)
	case 2: // active-observe
		s.notifyServer(serverID, body+"\n\n_Run it from Ask Kvasir, or raise the level to Active help to let me apply safe fixes._")
	case 3: // active-help
		applied, applyStatus = s.kvasirApply(serverID, dec, body)
	}
	// Record it in the panel so the admin can see (and act on) what Kvasir saw and
	// proposed without having to be in Discord.
	s.recordKvasirEvent(serverID, event, detail, dec, cfg.ProactiveLevel, applied, applyStatus)
}

// recordKvasirEvent persists one Kvasir reaction to the in-panel history.
// Best-effort: a failed insert must never break the reaction itself.
func (s *Server) recordKvasirEvent(serverID, event, detail string, dec kvasirDecision, level int, applied bool, applyStatus string) {
	ap := 0
	if applied {
		ap = 1
	}
	s.db.Exec(
		`INSERT INTO kvasir_events (id, server_id, event, detail, explanation, action, args, reason, level, applied, apply_status)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		uuid.New().String(), serverID, event, detail, dec.Explanation, dec.Action, dec.Args, dec.Reason, level, ap, applyStatus)
}

// kvasirApply executes a fix at the Active-help level, but only the safe,
// reversible ones. Config/env changes and anything destructive are proposed, not
// applied — the panel never lets the AI reconfigure or delete on its own.
// kvasirApply returns whether it auto-applied a fix and a short status ("proposed",
// "rate-limited", or the runAction status) so the caller can record it.
func (s *Server) kvasirApply(serverID string, dec kvasirDecision, body string) (bool, string) {
	restart := map[string]scheduler.Action{
		"restart":      scheduler.ActionRestart,
		"safe_restart": scheduler.ActionRestart,
	}
	if act, ok := restart[dec.Action]; ok {
		if !s.kvasir.allowAction(serverID, time.Now()) {
			s.notifyServer(serverID, body+kvasirRateLimitNote)
			return false, "rate-limited"
		}
		args := map[string]string{}
		if dec.Action == "safe_restart" {
			args["warn"] = "true"
		}
		status, detail := s.runAction(act, serverID, args)
		s.auditSystem("kvasir.action", "server:"+serverID, "kvasir", map[string]any{"action": dec.Action, "status": status})
		s.notifyServer(serverID, body+fmt.Sprintf("\n\n✅ I applied **%s** (%s). %s", dec.Action, status, detail))
		return true, status
	}
	// set_memory is the one config change allowed to auto-apply — bounded, and only to
	// RAISE a server that was OOM-killed. Everything else (set_env, wipe, delete,
	// reinstall) stays propose-only: the panel never lets the AI reconfigure or delete.
	if dec.Action == "set_memory" {
		return s.kvasirApplyMemory(serverID, dec, body)
	}
	s.notifyServer(serverID, body+"\n\n_This needs a config change (or isn't auto-safe), so I'm leaving it for you to apply — I only auto-run safe restarts and bounded memory bumps._")
	return false, "proposed"
}

const kvasirRateLimitNote = "\n\n🛑 I've already auto-fixed this server a few times recently — pausing auto-fixes and leaving it to you."

// kvasirClampMemory bounds an auto-applied memory bump. It returns the MB to apply and
// whether the bump is safe to auto-apply at all. Rules: there must be a current limit
// (0 = unlimited, where a cap can't fix a host-level OOM); the proposal must raise it;
// and the result is capped at min(2× current, 80% of host RAM). If that cap leaves no
// room above the current limit, it declines (ok=false) so the fix is proposed instead.
func kvasirClampMemory(proposedMB, currentMB, hostTotalMB int64) (int64, bool) {
	if currentMB <= 0 || proposedMB <= currentMB {
		return 0, false
	}
	capMB := currentMB * 2
	if hostTotalMB > 0 {
		if hostCap := hostTotalMB * 80 / 100; hostCap < capMB {
			capMB = hostCap
		}
	}
	if capMB <= currentMB {
		return 0, false
	}
	if proposedMB > capMB {
		proposedMB = capMB
	}
	return proposedMB, true
}

// kvasirApplyMemory auto-raises a server's RAM limit after an OOM kill, then restarts
// it so the change takes effect. Bounded so a bad AI value can't over-commit the host:
// never below the current limit, at most 2× it, and never above 80% of host RAM. If
// there is no current limit (0 = unlimited) a cap wouldn't help an OOM, so it proposes.
func (s *Server) kvasirApplyMemory(serverID string, dec kvasirDecision, body string) (bool, string) {
	proposeNote := body + "\n\n_This memory change isn't safe to auto-apply here, so I'm leaving it for you to set from Settings._"
	newMB, err := strconv.Atoi(strings.TrimSpace(dec.Args))
	if err != nil || newMB <= 0 {
		s.notifyServer(serverID, proposeNote)
		return false, "proposed"
	}
	var current int64
	s.db.QueryRow("SELECT COALESCE(mem_limit_mb,0) FROM servers WHERE id=?", serverID).Scan(&current)
	var hostTotalMB int64
	if total, _ := hostMem(); total > 0 {
		hostTotalMB = int64(total / (1024 * 1024))
	}
	applyMB, ok := kvasirClampMemory(int64(newMB), current, hostTotalMB)
	if !ok { // unlimited already, not a raise, or no headroom under the host cap
		s.notifyServer(serverID, proposeNote)
		return false, "proposed"
	}
	newMB = int(applyMB)
	if !s.kvasir.allowAction(serverID, time.Now()) {
		s.notifyServer(serverID, body+kvasirRateLimitNote)
		return false, "rate-limited"
	}
	if _, err := s.db.Exec("UPDATE servers SET mem_limit_mb=? WHERE id=?", newMB, serverID); err != nil {
		s.notifyServer(serverID, proposeNote)
		return false, "proposed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	status := "restarted"
	if err := s.recreateAndStart(ctx, serverID); err != nil {
		status = "applied, restart failed: " + err.Error()
	}
	s.auditSystem("kvasir.action", "server:"+serverID, "kvasir", map[string]any{"action": "set_memory", "mem_mb": newMB, "from_mb": current})
	s.notifyServer(serverID, body+fmt.Sprintf("\n\n✅ I raised memory %d→%d MB and restarted the server (%s).", current, newMB, status))
	return true, status
}

func kvasirTriggerOn(triggers, event string) bool {
	// A watcher is its own opt-in (the admin created it and set action=kvasir), so
	// it isn't gated on the proactive_triggers list the automatic events use.
	if event == "watcher" {
		return true
	}
	if triggers == "" {
		triggers = "crash,slowstart,resource,host"
	}
	for _, t := range strings.Split(triggers, ",") {
		if strings.TrimSpace(t) == event {
			return true
		}
	}
	return false
}

func kvasirArgSuffix(args string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	return " " + strings.TrimSpace(args)
}

// buildKvasirMessages is the reaction prompt: the event + context, and a strict
// JSON contract. Pure + testable.
func buildKvasirMessages(name, event, detail, logTail string) []llm.Message {
	system := "You are Kvasir, the operations assistant for a self-hosted game/app server panel. " +
		"An event just happened on a server. Explain it briefly and plainly to the admin, and propose ONE fix. " +
		"Respond with ONLY a JSON object — no prose, no markdown fences — of the form:\n" +
		`{"explanation":"<1-3 sentences: what happened and why>","action":"<none|restart|safe_restart|set_memory|set_env>","args":"<memory MB, or KEY=VALUE, else empty>","reason":"<short why this fix>"}` + "\n\n" +
		"Guidance: exit code 137 is usually an out-of-memory kill (suggest set_memory with a higher MB). " +
		"exit 143/130 are graceful stops (usually action none). A slow start that's still loading is often " +
		"fine (action none) unless the log shows a fatal error. DayZ 'Unknown object class' / 'No components in' " +
		"lines are harmless map/mod noise, not a fault. Prefer action none when nothing is actually wrong. " +
		"Only use safe_restart/restart for a hung-but-not-crashed server; use set_memory/set_env for config causes."
	user := fmt.Sprintf("Server: %s\nEvent: %s (%s)\n\nRecent log tail:\n%s", name, event, detail, logTail)
	return []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
}

// parseKvasirDecision tolerantly extracts the decision JSON (strips ``` fences and
// any prose around the object).
func parseKvasirDecision(out string) kvasirDecision {
	out = strings.TrimSpace(out)
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			out = out[i : j+1]
		}
	}
	var d kvasirDecision
	_ = json.Unmarshal([]byte(out), &d)
	d.Explanation = strings.TrimSpace(d.Explanation)
	d.Action = strings.ToLower(strings.TrimSpace(d.Action))
	return d
}
