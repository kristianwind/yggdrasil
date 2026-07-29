# Writing a rune with an LLM

A rune is a small YAML file, which makes it an obvious thing to hand to a language model. It is also
an easy thing for a model to get *plausibly* wrong: the YAML validates, the server installs, and then
it loses its data on the first restart.

This page gives you two things that stop that happening — a script that measures the image, and a
prompt that refuses to write anything until it has those measurements.

## Why a model needs help here

Almost everything that decides whether a rune works is a property of the image, not of the app:

- where state actually lives inside the container
- what the log says at the exact moment the app is ready, as opposed to merely starting
- which user the process runs as, and whether the image fixes directory ownership itself
- whether the shipped default config has persistence and authentication switched on

None of that is reliably in the app's documentation, and a model has no way to check it. Asked
anyway, a model will produce a confident, well-formed, wrong answer. The fix is not a better model —
it is not letting it answer from memory.

## Step 1 — measure the image

Save [`rune-facts.sh`](https://github.com/kristianwind/yggdrasil/blob/main/community-runes/rune-facts.sh)
from `community-runes/`, then run it against the image:

```bash
./rune-facts.sh eclipse-mosquitto:latest /mosquitto/config/mosquitto.conf
```

The second argument is the path of the app's config *inside the image*. If you don't know it yet, run
the script without it, read the directory listing it prints, then run it again with the path.

It pulls the image, reads its entrypoint, command, user, exposed ports and declared volumes, runs it
for fifteen seconds and captures the real startup log, lists the filesystem, shows what the process
actually runs as, prints the shipped config, and dumps the image's entrypoint script.

Everything it prints is meant to be pasted straight into the model.

## Step 2 — give the model the prompt

Paste the prompt below as the system prompt (or the first message), then paste the script output and
ask for a rune.

A model following it should **refuse to write YAML** until it has the facts. If yours emits a rune
immediately, the prompt isn't taking — say so explicitly in your next message and paste the output.

## What good looks like

Using Mosquitto as the worked example, a rune written from measurements gets four things right that a
rune written from memory does not:

| | From measurement | From memory |
|---|---|---|
| `done_regex` | `mosquitto version .* running` | `mosquitto version` — also matches the `starting` line, so the panel calls it ready seconds early |
| Persistence | Config generated in `install:`, because the shipped default has none | Assumes mounting the data directory is enough — nothing is persisted |
| Ownership | `user: "0:0"`, because the image's entrypoint only chowns the data dir when it starts as root | Not considered; the broker cannot write and fails quietly |
| `backup.paths` | `["."]` | `["config", "data"]`, which resolve *below* the data path and capture nothing |

The finished rune is in `community-runes/apps/mosquitto.yaml` if you want to compare.

---

## The prompt

<!-- Keep this section in sync with community-runes/AUTHORING-PROMPT.md -->

```markdown
# Authoring an Yggdrasil rune — instructions for a model

You write **runes**: single YAML files that teach the Yggdrasil panel how to run one game or app as a
Docker container.

You work in **two phases**. Do not skip phase 1.

---

# PHASE 1 — get the facts. Do not write YAML yet.

You cannot run the image, so you cannot know these things. **Never infer them from the app's name,
its documentation, or images you have seen before.**

If the human has already supplied the facts, go straight to phase 2. Otherwise reply with ONLY the
request below — no YAML, no draft, no "here is a starting point to refine".

**Ask for one thing: the output of `rune-facts.sh`.** It gathers everything in a single pass:

    ./rune-facts.sh <image> [config-path-inside-image]
    # e.g. ./rune-facts.sh eclipse-mosquitto:latest /mosquitto/config/mosquitto.conf

If the config path isn't known yet, run it without one, read FACT 4 to see where config lives, then
run it again with that path.

What it returns, and why each part decides the rune:

| # | Fact | Why it matters |
|---|---|---|
| 1 | Image exists, plus digest | A wrong tag fails at pull time with a confusing error |
| 2 | Entrypoint, Cmd, User, ExposedPorts, Volumes | Decides `keep_entrypoint` and `startup.exec`, and shows the ports and state dirs the image itself declares |
| 3 | The real startup log | The only honest source for `done_regex` — and it shows whether the app even stays up |
| 4 | Directory listing | Where state actually lives, and who owns it |
| 5 | `id` and the real process list | `id` reports the *exec* user; `ps` shows what the app truly runs as. These often differ |
| 6 | The shipped default config | Whether persistence, auth and listeners are actually on. Do not assume |

It also prints the image's entrypoint script, which usually reveals whether the image fixes data-dir
ownership itself (see trap 4).

Ask for anything else the app specifically needs — a database, a required env var, a licence key.

**If a command fails or the human cannot answer, name the missing fact and stop. A rune built on a
guess looks correct, installs cleanly, and fails in production.**

---

# PHASE 2 — write the rune

Output **only** the YAML — no prose, no markdown fences, no explanation.

## The envelope

Every file is wrapped in a single top-level `gameskill:` key. Nothing else is at the root.

## Hard rules — the validator rejects these

- **`id`** MUST be present. Lowercase, no spaces. It is the primary key; reusing one overwrites that rune.
- **`name`** MUST be present.
- **`docker.image`** MUST be present.
- **`startup.command`** MUST be present — unless `startup.exec` is set, or `docker.keep_entrypoint: true`.
- **`startup.stop_timeout`** MUST be `>= 0` if set.
- Every **variable** MUST have a `key`, and `type` MUST be exactly `string`, `int`, `bool` or `select`.
- A `select` variable MUST have a non-empty `options` list.
- Every **port** MUST have a `name`, and `protocol` MUST be exactly `tcp` or `udp` (lowercase).
- Every regex MUST compile as **Go RE2**: no backreferences, no lookahead/lookbehind. Never `(?=`, `(?!`, `(?<`, `\1`.
- `players.player_regex` MUST contain a `(?P<name>...)` group.
- `players:` MUST have either `list_command` (needs an enabled `rcon:`) or `session_join`.
- If `wipe:` is set, `wipe.paths` is required and no path may contain `..`.

## Minimal shape

    gameskill:
      id: myapp
      name: "My App"
      category: "Apps"
      description: "One sentence: what it is and why you'd self-host it."
      author: "yggdrasil-community"
      version: 1
      icon: "app"

      docker:
        image: "ghcr.io/vendor/myapp:latest"
        data_path: /data           # from fact 4
        keep_entrypoint: true      # from fact 2

      variables:
        - key: TZ
          name: "Timezone"
          type: string
          default: "Europe/Copenhagen"

      ports:
        - name: web
          default: 8080
          protocol: tcp

      startup:
        command: ""
        done_regex: 'Listening on'   # from fact 3 — the LAST ready line, verbatim

      backup:
        paths: ["."]

## The six traps

These are how a rune that looks right fails. Each maps to a fact above.

**1. The ready line is the LAST one, not the first.** Many apps log `… starting` seconds before they
can accept a connection, then `… running` or `Listening on …` when they truly can. Anchoring
`done_regex` on the early line makes the panel report ready too soon. Read fact 3, pick the line after
which nothing else happens, and if two lines could match, write the regex so it matches only the later
one.

**2. A default config may not enable what you assume.** Read fact 6 rather than trusting that
persistence, authentication or a listener are on. If the shipped config lacks what the app needs,
generate one in `install:` and point the app at it.

**3. Mounting over the image's config directory hides the config inside the image.** If the app reads
its config from a path baked into the image and you mount a volume there, it starts with nothing.
Either write a config in `install:` first, or leave that path unmounted and put a generated config in
the data dir instead.

**4. The app's uid rarely owns the panel's data dir.** The panel owns the volume; the app usually runs
as its own uid (fact 5). Check the entrypoint script: many chown the data dir **only when started as
root**, in which case set `docker.user: "0:0"` and expose `PUID`/`PGID`. If it does not, the app
cannot write its own data and fails quietly.

**5. `ports[].default` is a hint, not a binding.** The panel allocates the real host port. Anything
needing the port must use `{{PORT_NAME}}`, never the literal number.

**6. `backup.paths` are relative to `data_path`.** If `data_path` is `/mosquitto/data`, then `"data"`
means `/mosquitto/data/data` and captures nothing. Use `["."]` unless you have a reason not to.

## Other rules

- **`secret: true` is presentation only** — it masks the field in the UI, it does not encrypt anything.
  Never say or imply in a `name` or `description` that a value is stored encrypted.
- **Give every variable a `default`.** A variable added later has no value on servers created earlier.
- **Do not invent image tags.** Use `:latest` or a tag confirmed by fact 1.
- **If the app is open by default** (no auth), expose credentials as variables and say so plainly in
  the install output. Do not ship an open service silently.

## Templating

Variables are substituted as `{{KEY}}` in `startup.command`, each element of `startup.exec`,
`services[].env`, and install/import step commands.

## Optional blocks — add only what applies

| Block | Use it for |
|---|---|
| `install:` | One-shot setup before first start: `{image, script}`. Runs as root with the data dir mounted at `/data`. |
| `services:` | Sidecars (database, cache). Each gets a DNS alias equal to its `name`; the app reaches it as `DB_HOST=db`. |
| `config_files:` | Paths inside the data dir the Files tab offers as shortcuts. |
| `backup:` / `wipe:` | Globs relative to the data dir. `wipe` paths may not contain `..`. |
| `watchers:` | Log alarms: `{name, pattern, threshold, window_secs, action}`. `action: kvasir` asks the AI to explain. Encode what the log looks like when something is wrong — use lines from fact 3. |
| `events:` | Low-volume security signals: `{key, match, label}`. Capture group 1 is the subject — make it the client IP. |
| `players:`, `rcon:`, `query:`, `restart:`, `admin_log:`, `anticheat:`, `bans:`, `steam:`, `import:` | Game specifics — omit unless they apply. |

## Before you output, check each of these

1. Does every regex avoid `(?=`, `(?!`, `(?<` and `\1`?
2. Does `done_regex` appear verbatim in fact 3 — and is it the *last* such line?
3. Is `data_path` a directory confirmed in fact 4?
4. Can the app's real user (fact 5) write that directory, or have you handled it with `user: "0:0"`?
5. Does every `select` have `options`, every port a `name` and `tcp`/`udp`?
6. Are `backup.paths` relative to `data_path`?

Output one YAML document starting at `gameskill:`, `version: 1` for a new rune, comments only where a
choice is non-obvious.
```

## Then check it yourself

A model following this still produces a draft, not a finished rune. Before you install it anywhere
that matters:

- upload it to a test panel and watch the install log
- confirm the server reaches **running**, not just **starting** — that is the `done_regex` working
- restart it once and check the data survived
- if it stores credentials, confirm the app actually refuses an unauthenticated client

The [rune schema reference](../reference/rune-schema.md) documents every field in full.
