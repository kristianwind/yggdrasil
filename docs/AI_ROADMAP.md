# AI roadmap — from game digest to a panel-wide ops assistant

**Status:** the first AI feature shipped in v0.2.103 — the *advisory admin-log
digest* ("what happened while I was away") on the game Activity tab. This doc
proposes how to grow the AI layer beyond games to the whole panel and every rune.

## Principles (unchanged, apply to everything below)

- **Advisory + opt-in.** Off by default. The AI never changes a server on its own.
- **Own LLM.** The operator brings their own provider/key/endpoint (`internal/llm`
  already covers OpenAI/Anthropic/OpenRouter/DeepSeek/Mistral/Ollama/custom).
- **Deterministic "hands", AI "brain".** The panel already has safe, RBAC-gated
  actions (restart, wipe, kick, broadcast, schedule, backup). The AI's job is to
  *read* signals and *propose*; a human (or an explicit auto-rule) pulls the trigger.
- **Propose-then-confirm** for anything that acts. Never auto-execute from free text.
- **Untrusted input.** Player names, chat and logs are attacker-controlled — always
  framed to the model as data, never instructions (see the digest prompt hardening).
- **No new telemetry.** Everything reads data the panel already holds.

## What we can reuse today

`internal/llm` (provider-agnostic chat), the encrypted `ai_config`, `parseAdminLog`,
the scheduler + `schedule_runs`, `audit_log`, per-server CPU/RAM stats, watchdog
heals, reachability probes, backup status, the DayZ Norn/economy analysis, and the
notification channels (Telegram/Discord/webhook/email).

---

## Phase 1 — generalize the digest to any rune (near-term, low risk)

Today the digest is gated on a game's `admin_log:` block. Two cheap generalizations:

1. **`logs:` digest for non-game runes.** Let any rune point at a log source
   (container stdout, or a file glob like the admin log) and offer the same
   Summarize button: "Postgres: 3 connection storms + 1 slow query", "Vaultwarden:
   12 failed logins from 1 IP overnight". Reuses `readTail` + a generic prompt.
2. **Error explainer.** On an install failure or a crash, a one-click *"Explain
   this"* that feeds the tail of the install/console log to the LLM and returns a
   plain-language cause + fix ("SteamCMD: not enough disk quota — free ~4 GB").
   Highest utility-per-effort item on this list.

## Phase 2 — panel-wide health digest (medium, high value)

A cross-server briefing built entirely from signals the panel **already** records —
no log shipping, no new collection:

- crash/restart counts + watchdog heals (from `schedule_runs` / status changes),
- CPU/RAM trend + disk pressure, reachability flips, backup success/failure.

> *"Overnight: Midgard restarted 3× (watchdog), disk at 88%, 2 backups to `nas`
> failed, Niflheim unreachable from outside since 02:10."*

Surfaced on a dashboard card and, optionally, pushed once a day through the existing
notification channels ("daily ops digest").

## Phase 3 — config & moderation advisors (medium)

- **Config advisor.** When creating/editing a server, review env vs. best practice
  and flag issues inline: weak/`change-me` RCON password, memory too low for the
  declared player count, DayZ loot lifetimes that despawn too fast (we already
  compute the Norn/economy numbers — feed them in). Advisory suggestions, not blocks.
- **Moderation assist.** From the activity feed + violation rules, suggest players
  worth a look ("Bob: 8 kills / 0 deaths in 5 min across 2 servers — possible
  cheating") with one-click links to the existing kick/ban actions. The human decides.

## Phase 4 — natural-language ops (the north star; build last, carefully)

Let an admin describe an outcome and have the AI translate it into the panel's
existing actions, **always** as a preview to confirm:

> "Restart all Minecraft servers at 4am with a 10-minute warning, skip if players
> are online." → AI drafts the schedule rows → admin reviews → applied.

Hard rules: the AI may only propose actions the requesting user could perform
manually (same RBAC check), it renders an explicit diff/plan, and nothing runs until
the human confirms (or a pre-approved auto-rule the admin set up fires). This is the
"AI drives the same endpoints" goal — the deterministic tools built in v0.2.99–103
are exactly the surface it targets.

## Phase 5 — proactive pattern watch (opt-in, scheduled)

A daily scheduled AI pass over activity + audit + metrics that only speaks up when it
finds something: recurring crashes at the same time, a mod that keeps breaking joins,
a griefer hopping servers, disk trending to full by the weekend. Delivered through the
notification channels. This is "find patterns" without a human having to go looking.

---

## Suggested build order

1. **Error explainer** (Phase 1.2) — tiny, immediately useful, safe.
2. **Panel-wide health digest** (Phase 2) — reuses existing signals, big perceived value.
3. **`logs:` digest for apps** (Phase 1.1) — makes AI useful beyond games.
4. **Config advisor** (Phase 3) — catches footguns at create time.
5. **Natural-language ops** (Phase 4) — the flagship; only after the above prove the pattern.
