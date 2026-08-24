# Claude Code Remote Control — a headless coding agent, driven from claude.ai/code
# or the Claude mobile app. The agent dials out to Anthropic, so the rune publishes
# no ports at all (same shape as the cloudflared connector).
FROM node:20-bookworm-slim

# The toolchain a coding agent actually reaches for: git for the repos, gh for
# pull requests, ripgrep for search, ssh for anything it has to reach over the
# network. tini reaps the child processes Claude Code spawns per session.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git gnupg jq less openssh-client procps ripgrep tini unzip \
 && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && chmod 0644 /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/*

ARG CLAUDE_CODE_VERSION=latest
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm cache clean --force \
 && claude --version

# The panel mounts the server's data dir at /data and runs the container as its
# own service account — an account this image has never heard of, which is why the
# rune leaves docker.user unset and lets the panel's /etc/passwd shim handle it.
# Everything Claude Code writes (credentials, config, sessions, cloned repos) has
# to land under /data or it is gone on the next restart.
ENV HOME=/data \
    NPM_CONFIG_CACHE=/data/.npm \
    XDG_CONFIG_HOME=/data/.config \
    GIT_CONFIG_GLOBAL=/data/.gitconfig
WORKDIR /data

COPY start.sh /usr/local/bin/claude-rc-start
RUN chmod 0755 /usr/local/bin/claude-rc-start

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/claude-rc-start"]
