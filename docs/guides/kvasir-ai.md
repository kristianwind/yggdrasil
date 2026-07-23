# Kvasir — the AI assistant

Kvasir is Yggdrasil's optional AI layer: plain-language digests, an error explainer, a config
review, and a propose-then-confirm way to restart servers by describing what you want. It is off
until you configure a provider, and it uses your key and your endpoint.

## It is off by default

A fresh install has no AI at all. The `ai_config` row starts with `enabled = 0`, no model and no
API key, so every AI feature is hidden and nothing is ever sent off the host. To switch it on you
need two things: a provider with a working key, and the tier switches under
**Settings → Integrations → Kvasir**.

Only an admin can see or change the Kvasir settings — the config endpoints are admin-only.

## Choosing a provider

Kvasir speaks two wire formats: Anthropic's Messages API, and the OpenAI `/chat/completions` shape
that most other gateways implement. Pick a provider, enter the exact model id for it, and paste
your key. The key is encrypted at rest and never echoed back — reopening the page shows a mask, and
saving with the mask in place keeps the stored key.

| Provider | Wire format | Default base URL |
| --- | --- | --- |
| `anthropic` | Anthropic Messages | `https://api.anthropic.com` |
| `openai` | OpenAI chat completions | `https://api.openai.com/v1` |
| `openrouter` | OpenAI chat completions | `https://openrouter.ai/api/v1` |
| `deepseek` | OpenAI chat completions | `https://api.deepseek.com/v1` |
| `mistral` | OpenAI chat completions | `https://api.mistral.ai/v1` |
| `ollama` | OpenAI chat completions | `http://127.0.0.1:11434/v1` |
| `custom` | OpenAI chat completions | none — you must supply one |

The **Base URL** field overrides the default for any provider, which is what makes the
OpenAI-compatible list open-ended: anything that serves `/chat/completions` works. Use `custom`
with a base URL for a self-hosted gateway, or point `ollama` at a LAN box (`http://192.168.1.x:11434/v1`)
rather than `127.0.0.1` when Yggdrasil runs in Docker and Ollama runs on the host. A `custom`
provider with no base URL is rejected.

An API key is required for every provider except `ollama`, which may be left blank.

After **Save**, use **Test connection**. It sends a one-line prompt with the stored config and shows
the model's reply, so you find out about a bad key or an unreachable endpoint here rather than
halfway through a digest.

## The two tiers

Kvasir enables in two steps, and the second one never turns itself on.

**Advisory** (`enabled`) is the master switch. With it on, Kvasir reads panel data and writes text.
It unlocks the ops digest, the error explainer, the config advisor, the activity digest, and the
optional daily digest to your notification channels. Nothing in this tier changes a server.

**Actions** (`actions_enabled`, default off, and selectable only once Advisory is on) unlocks the
natural-language ops card on the dashboard: Kvasir may *propose* a short list of server actions that
you review and confirm. It cannot execute anything on its own, and it cannot propose anything
outside the allow-list below.

Turning Advisory off hides everything, including actions.

## What shipped

### Ops digest

The dashboard's **Kvasir · Ops digest** card asks for a cross-server briefing built from data the
panel already holds: how many servers are running, which ones aren't and their status, free disk on
the data filesystem, host memory and CPU, scheduled-task failures in the last 24 hours, failed
backups in the last 24 hours, and a 24-hour average/peak of CPU, memory and player counts per
server. No new collection happens for the digest — it is a query over existing rows.

Admin-only: `POST /api/ai/health-digest`.

### Error explainer

On the console and the install log, **Kvasir explains** sends the visible log text and gets back a
likely cause plus numbered fix steps. The tail is capped at 8000 characters — the most recent
lines, which is where the error normally is.

When the client has no log text to send (the console was cleared, or the container already exited
after a crash) and the server still has a container, Kvasir falls back to a snapshot of that
container's last 300 log lines. This is what lets you explain a crash after the fact. If neither
source has anything, it tells you there are no logs to explain.

Requires `server.view`: `POST /api/servers/{id}/explain`.

### Config advisor

A review of one server's rune settings against their current values, looking for footguns: default
or weak passwords, memory too low for the player count, options that cost performance or lose data.

Values the rune declares as secrets stay on the host. Yggdrasil masks every variable the rune marks
`secret: true`, plus the one named by its `rcon.password_var`, and decides locally whether the value
is weak — empty, shorter than six characters, or one of the obvious defaults like `change-me`,
`password`, `admin`, `123456` — sending the model only `(WEAK or default — should be changed)` or
`(set, strong)` in place of the value. The advisor can therefore flag a `change-me` RCON password
without the plaintext ever being transmitted.

The masking is only as good as the rune's declarations. A variable that holds a secret but was never
flagged `secret: true` is sent verbatim, like any other setting. If you write or import a rune, check
that its password fields carry the flag — see the [rune schema](../reference/rune-schema.md).

Requires `server.control`: `POST /api/servers/{id}/config-advice`.

### Activity digest

On the **Activity** tab of any server whose rune declares an `admin_log:` block, **Kvasir digest**
summarizes "what happened while I was away" from the parsed event feed — joins, leaves, deaths,
kills — and calls out anomalies like kill streaks or players who joined and left immediately.

Yggdrasil parses the log first and sends only the structured result: at most 200 events, each one a
timestamp, an event type and a player name. The raw log lines are not sent.

Requires `server.view`: `POST /api/servers/{id}/admin-log/digest`.

### Daily digest to notifications

**Daily ops digest to notifications** sends the same ops briefing to every enabled notification
channel once a day, at the hour you pick (default `08:00`, server time). A ten-minute ticker checks
whether today's digest has gone out, so a panel that was down at the chosen hour still catches up
later that day, and a per-day marker written before the LLM call prevents a slow request from
double-firing.

See [Notifications](notifications.md) for the channels it delivers through.

### Natural-language ops

With the Actions tier on, the dashboard shows **Ask Kvasir**. You describe an outcome ("safely
restart all Minecraft servers; players may be online"), and Kvasir returns a plan you review line by
line before anything runs.

The flow is two endpoints, deliberately:

1. `POST /api/ai/plan` — Yggdrasil sends your request plus the list of servers you can control
   (name, rune id, status) and gets back proposed actions. Each one is checked against the
   allow-list and resolved to a real server; rejected rows come back marked with why
   (`action not allowed`, `unknown or not-controllable server`) and are shown greyed out.
   **This endpoint never executes anything.**
2. `POST /api/ai/plan/execute` — runs only the actions you confirmed.

## Proactive monitoring

Beyond answering when asked, Kvasir can watch for trouble on its own. **Settings → Kvasir** has a
proactive level — off / passive (explain what happened) / active-observe (also propose a fix) /
active-help (apply the safe fixes automatically) — and a set of trigger checkboxes deciding what it
reacts to:

- **Crashes / faults** — a container that died unexpectedly.
- **Slow / failed starts** — a server stuck or giving up during start.
- **Resource alarms** — a tripped CPU/RAM/disk threshold.
- **Panel / host problems** — e.g. the host running low on disk.
- **Player / traffic anomalies** *(opt-in, not on by default)* — activity being wrong rather than a
  log line matching. Every ~5 minutes each running server is checked for three things: a **mass
  disconnect** (from 4+ players, 75% or more gone between samples while the server stayed up), a
  **player influx** above the server's own 14-day high (from 8+ players), and a **log-volume spike**
  (the container suddenly logging 5× its own recent baseline; the baseline warms up for ~30 minutes
  and quiet logs never alert). A detected anomaly is always notified as plain fact; the AI
  explanation on top follows the proactive level. At most one alert per hour per server per kind.

Log-pattern watching — regex rules over any server's log — is its own feature with its own guide
section: see Kvasir Watchers in the monitoring guide. A watcher with action *Kvasir* is its own
opt-in and doesn't need a trigger checkbox here.

At active-help, only `restart` and `safe_restart` run without confirmation (plus the clamped
memory raise after an out-of-memory crash); anything touching config or data stays propose-only.
Automatic actions are rate-limited to 3 per 30 minutes per server and audited as actor `kvasir`.

## The safety model

Each of these is enforced in the panel, not in the prompt:

- **Planning never executes.** `/api/ai/plan` builds and validates a preview and returns it. There
  is no execution path in it at all.
- **Execution is a separate, confirmed call.** `/api/ai/plan/execute` takes the actions you
  confirmed and re-derives the set of servers you may control from scratch. It does not trust the
  client's list: an action whose server id isn't in your freshly computed controllable set comes
  back `rejected / not permitted`.
- **`server.control` is re-checked per server.** Both the plan and the execute step build their
  server list from your grants (admins get all servers; everyone else gets exactly the servers where
  `server.control` is allowed). Kvasir can never propose or run an action on a server you couldn't
  act on yourself by hand.
- **The allow-list is four actions.** `restart`, `safe_restart`, `stop`, `start` — nothing else is
  executable. Wipe, delete, reconfigure, backup and arbitrary console commands are not in the
  vocabulary, and the allow-list is re-checked at execution, not only at planning time.
  `safe_restart` maps to the warned restart, which broadcasts the rune's in-game countdown first.
- **Both tiers are re-read on every call.** If Advisory or Actions is off when you confirm, the
  execute call refuses.

Most AI calls are written to the audit log (`ai.digest`, `ai.explain`, `ai.config_advice`,
`ai.health_digest`, `ai.action`, `ai.config`), so there is a record of who asked and what ran. Two
paths are not logged: **Test connection**, and the daily digest, which fires on a ticker rather than
a request. The entries record the endpoint and the target, not the conversation — no prompt text and
no model reply is stored.

Because game logs and player names are attacker-controlled, the prompts that carry them mark the
data as untrusted and tell the model to treat it strictly as data. Treat that as one layer among
several — the allow-list and the RBAC re-check are what actually constrain the outcome.

## What leaves your host

With Kvasir off, one thing: **Test connection**. It checks that a model and a key are configured, not
that Advisory is on, so pressing it sends its one-line prompt to your provider even with both tiers
switched off. Nothing else makes an AI request while Kvasir is off. There is no other endpoint
involved either — Yggdrasil talks only to the base URL you configured.

When you turn it on, each feature sends a bounded, purpose-built excerpt to that endpoint:

| Feature | What is sent |
| --- | --- |
| Ops digest, daily digest | Server names and statuses, disk/memory/CPU figures, 24h scheduled-task failure lines, a failed-backup count, per-server 24h CPU/memory/player averages and peaks |
| Error explainer | The rune id and the last 8000 characters of the log you're viewing (or the container's last 300 lines on the crash fallback) |
| Config advisor | The rune name and id, and each setting's name, key and value — with rune-declared secrets replaced by "(set, strong)" or "(WEAK …)" |
| Activity digest | The server name and up to 200 parsed events: time, type, player name |
| Natural-language ops | Your request text, plus the name, rune id and status of each server you can control |

Nothing else is shipped: not your backups, not your config files, not your API keys, not host
identity. Whatever your provider does with a prompt is between you and them — a local Ollama keeps
the whole loop on your own hardware.

## See also

- [Notifications](notifications.md) — where the daily digest is delivered
- [Servers](servers.md) — the actions Kvasir is allowed to propose
- [Monitoring and alerts](monitoring-and-alerts.md) — the signals the ops digest reads
- [API reference](../reference/api.md)
