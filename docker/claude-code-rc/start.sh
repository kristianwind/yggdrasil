#!/bin/sh
# Bring a headless Claude Code Remote Control agent up inside the panel.
#
# Two first-run gates would otherwise block a headless agent forever: the
# workspace-trust dialog and the one-time "Enable Remote Control?" consent. Both
# are just state in ~/.claude.json, so we seed them before the CLI ever starts.
set -eu

WORKDIR="${HOME}/work"
CONFIG="${HOME}/.claude.json"
CREDS="${HOME}/.claude/.credentials.json"
RC_NAME="${RC_NAME:-claude-rc}"
PERMISSION_MODE="${PERMISSION_MODE:-default}"

mkdir -p "$WORKDIR" "${HOME}/.claude"

# Trust is stored per directory and a home directory is never remembered, so the
# agent works in ${HOME}/work rather than in ${HOME} itself.
node -e '
  const fs = require("fs");
  const p = process.argv[1], dir = process.argv[2];
  let cfg = {};
  try { cfg = JSON.parse(fs.readFileSync(p, "utf8")) || {}; } catch (e) {}
  cfg.remoteDialogSeen = true;
  cfg.projects = cfg.projects || {};
  cfg.projects[dir] = Object.assign({
    allowedTools: [],
    history: [],
    projectOnboardingSeenCount: 1,
    hasCompletedProjectOnboarding: true,
  }, cfg.projects[dir], { hasTrustDialogAccepted: true });
  fs.writeFileSync(p, JSON.stringify(cfg, null, 2));
' "$CONFIG" "$WORKDIR"

git config --global --get user.name  >/dev/null 2>&1 || git config --global user.name  "${GIT_USER_NAME:-Claude Code}"
git config --global --get user.email >/dev/null 2>&1 || git config --global user.email "${GIT_USER_EMAIL:-noreply@anthropic.com}"
git config --global --add safe.directory '*'

# A repo is cloned once, on the first start. After that the working copy is the
# agent's own — re-cloning would throw away whatever it has in flight.
if [ -n "${REPO_URL:-}" ] && [ ! -e "${WORKDIR}/.git" ]; then
  echo "Cloning ${REPO_URL} into ${WORKDIR} ..."
  git clone "$REPO_URL" "$WORKDIR" || echo "WARNING: clone failed — starting with an empty workspace."
fi

if [ -n "${GH_TOKEN:-}" ]; then
  gh auth setup-git >/dev/null 2>&1 || echo "WARNING: 'gh auth setup-git' failed — git pushes over HTTPS may prompt."
fi

# Remote Control needs a full-scope login. A long-lived token from
# `claude setup-token` is deliberately inference-only and is REJECTED here, so
# there is no way to pre-bake the credentials into a variable — the sign-in has
# to happen once, in this container. `claude auth login` prints a URL and then
# blocks reading the code from stdin, which is exactly what the panel's Console
# tab is: paste the code there and it lands on the container's stdin.
if [ ! -s "$CREDS" ]; then
  echo "============================================================"
  echo " This agent is not signed in yet."
  echo " 1. Open the URL printed below in a browser and approve."
  echo " 2. Paste the code it gives you into this server's Console tab."
  echo " The sign-in is stored in the server's data dir, so this is a"
  echo " one-time step — restarts will not ask again."
  echo "============================================================"
  attempt=1
  while [ ! -s "$CREDS" ]; do
    if [ "$attempt" -gt 3 ]; then
      echo "FATAL: still not signed in after 3 attempts — stopping instead of"
      echo "restart-looping (a crash loop would rewrite the config we just seeded)."
      exit 1
    fi
    echo "--- sign-in attempt ${attempt} of 3 ---"
    claude auth login || echo "Sign-in attempt failed."
    attempt=$((attempt + 1))
  done
  echo "Signed in."
fi

cd "$WORKDIR"
echo "Starting Remote Control as '${RC_NAME}' (permission mode: ${PERMISSION_MODE}) in ${WORKDIR}"
exec claude remote-control --name "$RC_NAME" --permission-mode "$PERMISSION_MODE"
