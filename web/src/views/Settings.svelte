<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let targets = $state([]);
  let showCreate = $state(false);
  let form = $state(blank());

  // Two-factor auth
  let twofa = $state({ enabled: false });
  let twofaSetup = $state(null); // { secret, uri }
  let twofaCode = $state("");

  async function load2fa() {
    try {
      twofa = await api.get("/auth/2fa");
    } catch (e) {
      /* ignore */
    }
  }
  async function start2fa() {
    try {
      twofaSetup = await api.post("/auth/2fa/setup");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function enable2fa() {
    try {
      await api.post("/auth/2fa/enable", { code: twofaCode });
      toast("2FA enabled", "success");
      twofaSetup = null;
      twofaCode = "";
      await load2fa();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function disable2fa() {
    const c = prompt("Enter a current 2FA code to disable:");
    if (c === null) return;
    try {
      await api.post("/auth/2fa/disable", { code: c });
      toast("2FA disabled", "success");
      await load2fa();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // Message templates
  let templates = $state([]);
  let editing = $state(null); // { id?, name, body }

  // Steam authorization
  let steam = $state(null);
  let steamForm = $state({ username: "", password: "", guard_code: "" });
  let steamStep = $state(1); // 1 = credentials, 2 = guard code
  let steamBusy = $state(false);

  async function loadSteam() {
    try {
      steam = await api.get("/steam/account");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  // Step 1: trigger Steam to email a Guard code (login without a code).
  async function sendSteamCode() {
    if (!steamForm.username || !steamForm.password) return toast("Username and password required", "warn");
    steamBusy = true;
    try {
      const res = await api.post("/steam/send-code", {
        username: steamForm.username,
        password: steamForm.password,
      });
      if (res.status === "no_guard_needed") {
        await authorizeSteam(); // no Guard — finish immediately
      } else {
        steamStep = 2;
        toast("Steam Guard code sent to your account's email", "info");
      }
    } catch (e) {
      toast(e.message, "error");
    } finally {
      steamBusy = false;
    }
  }
  // Step 2: complete authorization with the emailed code.
  async function authorizeSteam() {
    steamBusy = true;
    try {
      await api.post("/steam/authorize", steamForm);
      toast("Steam account authorized", "success");
      steamForm = { username: "", password: "", guard_code: "" };
      steamStep = 1;
      await loadSteam();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      steamBusy = false;
    }
  }
  async function forgetSteam() {
    if (!confirm("Forget the Steam account? (The cached login is kept on disk so re-adding won't re-trigger Steam Guard.)")) return;
    try {
      await api.del("/steam/account");
      await loadSteam();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // API tokens
  let tokens = $state([]);
  let newTokenName = $state("");
  let createdToken = $state(null); // plaintext shown once

  // Notifications
  let channels = $state([]);
  let showNotify = $state(false);
  let notifyForm = $state({
    type: "telegram",
    token: "",
    chat_id: "",
    url: "",
    host: "",
    port: 587,
    username: "",
    password: "",
    from: "",
    to: "",
  });

  async function loadTokens() {
    try {
      tokens = await api.get("/tokens");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function createToken() {
    if (!newTokenName) return toast("Name required", "warn");
    try {
      const res = await api.post("/tokens", { name: newTokenName });
      createdToken = res.token;
      newTokenName = "";
      await loadTokens();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function deleteToken(t) {
    if (!confirm(`Delete token "${t.name}"?`)) return;
    try {
      await api.del(`/tokens/${t.id}`);
      await loadTokens();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function loadChannels() {
    try {
      channels = await api.get("/notifications");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function createChannel() {
    try {
      await api.post("/notifications", notifyForm);
      toast("Channel added", "success");
      showNotify = false;
      notifyForm = { type: "telegram", token: "", chat_id: "", url: "", host: "", port: 587, username: "", password: "", from: "", to: "" };
      await loadChannels();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function testChannel(c) {
    try {
      await api.post(`/notifications/${c.id}/test`);
      toast("Test sent", "success");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function deleteChannel(c) {
    if (!confirm("Delete this channel?")) return;
    try {
      await api.del(`/notifications/${c.id}`);
      await loadChannels();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function loadTemplates() {
    try {
      templates = await api.get("/templates");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function saveTemplate() {
    if (!editing.name || !editing.body) return toast("Name and body required", "warn");
    try {
      await api.post("/templates", editing);
      toast("Template saved", "success");
      editing = null;
      await loadTemplates();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function deleteTemplate(t) {
    if (!confirm(`Delete template "${t.name}"?`)) return;
    try {
      await api.del(`/templates/${t.id}`);
      await loadTemplates();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function blank() {
    return {
      name: "",
      type: "local",
      path: "",
      host: "",
      port: 0,
      username: "",
      password: "",
      share: "",
      keep_n: 0,
      keep_days: 0,
    };
  }

  async function load() {
    try {
      targets = await api.get("/backup/targets");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  // Network (public hostname / connect address)
  let network = $state({ public_hostname: "", detected: "", effective: "" });
  let savingNetwork = $state(false);
  async function loadNetwork() {
    try {
      network = await api.get("/settings/network");
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveNetwork() {
    savingNetwork = true;
    try {
      const res = await api.put("/settings/network", {
        public_hostname: network.public_hostname,
        upnp_enabled: !!network.upnp_enabled,
      });
      network = { ...network, ...res };
      toast("Network settings saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingNetwork = false;
    }
  }
  let upnpStatus = $state(null);
  let checkingUpnp = $state(false);
  async function checkUpnp() {
    checkingUpnp = true;
    try {
      upnpStatus = await api.get("/upnp/status");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      checkingUpnp = false;
    }
  }

  onMount(() => {
    load();
    loadTemplates();
    loadTokens();
    loadChannels();
    loadSteam();
    load2fa();
    loadNetwork();
  });

  async function create() {
    if (!form.name) return toast("Name required", "warn");
    try {
      await api.post("/backup/targets", { ...form, port: Number(form.port) || 0 });
      toast("Target created", "success");
      showCreate = false;
      form = blank();
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function test(t) {
    try {
      await api.post(`/backup/targets/${t.id}/test`);
      toast(`${t.name}: reachable`, "success");
    } catch (e) {
      toast(`${t.name}: ${e.message}`, "error");
    }
  }

  async function del(t) {
    if (!confirm(`Delete target "${t.name}"?`)) return;
    try {
      await api.del(`/backup/targets/${t.id}`);
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
</script>

<h1 class="text-2xl font-semibold mb-6">Settings</h1>

<!-- Network -->
<h2 class="text-xl font-semibold mb-2">Network</h2>
<p class="text-muted mb-4 text-sm">The public hostname players use to connect. It's shown as the connect address on each server's page.</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div>
    <label class="label" for="pubhost">Public hostname</label>
    <input id="pubhost" class="input" bind:value={network.public_hostname} placeholder="games.example.com" />
    <p class="text-muted text-xs mt-1">
      Leave empty to auto-detect your external IP{#if network.detected}
        (currently <span class="font-mono">{network.detected}</span>){/if}. Servers show
      <span class="font-mono">{network.effective || "your-host"}:&lt;port&gt;</span> as the connect address.
    </p>
  </div>
  <div class="border-t border-border pt-3">
    <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
      <input type="checkbox" bind:checked={network.upnp_enabled} />
      Automatic UPnP port forwarding
    </label>
    <p class="text-muted text-xs mt-1">
      When a server starts, ask the router (via UPnP-IGD) to forward its ports, and release them on stop.
      Off by default — many routers (incl. UniFi) ship with UPnP disabled. If unavailable, forward ports manually.
    </p>
    <div class="flex items-center gap-2 mt-2">
      <button class="btn-ghost px-2 py-1 text-xs" onclick={checkUpnp} disabled={checkingUpnp}>
        {checkingUpnp ? "Checking…" : "Check gateway"}
      </button>
      {#if upnpStatus}
        {#if upnpStatus.gateway}
          <span class="text-xs text-accent">✓ Gateway found{#if upnpStatus.external_ip} · WAN {upnpStatus.external_ip}{/if}</span>
        {:else}
          <span class="text-xs text-muted">No UPnP gateway ({upnpStatus.message || "router UPnP off"})</span>
        {/if}
      {/if}
    </div>
  </div>
  <button class="btn-primary" onclick={saveNetwork} disabled={savingNetwork}>
    {savingNetwork ? "Saving…" : "Save"}
  </button>
</div>

<!-- Two-factor auth -->
<h2 class="text-xl font-semibold mb-2">Two-factor authentication</h2>
<p class="text-muted mb-4 text-sm">Protect your account with a TOTP authenticator app.</p>
<div class="card p-4 mb-10">
  {#if twofa.enabled}
    <div class="flex items-center gap-3">
      <span class="badge bg-accent2/20 text-accent">enabled</span>
      <span class="flex-1 text-sm text-muted">A code from your authenticator is required at login.</span>
      <button class="btn-danger" onclick={disable2fa}>Disable</button>
    </div>
  {:else if twofaSetup}
    <p class="text-sm mb-2">
      Add this secret to your authenticator app (or scan the otpauth URI), then enter a code to
      confirm:
    </p>
    <code class="block break-all text-xs bg-black/40 p-2 rounded mb-1">{twofaSetup.secret}</code>
    <code class="block break-all text-[10px] text-muted bg-black/30 p-2 rounded mb-3">{twofaSetup.uri}</code>
    <div class="flex gap-2">
      <input class="input font-mono tracking-widest" bind:value={twofaCode} placeholder="123456" inputmode="numeric" />
      <button class="btn-primary" onclick={enable2fa}>Confirm & enable</button>
      <button class="btn-ghost" onclick={() => (twofaSetup = null)}>Cancel</button>
    </div>
  {:else}
    <div class="flex items-center gap-3">
      <span class="badge bg-border text-muted">disabled</span>
      <span class="flex-1 text-sm text-muted">Add a second factor to your login.</span>
      <button class="btn-primary" onclick={start2fa}>Enable 2FA</button>
    </div>
  {/if}
</div>

<!-- Steam authorization -->
<h2 class="text-xl font-semibold mb-2">Steam account</h2>
<p class="text-muted mb-4 text-sm">
  Most dedicated servers download anonymously. A few (e.g. <b>DayZ</b>) need a Steam account that
  owns the game. Authorize one <b>once</b> here — the login is cached so Steam Guard isn't asked
  again. Tip: use a dedicated account for the host and <b>email-based</b> Steam Guard (the mobile
  authenticator asks for a new code every login and breaks unattended updates). Your password and
  code are never stored or logged.
</p>
<div class="card p-4 mb-10">
  {#if steam?.configured}
    <div class="flex items-center gap-3">
      <div class="flex-1">
        <div class="font-medium">{steam.username}
          <span class="badge {steam.authorized ? 'bg-accent2/20 text-accent' : 'bg-warn/20 text-warn'} ml-1">
            {steam.authorized ? "authorized" : "not authorized"}
          </span>
        </div>
        {#if steam.authorized_at}<div class="text-xs text-muted">since {steam.authorized_at}</div>{/if}
      </div>
      <button class="btn-danger" onclick={forgetSteam}>Forget</button>
    </div>
  {:else if steamStep === 1}
    <div class="text-xs text-muted mb-2">
      Step 1 of 2 — enter your credentials. Steam will email a Guard code to the account.
    </div>
    <div class="grid sm:grid-cols-2 gap-3">
      <div>
        <label class="label" for="st-user">Username</label>
        <input id="st-user" class="input" bind:value={steamForm.username} autocomplete="off" />
      </div>
      <div>
        <label class="label" for="st-pass">Password</label>
        <input id="st-pass" class="input" type="password" bind:value={steamForm.password} autocomplete="off" />
      </div>
    </div>
    <button class="btn-primary mt-3" onclick={sendSteamCode} disabled={steamBusy}>
      {steamBusy ? "Contacting Steam… (can take a minute)" : "Send Steam Guard code"}
    </button>
  {:else}
    <div class="text-xs text-muted mb-2">
      Step 2 of 2 — enter the Steam Guard code from <b>{steamForm.username}</b>'s email.
    </div>
    <div class="max-w-xs">
      <label class="label" for="st-guard">Steam Guard code</label>
      <input id="st-guard" class="input font-mono tracking-widest" bind:value={steamForm.guard_code}
        autocomplete="off" placeholder="XXXXX" />
    </div>
    <div class="flex gap-2 mt-3">
      <button class="btn-primary" onclick={authorizeSteam} disabled={steamBusy}>
        {steamBusy ? "Authorizing…" : "Authorize"}
      </button>
      <button class="btn-ghost" onclick={() => { steamStep = 1; steamForm.guard_code = ''; }}>Back</button>
    </div>
  {/if}
</div>

<div class="flex items-center justify-between mb-2">
  <h2 class="text-xl font-semibold">Backup targets</h2>
  <button class="btn-primary" onclick={() => (showCreate = true)}>+ New target</button>
</div>
<p class="text-muted mb-4 text-sm">
  Where backups are stored. <b>Local</b> also covers an NFS or CIFS share already mounted on the
  host — just point the path at the mountpoint. <b>SFTP</b> and <b>SMB</b> connect directly.
  Credentials are encrypted at rest.
</p>

<div class="card divide-y divide-border">
  {#if targets.length === 0}
    <div class="p-4 text-muted text-sm">No backup targets yet.</div>
  {/if}
  {#each targets as t}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1">
        <div class="font-medium">{t.name} <span class="badge bg-border text-muted ml-1">{t.type}</span></div>
        <div class="text-xs text-muted">
          {t.host ? t.host + " · " : ""}{t.path || "(default)"}
          {#if t.keep_n || t.keep_days}
            · retention: {t.keep_n ? `keep ${t.keep_n}` : ""}{t.keep_n && t.keep_days ? " / " : ""}{t.keep_days
              ? `${t.keep_days}d`
              : ""}
          {/if}
        </div>
      </div>
      <button class="btn-ghost" onclick={() => test(t)}>Test</button>
      <button class="btn-danger" onclick={() => del(t)}>Delete</button>
    </div>
  {/each}
</div>

<!-- Notifications -->
<div class="flex items-center justify-between mt-10 mb-2">
  <h2 class="text-xl font-semibold">Notifications</h2>
  <button class="btn-primary" onclick={() => (showNotify = true)}>+ Add channel</button>
</div>
<p class="text-muted mb-4 text-sm">
  Get notified on backups (done/failed) and servers starting/stopping, via Telegram, Discord, or a
  generic webhook. Tokens are encrypted at rest.
</p>
<div class="card divide-y divide-border">
  {#if channels.length === 0}
    <div class="p-4 text-muted text-sm">No notification channels.</div>
  {/if}
  {#each channels as c}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1 font-medium capitalize">{c.type}</div>
      <button class="btn-ghost" onclick={() => testChannel(c)}>Test</button>
      <button class="btn-danger" onclick={() => deleteChannel(c)}>Delete</button>
    </div>
  {/each}
</div>

{#if showNotify}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-md p-5 space-y-3">
      <h2 class="text-lg font-semibold">Add notification channel</h2>
      <div>
        <label class="label" for="n-type">Type</label>
        <select id="n-type" class="input" bind:value={notifyForm.type}>
          <option value="telegram">Telegram</option>
          <option value="discord">Discord webhook</option>
          <option value="webhook">Generic webhook</option>
          <option value="email">Email (SMTP)</option>
        </select>
      </div>
      {#if notifyForm.type === "telegram"}
        <div>
          <label class="label" for="n-token">Bot token</label>
          <input id="n-token" class="input" bind:value={notifyForm.token} />
        </div>
        <div>
          <label class="label" for="n-chat">Chat ID</label>
          <input id="n-chat" class="input" bind:value={notifyForm.chat_id} />
        </div>
      {:else if notifyForm.type === "email"}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="n-host">SMTP host</label>
            <input id="n-host" class="input" bind:value={notifyForm.host} />
          </div>
          <div>
            <label class="label" for="n-port">Port</label>
            <input id="n-port" class="input" type="number" bind:value={notifyForm.port} />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="n-user">Username</label>
            <input id="n-user" class="input" bind:value={notifyForm.username} autocomplete="off" />
          </div>
          <div>
            <label class="label" for="n-pass">Password</label>
            <input id="n-pass" class="input" type="password" bind:value={notifyForm.password} autocomplete="off" />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="n-from">From</label>
            <input id="n-from" class="input" bind:value={notifyForm.from} placeholder="ygg@example.com" />
          </div>
          <div>
            <label class="label" for="n-to">To</label>
            <input id="n-to" class="input" bind:value={notifyForm.to} placeholder="you@example.com" />
          </div>
        </div>
      {:else}
        <div>
          <label class="label" for="n-url">Webhook URL</label>
          <input id="n-url" class="input" bind:value={notifyForm.url} />
        </div>
      {/if}
      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showNotify = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={createChannel}>Add</button>
      </div>
    </div>
  </div>
{/if}

<!-- API tokens -->
<div class="flex items-center justify-between mt-10 mb-2">
  <h2 class="text-xl font-semibold">API tokens</h2>
</div>
<p class="text-muted mb-4 text-sm">
  Drive Yggdrasil from automation (scripts, a home AI). A token acts as you and is shown only once.
</p>
{#if createdToken}
  <div class="card border-accent2/40 bg-accent2/10 p-4 mb-3">
    <div class="text-sm text-accent mb-1">New token — copy it now, it won't be shown again:</div>
    <code class="block break-all text-xs bg-black/40 p-2 rounded">{createdToken}</code>
    <button class="btn-ghost mt-2" onclick={() => (createdToken = null)}>Dismiss</button>
  </div>
{/if}
<div class="flex gap-2 mb-3">
  <input class="input" bind:value={newTokenName} placeholder="Token name (e.g. home-ai)" />
  <button class="btn-primary" onclick={createToken}>Create</button>
</div>
<div class="card divide-y divide-border">
  {#if tokens.length === 0}
    <div class="p-4 text-muted text-sm">No API tokens.</div>
  {/if}
  {#each tokens as t}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1">
        <div class="font-medium">{t.name}</div>
        <div class="text-xs text-muted">
          created {t.created_at}{t.last_used_at ? ` · last used ${t.last_used_at}` : " · never used"}
        </div>
      </div>
      <button class="btn-danger" onclick={() => deleteToken(t)}>Delete</button>
    </div>
  {/each}
</div>

<!-- Message templates -->
<div class="flex items-center justify-between mt-10 mb-2">
  <h2 class="text-xl font-semibold">In-game message templates</h2>
  <button class="btn-primary" onclick={() => (editing = { name: "", body: "" })}>+ New template</button>
</div>
<p class="text-muted mb-4 text-sm">
  Used by scheduled "in-game message" tasks. Variables like <code>{"{{minutes}}"}</code> and
  <code>{"{{server_name}}"}</code> are substituted at send time. The body is the full console/RCON
  command (e.g. <code>say …</code>).
</p>

<div class="card divide-y divide-border">
  {#each templates as t}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1 min-w-0">
        <div class="font-medium">{t.name} {#if t.builtin}<span class="badge bg-border text-muted ml-1">built-in</span>{/if}</div>
        <div class="text-xs text-muted font-mono truncate">{t.body}</div>
      </div>
      <button class="btn-ghost" onclick={() => (editing = { id: t.id, name: t.name, body: t.body })}>Edit</button>
      {#if !t.builtin}
        <button class="btn-danger" onclick={() => deleteTemplate(t)}>Delete</button>
      {/if}
    </div>
  {/each}
</div>

{#if editing}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-lg p-5 space-y-3">
      <h2 class="text-lg font-semibold">{editing.id ? "Edit" : "New"} template</h2>
      <div>
        <label class="label" for="m-name">Name</label>
        <input id="m-name" class="input" bind:value={editing.name} />
      </div>
      <div>
        <label class="label" for="m-body">Body (console/RCON command)</label>
        <input id="m-body" class="input font-mono" bind:value={editing.body} placeholder="say Restarting in {{minutes}} min" />
      </div>
      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (editing = null)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={saveTemplate}>Save</button>
      </div>
    </div>
  </div>
{/if}

{#if showCreate}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-lg max-h-[90vh] overflow-auto p-5 space-y-3">
      <h2 class="text-lg font-semibold">New backup target</h2>
      <div>
        <label class="label" for="t-name">Name</label>
        <input id="t-name" class="input" bind:value={form.name} />
      </div>
      <div>
        <label class="label" for="t-type">Type</label>
        <select id="t-type" class="input" bind:value={form.type}>
          <option value="local">Local / NFS / CIFS mount</option>
          <option value="sftp">SFTP</option>
          <option value="smb">SMB / CIFS (direct)</option>
        </select>
      </div>

      {#if form.type !== "local"}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="t-host">Host</label>
            <input id="t-host" class="input" bind:value={form.host} />
          </div>
          <div>
            <label class="label" for="t-port">Port (optional)</label>
            <input id="t-port" class="input" type="number" bind:value={form.port} />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="t-user">Username</label>
            <input id="t-user" class="input" bind:value={form.username} />
          </div>
          <div>
            <label class="label" for="t-pass">Password</label>
            <input id="t-pass" class="input" type="password" bind:value={form.password} />
          </div>
        </div>
      {/if}

      {#if form.type === "smb"}
        <div>
          <label class="label" for="t-share">Share name</label>
          <input id="t-share" class="input" bind:value={form.share} />
        </div>
      {/if}

      <div>
        <label class="label" for="t-path">{form.type === "local" ? "Directory path" : "Remote path"}</label>
        <input id="t-path" class="input" bind:value={form.path} placeholder={form.type === "local" ? "/mnt/backups" : "backups"} />
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="t-keepn">Keep latest N (0 = ∞)</label>
          <input id="t-keepn" class="input" type="number" bind:value={form.keep_n} />
        </div>
        <div>
          <label class="label" for="t-keepd">Keep days (0 = ∞)</label>
          <input id="t-keepd" class="input" type="number" bind:value={form.keep_days} />
        </div>
      </div>

      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showCreate = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={create}>Create target</button>
      </div>
    </div>
  </div>
{/if}
