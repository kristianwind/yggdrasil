# Claude connector (MCP)

Yggdrasil speaks the [Model Context Protocol](https://modelcontextprotocol.io), so Claude can
read and drive the panel in conversation — *"is anything down?"*, *"show me the last hundred lines
from the Minecraft server"*, *"restart it"*. It is one endpoint, `/api/mcp`, and adding it takes a
URL and one click of **Allow**.

Claude acts as **you**: the connection is bound to the account that approved it and gets exactly
that account's permissions. Everything it does lands in the audit log next to everything you do by
hand.

## Adding it

1. In the panel: **Settings → System → Claude connector**. Copy the URL — it looks like
   `https://panel.example.com/api/mcp`.
2. In Claude: **Settings → Connectors → Add custom connector**, paste the URL, and add it.
3. Claude opens this panel in your browser and asks you to approve the connection. If the page says
   you are not signed in, open the panel in another tab, sign in, and press **Allow** again — the
   panel's session cookie is `SameSite=Strict`, so it does not travel on the first arrival from
   claude.ai, only on the same-site press of the button.
4. Done. Ask Claude what your servers are doing.

**The panel must be reachable from the internet over HTTPS.** Claude's own servers make the
connection, not your browser, so a LAN address, a `.lab` hostname or a VPN-only panel cannot work —
the Settings card says so when it detects one. For a panel that stays private, use an
[API token](users-and-permissions.md) with an MCP client running on your own machine instead.

## What Claude can do

| Tool | What it does |
|---|---|
| `list_servers` | Every server you can see, with status, rune, ports and realm |
| `get_server` | One server in full — status, ports, limits, install state, rune version |
| `server_logs` | The tail of a server's container log (default 100 lines, max 500) |
| `start_server` | Start a stopped server |
| `stop_server` | Stop a running server — disconnects anyone connected |
| `restart_server` | Restart now, recreating the container so rune/env changes apply |
| `panel_status` | Panel version and how many servers are running, stopped or in trouble |

Tools take a server's **name** as shown in the panel, so you can say "restart Bimmelim" rather than
quoting an id. A name that matches two servers is refused rather than guessed at.

The destructive ones stop there on purpose. Creating servers, editing files, deleting anything,
changing settings and installing runes are not exposed — those want the panel in front of you.

## Safety

- **Claude asks you before every call.** The connector tools are subject to Claude's own
  confirmation prompts, and the stop/restart tools say plainly in their description that they
  disconnect players, so a model has reason to check first.
- **Permissions are yours, not more.** A delegate who can only view their own realm's servers
  connects a Claude that can only do that. See [Users & permissions](users-and-permissions.md).
- **Revoke any time** from the same Settings card. Disconnecting invalidates the tokens
  immediately; the client can only come back by asking you to approve again.
- **Audited.** Every start, stop and restart is written to the audit log with the user who approved
  the connection, exactly as a click would be.

## How the connection is authorized

Yggdrasil is its own OAuth 2.1 authorization server — there is no third party in the flow and
nothing to configure. Claude registers itself
([RFC 7591](https://datatracker.ietf.org/doc/html/rfc7591)), discovers the panel through
`/.well-known/oauth-protected-resource`
([RFC 9728](https://datatracker.ietf.org/doc/html/rfc9728)), sends you to the consent screen with
PKCE, and exchanges the resulting code for a token bound to this panel's MCP endpoint. Tokens are
stored hashed, are rejected if presented to any other address, and refresh tokens rotate on every
use.

The MCP endpoint itself is Streamable HTTP: JSON-RPC in a POST, one JSON object back. There is no
server-initiated stream and no session id, so `GET` and `DELETE` answer `405` — which is how the
transport spec says to advertise exactly that.

## When it does not connect

| What you see | What it means |
|---|---|
| Claude cannot reach the server | The panel is not reachable from the internet, or a firewall blocks Anthropic's addresses. The Settings card flags a local-only address. |
| The approval page says you are not signed in | Sign in to the panel in another tab, then press **Allow** again. |
| Connected, but no servers listed | The approving account cannot see any servers — check its realm grants. |
| A tool answers "no server called …" | Use the name exactly as the panel shows it; `list_servers` prints them. |
