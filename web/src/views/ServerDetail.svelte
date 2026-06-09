<script>
  import { onMount, onDestroy } from "svelte";
  import { api, wsURL } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { navigate } from "../lib/router.js";
  import { user } from "../lib/auth.js";
  import FileManager from "../components/FileManager.svelte";
  import VarForm from "../components/VarForm.svelte";

  let { id } = $props();

  let server = $state(null);
  let tab = $state("console");
  let stats = $state(null);
  let status = $state(null); // player-count query result
  let statsTimer;

  // Console
  let lines = $state([]);
  let cmd = $state("");
  let ws = $state(null);
  let termEl;

  // Install
  let installLines = $state([]);
  let installWs = $state(null);
  let installEl;

  // Backups
  let backups = $state([]);
  let backupTargets = $state([]);
  let selectedTarget = $state("");
  let backupBusy = $state(false);

  async function loadBackups() {
    try {
      [backups, backupTargets] = await Promise.all([
        api.get(`/servers/${id}/backups`),
        api.get("/backup/targets").catch(() => []),
      ]);
      if (!selectedTarget && backupTargets[0]) selectedTarget = backupTargets[0].id;
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function runBackup() {
    if (!selectedTarget) return toast("Create a backup target in Settings first", "warn");
    backupBusy = true;
    try {
      await api.post(`/servers/${id}/backup`, { target_id: selectedTarget });
      toast("Backup started", "info");
      setTimeout(loadBackups, 1500);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      backupBusy = false;
    }
  }

  async function restoreBackup(b) {
    if (!confirm("Restore this backup? The server will be stopped and files overwritten.")) return;
    try {
      await api.post(`/backups/${b.id}/restore`);
      toast("Restored", "success");
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function deleteBackup(b) {
    if (!confirm("Delete this backup?")) return;
    try {
      await api.del(`/backups/${b.id}`);
      await loadBackups();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function fmtSize(n) {
    if (!n) return "—";
    const u = ["B", "KB", "MB", "GB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n.toFixed(1)} ${u[i]}`;
  }
  // Readable local date/time for a backup's created_at (falls back to the raw value).
  function fmtDate(s) {
    if (!s) return "—";
    const d = new Date(s);
    return isNaN(d) ? s : d.toLocaleString();
  }
  // The backup's storage name (the file's basename, e.g. 20260602-150405.tar.gz).
  function backupName(b) {
    const p = (b.path || "").split("/");
    return p[p.length - 1] || b.id;
  }

  // can(perm) — does the caller hold this permission on this server? The API
  // attaches `perms` (effective permissions; admins get all). Drives which tabs
  // and action buttons a delegated user sees, so they never face a button that
  // would just 403.
  const can = (p) => server?.perms?.includes(p) ?? false;

  // Tabs are filtered to what the caller may actually do (Norn/Mods are DayZ-only
  // and need write access; Install log only needs view).
  let tabs = $derived(
    [
      ...(can("server.console") ? [["console", "Console"]] : []),
      ...(can("server.files") ? [["files", "Files"]] : []),
      ...(can("server.backup") ? [["backups", "Backups"]] : []),
      ...(can("server.control") ? [["settings", "Settings"]] : []),
      ...(can("server.files") ? [["anticheat", "Anti-cheat"]] : []),
      ...(server?.gameskill_id === "dayz" && can("server.control")
        ? [["mods", "Mods"], ["norn", "Norn (loot)"]]
        : []),
      ["install", "Install log"],
    ],
  );

  // Keep the active tab valid as perms/tabs resolve — if the current tab isn't
  // available to this user, fall back to the first one they can see.
  $effect(() => {
    if (tabs.length && !tabs.some(([k]) => k === tab)) {
      tab = tabs[0][0];
    }
  });
  let economy = $state(null);
  let modLoot = $state(null);
  let nornBusy = $state(false);
  let minHours = $state(4);
  let globalsEdit = $state({});
  function fmtDur(sec) {
    if (sec == null || sec < 0) return "—";
    if (sec < 3600) return `${Math.round(sec / 60)} min`;
    if (sec < 86400) return `${(sec / 3600).toFixed(1)} h`;
    return `${(sec / 86400).toFixed(1)} d`;
  }
  async function loadEconomy() {
    [economy, modLoot] = await Promise.all([
      api.get(`/servers/${id}/dayz/economy`).catch(() => null),
      api.get(`/servers/${id}/dayz/mod-loot`).catch(() => null),
    ]);
    globalsEdit = { ...(economy?.globals || {}) };
  }
  async function importModTypes(path) {
    nornBusy = true;
    try {
      const r = await api.post(`/servers/${id}/dayz/import-mod-types`, { path });
      toast(`Imported ${r.imported} into the economy — set a floor + restart`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function applyMinLifetime() {
    if (!(minHours > 0)) return toast("Enter hours > 0", "warn");
    nornBusy = true;
    try {
      const r = await api.post(`/servers/${id}/dayz/min-lifetime`, { hours: Number(minHours) });
      toast(`Raised ${r.changed} item lifetimes to ≥ ${minHours} h — restart to apply`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function saveGlobals() {
    nornBusy = true;
    try {
      const payload = {};
      for (const [k, v] of Object.entries(globalsEdit)) payload[k] = Number(v) || 0;
      const r = await api.post(`/servers/${id}/dayz/globals`, payload);
      toast(`Updated ${r.changed} cleanup timer(s) — restart to apply`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function resetNorn() {
    if (!confirm("Forget all saved Norn settings? Loot files revert to vanilla on the next Update/Reinstall.")) return;
    nornBusy = true;
    try {
      await api.post(`/servers/${id}/dayz/reset`, {});
      toast("Norn settings cleared — Update/Reinstall to restore vanilla loot", "info");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function registerTypes() {
    nornBusy = true;
    try {
      const r = await api.post(`/servers/${id}/dayz/register-types`, {});
      toast(`Registered ${r.registered} modded types file(s) — apply a lifetime floor + restart`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }

  // --- Mod control: which configured Workshop mods are installed / still exist ---
  let modStatus = $state(null);
  let modsBusy = $state(false);
  async function loadMods() {
    modStatus = await api.get(`/servers/${id}/dayz/mods`).catch(() => null);
  }
  async function setMods(ids) {
    modsBusy = true;
    try {
      await api.put(`/servers/${id}`, { mods: ids.join(";") });
      toast("Mod list updated — press Update/Reinstall to download + apply", "success");
      await Promise.all([loadServer(), loadMods()]);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modsBusy = false;
    }
  }
  function removeMod(modId) {
    const keep = (modStatus?.mods || []).map((m) => m.id).filter((x) => x !== modId);
    return setMods(keep);
  }
  function addOrphan(modId) {
    const cur = (modStatus?.mods || []).map((m) => m.id);
    return setMods([...cur, modId]);
  }
  function pruneBroken() {
    const broken = (modStatus?.mods || []).filter((m) => !m.installed || m.workshop === "removed");
    if (!broken.length) return;
    if (!confirm(`Remove ${broken.length} missing/removed mod(s) from the load order? Press Update/Reinstall afterwards.`)) return;
    const keep = (modStatus?.mods || []).filter((m) => m.installed && m.workshop !== "removed").map((m) => m.id);
    return setMods(keep);
  }

  let skill = $state(null); // parsed gameskill (for anti-cheat surface + edit form)

  // Edit settings
  let edit = $state(null); // { name, env, cpu_percent, memory_mb }
  let savingEdit = $state(false);
  function openEdit() {
    edit = {
      name: server.name,
      env: { ...(server.env || {}) },
      cpu_percent: server.cpu_percent || 0,
      memory_mb: server.memory_mb || 0,
      bm_server_id: server.bm_server_id || "",
      auto_forward: server.auto_forward !== false,
      subdomain: server.subdomain || "",
    };
  }
  async function saveEdit() {
    savingEdit = true;
    try {
      const env = {};
      for (const [k, v] of Object.entries(edit.env)) env[k] = String(v);
      await api.put(`/servers/${id}`, {
        name: edit.name,
        env,
        cpu_percent: Number(edit.cpu_percent) || 0,
        memory_mb: Number(edit.memory_mb) || 0,
        bm_server_id: edit.bm_server_id || "",
        auto_forward: !!edit.auto_forward,
        subdomain: edit.subdomain || "",
      });
      toast("Saved — restart to apply (reinstall for file-baked values)", "success");
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingEdit = false;
    }
  }

  // --- Per-server user delegation (admin only) ---
  // The permissions that make sense to grant on a single server.
  const DELEGATE_PERMS = [
    ["server.view", "View"],
    ["server.control", "Start / Stop"],
    ["server.console", "Console / RCON"],
    ["server.files", "Files"],
    ["server.backup", "Backups"],
    ["server.schedule", "Schedules"],
    ["server.delete", "Delete"],
  ];
  let allUsers = $state([]);
  let delegates = $state([]); // [{ user_id, username, role, perms: [] }]
  let savingDelegates = $state(false);
  async function loadDelegation() {
    if ($user?.role !== "admin") return;
    try {
      const [users, dels] = await Promise.all([
        api.get("/users"),
        api.get(`/servers/${id}/delegates`),
      ]);
      // Only non-admin users are delegable (admins already have full access).
      allUsers = users.filter((u) => u.role !== "admin");
      delegates = dels;
    } catch (e) {
      toast(e.message, "error");
    }
  }
  function addDelegate(userId) {
    if (!userId) return;
    if (delegates.some((d) => d.user_id === userId)) return;
    const u = allUsers.find((x) => x.id === userId);
    if (!u) return;
    delegates = [...delegates, { user_id: u.id, username: u.username, role: u.role, perms: ["server.view"] }];
  }
  function toggleDelegatePerm(userId, perm) {
    delegates = delegates.map((d) => {
      if (d.user_id !== userId) return d;
      const has = d.perms.includes(perm);
      return { ...d, perms: has ? d.perms.filter((p) => p !== perm) : [...d.perms, perm] };
    });
  }
  function removeDelegate(userId) {
    delegates = delegates.filter((d) => d.user_id !== userId);
  }
  async function saveDelegates() {
    savingDelegates = true;
    try {
      // Drop any delegate with no permissions (equivalent to removing access).
      const payload = delegates
        .filter((d) => d.perms.length > 0)
        .map((d) => ({ user_id: d.user_id, perms: d.perms }));
      await api.put(`/servers/${id}/delegates`, payload);
      toast("Delegated access saved", "success");
      await loadDelegation();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingDelegates = false;
    }
  }
  let undelegatedUsers = $derived(allUsers.filter((u) => !delegates.some((d) => d.user_id === u.id)));

  // Public connect address (from network settings).
  let network = $state(null);
  async function loadNetwork() {
    try {
      network = await api.get("/settings/network");
    } catch (e) {
      /* non-fatal */
    }
  }
  let connectHost = $derived(network?.effective || "");

  // BattleMetrics live status (only when a BM id is configured on the server).
  let bm = $state(null);
  async function loadBM() {
    if (!server?.bm_server_id) {
      bm = null;
      return;
    }
    bm = await api.get(`/servers/${id}/battlemetrics`).catch(() => null);
  }

  // "Online from outside" — probes the server via its public address (see backend).
  let reach = $state(null);
  async function loadReach() {
    if (!server || (server.status !== "running" && server.status !== "starting")) {
      reach = null;
      return;
    }
    reach = await api.get(`/servers/${id}/reachability`).catch(() => null);
  }

  async function loadServer() {
    try {
      const prev = server;
      server = await api.get(`/servers/${id}`);
      // When an install finishes, refresh the console.
      if (prev && !prev.installed && server.installed) {
        toast("Install complete", "success");
      }
      if (!skill && server) {
        skill = await api.get(`/gameskills/${server.gameskill_id}`).catch(() => null);
      }
      loadBM();
      loadReach();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function connectInstallLog() {
    if (installWs) return;
    installLines = [];
    installWs = new WebSocket(wsURL(`/servers/${id}/install/log`));
    installWs.onmessage = (ev) => {
      installLines = [...installLines.slice(-2000), ev.data];
      queueMicrotask(() => {
        if (installEl) installEl.scrollTop = installEl.scrollHeight;
      });
      // Refresh server state when the install reports completion.
      if (/Install complete|Install FAILED/.test(ev.data)) {
        setTimeout(loadServer, 500);
      }
    };
    installWs.onclose = () => {
      installWs = null;
    };
  }

  async function runInstall(confirmFirst = false) {
    if (confirmFirst &&
      !confirm("Update / reinstall this server? It re-runs the install script to fetch the latest version. Back up your world first — config files may be regenerated."))
      return;
    try {
      await api.post(`/servers/${id}/install`);
      tab = "install";
      connectInstallLog();
      toast("Update / reinstall started", "info");
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function connectConsole() {
    closeWS();
    lines = [];
    ws = new WebSocket(wsURL(`/servers/${id}/console`));
    ws.onmessage = (ev) => {
      lines = [...lines.slice(-1000), ev.data];
      queueMicrotask(() => {
        if (termEl) termEl.scrollTop = termEl.scrollHeight;
      });
    };
    ws.onclose = () => {
      lines = [...lines, "[console disconnected]"];
    };
  }
  function closeWS() {
    if (ws) {
      ws.onclose = null;
      ws.close();
      ws = null;
    }
  }

  function sendCmd(e) {
    e.preventDefault();
    if (!cmd.trim() || !ws || ws.readyState !== 1) return;
    ws.send(cmd);
    lines = [...lines, `> ${cmd}`];
    cmd = "";
  }

  async function pollStats() {
    try {
      stats = await api.get(`/servers/${id}/stats`);
    } catch {
      stats = null;
    }
    try {
      status = await api.get(`/servers/${id}/query`);
    } catch {
      status = null;
    }
  }

  async function action(verb) {
    try {
      await api.post(`/servers/${id}/${verb}`);
      toast(verb, "success");
      await loadServer();
      if (verb !== "stop") setTimeout(connectConsole, 800);
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function del() {
    if (!confirm(`Delete "${server.name}"? This removes the container.`)) return;
    try {
      await api.del(`/servers/${id}`);
      toast("Server deleted", "success");
      navigate("/servers");
    } catch (e) {
      toast(e.message, "error");
    }
  }

  onMount(async () => {
    loadNetwork();
    await loadServer();
    if (server && !server.installed) {
      tab = "install";
      connectInstallLog();
    } else if ((server?.status === "running" || server?.status === "starting")) {
      connectConsole();
    }
    pollStats();
    statsTimer = setInterval(pollStats, 4000);
  });
  onDestroy(() => {
    closeWS();
    if (installWs) {
      installWs.onclose = null;
      installWs.close();
    }
    clearInterval(statsTimer);
  });
</script>

{#if !server}
  <div class="text-muted">Loading…</div>
{:else}
  <div class="flex items-center gap-3 mb-1">
    <button class="btn-ghost px-2 py-1" onclick={() => navigate("/servers")}>←</button>
    <h1 class="text-2xl font-semibold">{server.name}</h1>
    <span class="badge {server.status === 'running' ? 'bg-accent2/20 text-accent' : server.status === 'starting' ? 'bg-warn/20 text-warn' : 'bg-border text-muted'}"
      >{server.status}</span
    >
    {#if reach}
      <span
        class="badge {reach.reachable ? 'bg-accent2/20 text-accent' : 'bg-warn/20 text-warn'}"
        title={reach.reachable
          ? `Responds from the internet on ${reach.host}:${reach.port}`
          : `No external reply on ${reach.host}:${reach.port} — check the port forward (or your router's NAT loopback)`}
      >
        {reach.reachable ? "🌐 reachable" : "🌐 not from outside"}
      </span>
    {/if}
    {#if bm && bm.configured}
      <a
        href={bm.url}
        target="_blank"
        rel="noopener"
        class="badge {bm.online ? 'bg-accent2/20 text-accent' : 'bg-border text-muted'}"
        title="BattleMetrics{bm.rank ? ` · rank #${bm.rank}` : ''}"
      >
        BM: {bm.online ? `online ${bm.players}/${bm.max_players}` : bm.status || "offline"}
      </a>
    {/if}
  </div>
  <div class="text-muted text-sm mb-4">{server.gameskill_id}</div>

  <!-- Controls + live stats (each gated on the caller's permissions) -->
  <div class="flex flex-wrap items-center gap-2 mb-4">
    {#if !server.installed}
      {#if can("server.control")}
        <button class="btn-primary" onclick={runInstall} disabled={server.install_status === "installing"}>
          {server.install_status === "installing" ? "Installing…" : server.install_status === "error" ? "Retry install" : "Install"}
        </button>
      {/if}
    {:else if (server.status === "running" || server.status === "starting")}
      {#if can("server.control")}
        <button class="btn-ghost" onclick={() => action("restart")}>Restart</button>
        <button class="btn-ghost" onclick={() => action("stop")}>Stop</button>
      {/if}
    {:else if can("server.control")}
      <button class="btn-primary" onclick={() => action("start")}>Start</button>
      <button class="btn-ghost" onclick={() => runInstall(true)}>Update / Reinstall</button>
    {/if}
    {#if can("server.delete")}
      <button class="btn-danger ml-auto" onclick={del}>Delete</button>
    {/if}
  </div>

  {#if !server.installed}
    <div class="card border-warn/40 bg-warn/10 text-warn text-sm px-4 py-2 mb-4">
      This server isn't installed yet. Click <b>Install</b> to download/build it; progress shows below.
    </div>
  {/if}

  {#if server.ports && Object.keys(server.ports).length}
    <div class="card p-3 mb-4">
      <div class="text-xs text-muted uppercase tracking-wide mb-1">Connect address</div>
      <div class="flex flex-wrap gap-2">
        {#each Object.entries(server.ports) as [name, port]}
          <span class="badge bg-panel2 border border-border font-mono text-xs">
            {name}: {connectHost || "your-host"}:{port}
          </span>
        {/each}
      </div>
      {#if !connectHost}
        <div class="text-muted text-xs mt-1">Set a public hostname in <a href="#/settings" class="underline">Settings → Network</a> to replace “your-host”.</div>
      {/if}
    </div>
  {/if}

  {#if stats && (server.status === "running" || server.status === "starting")}
    <div class="grid grid-cols-2 sm:grid-cols-3 gap-3 mb-4">
      <div class="card p-3">
        <div class="text-xs text-muted">CPU</div>
        <div class="text-lg font-semibold">{stats.cpu_percent?.toFixed(1)}%</div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-muted">Memory</div>
        <div class="text-lg font-semibold">{stats.mem_usage_mb?.toFixed(0)} MB</div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-muted">Players</div>
        <div class="text-lg font-semibold">
          {#if status && status.online}
            {status.players}{status.max_players ? ` / ${status.max_players}` : ""}
          {:else}
            —
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Tabs -->
  <div class="flex gap-1 border-b border-border mb-4">
    {#each tabs as [key, label]}
      <button
        class="px-4 py-2 text-sm border-b-2 -mb-px {tab === key
          ? 'border-accent text-text'
          : 'border-transparent text-muted hover:text-text'}"
        onclick={() => {
          tab = key;
          if (key === "install" && !installWs) connectInstallLog();
          if (key === "backups") loadBackups();
          if (key === "norn") loadEconomy();
          if (key === "mods") loadMods();
          if (key === "settings") {
            openEdit();
            loadDelegation();
          }
        }}>{label}</button
      >
    {/each}
  </div>

  {#if tab === "install"}
    <div bind:this={installEl} class="term h-[50vh]">
      {#if installLines.length === 0}
        <div class="text-muted">No install output yet. Click Install to begin.</div>
      {/if}
      {#each installLines as l}<div>{l}</div>{/each}
    </div>
  {:else if tab === "console"}
    <div bind:this={termEl} class="term h-[50vh]">
      {#each lines as l}<div>{l}</div>{/each}
    </div>
    <form onsubmit={sendCmd} class="flex gap-2 mt-3">
      <input
        class="input font-mono"
        bind:value={cmd}
        placeholder={(server.status === "running" || server.status === "starting") ? "Type a console command…" : "Server is stopped"}
        disabled={server.status !== "running"}
      />
      <button class="btn-primary" disabled={server.status !== "running"}>Send</button>
    </form>
    {#if (server.status === "running" || server.status === "starting") && (!ws || ws.readyState !== 1)}
      <button class="btn-ghost mt-2" onclick={connectConsole}>Reconnect console</button>
    {/if}
  {:else if tab === "files"}
    <FileManager serverId={id} />
  {:else if tab === "settings"}
    {#if edit}
      <div class="max-w-lg space-y-4">
        <div>
          <label class="label" for="e-name">Server name</label>
          <input id="e-name" class="input" bind:value={edit.name} />
        </div>
        {#if skill?.variables}
          <VarForm variables={skill.variables} bind:values={edit.env} />
        {/if}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="e-cpu">CPU limit (%, 0 = unlimited)</label>
            <input id="e-cpu" class="input" type="number" bind:value={edit.cpu_percent} />
          </div>
          <div>
            <label class="label" for="e-mem">RAM limit (MB, 0 = unlimited)</label>
            <input id="e-mem" class="input" type="number" bind:value={edit.memory_mb} />
          </div>
        </div>
        <div>
          <label class="label" for="e-bm">BattleMetrics server ID (optional)</label>
          <input id="e-bm" class="input" placeholder="e.g. 12345678" bind:value={edit.bm_server_id} />
          <p class="text-xs text-muted mt-1">
            Find your server on battlemetrics.com — the number in its URL. Shows a live
            online/players badge at the top of this page.
          </p>
        </div>
        <div>
          <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
            <input type="checkbox" bind:checked={edit.auto_forward} />
            Open firewall ports automatically (UPnP / UniFi)
          </label>
          <p class="text-xs text-muted mt-1">
            On by default. Turn off to keep this server LAN-only — its ports won't be forwarded
            on the router when it starts. Takes effect on the next start.
          </p>
        </div>
        {#if server.ports?.web}
          <div>
            <label class="label" for="e-sub">Subdomain (optional)</label>
            <input id="e-sub" class="input" placeholder="e.g. notes" bind:value={edit.subdomain} />
            <p class="text-xs text-muted mt-1">
              Routes <code>{edit.subdomain || "sub"}.&lt;your base domain&gt;</code> to this app via
              Nginx Proxy Manager or Cloudflare Tunnel (configure under Settings → Network). The route is
              created on start and removed on stop. You can also enter a full custom domain. Leave blank to disable.
            </p>
          </div>
        {/if}
        <div class="card bg-warn/10 border-warn/40 text-warn text-xs px-3 py-2">
          Changes apply on the next <b>restart</b>. Values written into config files at install time
          (e.g. RCON password, world seed) need a <b>Reinstall</b> to fully apply — back up your
          world first, as reinstall can regenerate config.
        </div>
        <div class="flex gap-2">
          <button class="btn-primary" onclick={saveEdit} disabled={savingEdit}>
            {savingEdit ? "Saving…" : "Save changes"}
          </button>
          <button class="btn-ghost" onclick={() => runInstall(true)}>Update / Reinstall</button>
        </div>
      </div>
    {/if}

    {#if $user?.role === "admin"}
      <div class="max-w-2xl mt-10 pt-6 border-t border-border">
        <h3 class="text-lg font-semibold mb-1">Delegated users</h3>
        <p class="text-muted text-sm mb-4">
          Give specific non-admin users access to <b>this server only</b>. Permissions here apply
          just to {server?.name}; the user's access to other servers is unaffected.
        </p>

        {#if delegates.length === 0}
          <div class="text-muted text-sm mb-3">No users are delegated to this server yet.</div>
        {/if}

        <div class="space-y-3">
          {#each delegates as d (d.user_id)}
            <div class="card p-3">
              <div class="flex items-center justify-between mb-2">
                <div class="font-medium">{d.username}</div>
                <button class="btn-ghost px-2 py-1 text-danger" onclick={() => removeDelegate(d.user_id)}>
                  Remove
                </button>
              </div>
              <div class="flex flex-wrap gap-x-4 gap-y-1.5">
                {#each DELEGATE_PERMS as [perm, label]}
                  <label class="inline-flex items-center gap-1.5 text-sm cursor-pointer">
                    <input
                      type="checkbox"
                      checked={d.perms.includes(perm)}
                      onchange={() => toggleDelegatePerm(d.user_id, perm)}
                    />
                    {label}
                  </label>
                {/each}
              </div>
            </div>
          {/each}
        </div>

        <div class="flex items-center gap-2 mt-4">
          <select
            class="input max-w-xs"
            disabled={undelegatedUsers.length === 0}
            onchange={(e) => {
              addDelegate(e.target.value);
              e.target.value = "";
            }}
          >
            <option value="">
              {undelegatedUsers.length ? "+ Add user…" : "No more users to add"}
            </option>
            {#each undelegatedUsers as u}
              <option value={u.id}>{u.username}</option>
            {/each}
          </select>
          <button class="btn-primary" onclick={saveDelegates} disabled={savingDelegates}>
            {savingDelegates ? "Saving…" : "Save delegated access"}
          </button>
        </div>
        {#if allUsers.length === 0}
          <p class="text-muted text-xs mt-2">
            Create non-admin users on the <a href="#/users" class="underline">Users</a> page to delegate access.
          </p>
        {/if}
      </div>
    {/if}
  {:else if tab === "anticheat"}
    {#if skill?.anticheat}
      <div class="space-y-3">
        {#if skill.anticheat.antixray}
          <div class="card p-4">
            <div class="font-medium flex items-center gap-2">
              🛡️ Anti-xray
              <span class="badge {skill.anticheat.antixray.supported ? 'bg-accent2/20 text-accent' : 'bg-border text-muted'}">
                {skill.anticheat.antixray.supported ? "supported" : "n/a"}
              </span>
            </div>
            <p class="text-sm text-muted mt-1">{skill.anticheat.antixray.config_hint}</p>
            <p class="text-xs text-muted mt-2">
              Server-side anti-xray hides ore data, so xray clients see nothing — no detection or
              bans needed. Configure it in the file editor.
            </p>
            <button class="btn-ghost mt-2" onclick={() => (tab = "files")}>Open file editor →</button>
          </div>
        {/if}
        {#if skill.anticheat.battleye}
          <div class="card p-4">
            <div class="font-medium flex items-center gap-2">
              🛡️ BattlEye
              <span class="badge bg-accent2/20 text-accent">supported</span>
            </div>
            <p class="text-sm text-muted mt-1">{skill.anticheat.battleye.config_hint}</p>
          </div>
        {/if}
        {#if skill.anticheat.plugins_recommended?.length}
          <div class="card p-4">
            <div class="font-medium">Recommended anti-cheat</div>
            <div class="flex flex-wrap gap-2 mt-2">
              {#each skill.anticheat.plugins_recommended as p}
                <span class="badge bg-panel2 text-text border border-border">{p}</span>
              {/each}
            </div>
          </div>
        {/if}
        <div class="card p-4 text-sm text-muted">
          Caught a cheater? Use centralized <a href="#/bans" class="text-accent hover:underline">Bans</a>
          to ban them here or across every server at once.
        </div>
      </div>
    {:else}
      <div class="card p-6 text-center text-muted">
        This game defines no server-side anti-cheat hooks. Client-side anti-cheat (EAC/VAC/BattlEye)
        is shipped by the game itself.
      </div>
    {/if}
  {:else if tab === "backups"}
    <div class="flex flex-wrap items-end gap-2 mb-4">
      <div>
        <label class="label" for="bt">Target</label>
        <select id="bt" class="input" bind:value={selectedTarget}>
          {#if backupTargets.length === 0}
            <option value="">No targets — add one in Settings</option>
          {/if}
          {#each backupTargets as t}
            <option value={t.id}>{t.name} ({t.type})</option>
          {/each}
        </select>
      </div>
      <button class="btn-primary" onclick={runBackup} disabled={backupBusy || !selectedTarget}>
        {backupBusy ? "Starting…" : "Back up now"}
      </button>
      <button class="btn-ghost" onclick={loadBackups}>Refresh</button>
    </div>

    <div class="card divide-y divide-border">
      {#if backups.length === 0}
        <div class="p-4 text-muted text-sm">No backups yet.</div>
      {/if}
      {#each backups as b}
        <div class="flex items-center gap-3 px-4 py-3">
          <div class="flex-1 min-w-0">
            <div class="text-sm truncate">{fmtDate(b.created_at)}</div>
            <div class="text-xs text-muted truncate">
              <span class="font-mono">{backupName(b)}</span> ·
              {fmtSize(b.size_bytes)} ·
              <span
                class={b.status === "done"
                  ? "text-accent"
                  : b.status === "error"
                    ? "text-danger"
                    : "text-warn"}>{b.status}</span
              >
              {#if b.error}— {b.error}{/if}
            </div>
          </div>
          {#if b.status === "done"}
            <button class="btn-ghost" onclick={() => restoreBackup(b)}>Restore</button>
          {/if}
          <button class="btn-danger" onclick={() => deleteBackup(b)}>Delete</button>
        </div>
      {/each}
    </div>
  {:else if tab === "mods"}
    <div class="flex items-center justify-between gap-2 mb-1">
      <h3 class="text-lg font-semibold">🧩 Workshop mods</h3>
      <button class="btn-ghost text-xs" onclick={loadMods} disabled={modsBusy}>Refresh</button>
    </div>
    <p class="text-muted text-sm mb-4">
      The mods this server loads, in order. Yggdrasil checks each one against the Steam Workshop and
      against what actually downloaded to disk — so a mod that was removed upstream (or failed to
      download) shows up here instead of silently dropping out and blocking players from joining.
      <b>Editing the list takes effect on the next Update/Reinstall.</b>
    </p>

    {#if !modStatus}
      <div class="text-muted text-sm">Loading…</div>
    {:else if !modStatus.mods.length && !modStatus.orphans.length}
      <div class="card text-sm p-3 text-muted">
        No Workshop mods configured. Add IDs (semicolon-separated, in load order) under
        <button class="text-accent hover:underline" onclick={() => { tab = "settings"; openEdit(); loadDelegation(); }}>Settings → MODS</button>,
        then Update/Reinstall.
      </div>
    {:else}
      {#if modStatus.issues > 0}
        <div class="card border-warn/40 bg-warn/10 p-3 mb-4 flex items-start justify-between gap-3">
          <div class="text-sm text-warn">
            <b>{modStatus.issues} of {modStatus.mods.length} mod(s) are missing or were removed from the Workshop.</b>
            DayZ starts without them, which often stops players from joining (a missing dependency
            breaks the mission). Remove the dead ones, then press <b>Update/Reinstall</b>.
          </div>
          <button class="btn-ghost text-xs shrink-0 text-warn" onclick={pruneBroken} disabled={modsBusy}>
            Remove broken
          </button>
        </div>
      {:else if modStatus.mods.length}
        <div class="card border-accent2/40 bg-accent2/5 p-3 mb-4 text-sm text-accent">
          ✓ All {modStatus.mods.length} mod(s) are installed and still present on the Workshop.
        </div>
      {/if}

      {#if modStatus.mods.length}
        <div class="card divide-y divide-border mb-5">
          {#each modStatus.mods as m, i}
            <div class="flex items-center gap-3 p-2.5">
              <span class="text-muted text-xs w-5 text-right shrink-0">{i + 1}</span>
              <div class="min-w-0 flex-1">
                <a class="text-accent hover:underline font-medium truncate block" href={m.url} target="_blank" rel="noopener">{m.name}</a>
                <span class="text-muted text-xs font-mono">{m.id}</span>
              </div>
              {#if m.installed}
                <span class="text-xs px-2 py-0.5 rounded bg-accent2/15 text-accent shrink-0">on disk</span>
              {:else}
                <span class="text-xs px-2 py-0.5 rounded bg-warn/15 text-warn shrink-0">not downloaded</span>
              {/if}
              {#if m.workshop === "removed"}
                <span class="text-xs px-2 py-0.5 rounded bg-danger/15 text-danger shrink-0">removed from Workshop</span>
              {:else if m.workshop === "ok"}
                <span class="text-xs px-2 py-0.5 rounded bg-panel2 text-muted shrink-0">Workshop ✓</span>
              {:else}
                <span class="text-xs px-2 py-0.5 rounded bg-panel2 text-muted shrink-0">Workshop ?</span>
              {/if}
              <button class="btn-ghost text-xs shrink-0" onclick={() => removeMod(m.id)} disabled={modsBusy}>Remove</button>
            </div>
          {/each}
        </div>
      {/if}

      {#if modStatus.orphans.length}
        <h4 class="text-sm font-semibold mb-1">Downloaded but not in the load order</h4>
        <p class="text-muted text-xs mb-2">
          These <span class="font-mono">@mod</span> folders are on disk but not in MODS, so the server
          doesn't load them. Add one to the list (and Update/Reinstall) or ignore it.
        </p>
        <div class="card divide-y divide-border">
          {#each modStatus.orphans as m}
            <div class="flex items-center gap-3 p-2.5">
              <div class="min-w-0 flex-1">
                <a class="text-accent hover:underline font-medium truncate block" href={m.url} target="_blank" rel="noopener">{m.name}</a>
                <span class="text-muted text-xs font-mono">{m.id}</span>
              </div>
              {#if m.workshop === "removed"}
                <span class="text-xs px-2 py-0.5 rounded bg-danger/15 text-danger shrink-0">removed from Workshop</span>
              {/if}
              <button class="btn-ghost text-xs shrink-0" onclick={() => addOrphan(m.id)} disabled={modsBusy}>Add to list</button>
            </div>
          {/each}
        </div>
      {/if}
    {/if}
  {:else if tab === "norn"}
    <div class="flex items-center gap-2 mb-1">
      <h3 class="text-lg font-semibold">🧵 Norn — loot economy</h3>
    </div>
    <p class="text-muted text-sm mb-4">
      Controls how long dropped items stay in the world before they despawn. Modded loot often
      vanishes too fast — set a floor below and nothing will despawn quicker than that.
      <b>Changes apply on the next restart.</b>
    </p>

    {#if !economy}
      <div class="text-muted text-sm">Loading…</div>
    {:else if !economy.found}
      <div class="card border-warn/40 bg-warn/10 text-warn text-sm p-3">
        No mission economy files found for <span class="font-mono">{economy.mission}</span>. Install
        and start the server once so DayZ writes its <span class="font-mono">mpmissions</span> files.
      </div>
    {:else}
      <!-- Saved / persistence -->
      {#if economy.saved && (economy.saved.min_lifetime_hours || (economy.saved.registered && economy.saved.registered.length) || (economy.saved.globals && Object.keys(economy.saved.globals).length))}
        <div class="card border-accent2/40 bg-accent2/5 p-3 mb-5 flex items-center justify-between gap-3">
          <div class="text-sm">
            <span class="text-accent font-medium">✓ Saved & auto-re-applied after updates.</span>
            <span class="text-muted">
              {#if economy.saved.min_lifetime_hours}floor {economy.saved.min_lifetime_hours}h{/if}{#if economy.saved.registered && economy.saved.registered.length} · {economy.saved.registered.length} registration(s){/if}{#if economy.saved.globals && Object.keys(economy.saved.globals).length} · {Object.keys(economy.saved.globals).length} globals{/if}.
              A restart keeps your settings; Update/Reinstall regenerates vanilla files and Norn re-applies these automatically.
            </span>
          </div>
          <button class="btn-ghost text-xs shrink-0" onclick={resetNorn} disabled={nornBusy}>Reset</button>
        </div>
      {/if}

      <!-- Overview -->
      <div class="grid grid-cols-2 sm:grid-cols-3 gap-3 mb-5">
        <div class="card p-3">
          <div class="text-muted text-xs uppercase tracking-wide">Mission</div>
          <div class="text-sm font-mono mt-1 truncate">{economy.mission}</div>
        </div>
        <div class="card p-3">
          <div class="text-muted text-xs uppercase tracking-wide">Item types</div>
          <div class="text-2xl font-semibold mt-1">{economy.total_items}</div>
        </div>
        <div class="card p-3 {economy.min_lifetime >= 0 && economy.min_lifetime < 3600 ? 'border-warn/50' : ''}">
          <div class="text-muted text-xs uppercase tracking-wide">Shortest lifetime</div>
          <div class="text-2xl font-semibold mt-1">{fmtDur(economy.min_lifetime)}</div>
          {#if economy.min_lifetime >= 0 && economy.min_lifetime < 3600}
            <div class="text-warn text-xs mt-0.5">some items despawn fast</div>
          {/if}
        </div>
      </div>

      <!-- Unregistered modded types -->
      {#if economy.unregistered && economy.unregistered.length}
        <div class="card border-warn/40 bg-warn/10 p-4 mb-5">
          <h4 class="font-semibold mb-1 text-warn">⚠ {economy.unregistered.length} modded loot file(s) not in the economy</h4>
          <p class="text-muted text-xs mb-2">
            These <span class="font-mono">types.xml</span> files exist in the mission but aren't registered in
            <span class="font-mono">cfgeconomycore.xml</span>, so their items don't spawn or get managed. Register
            them so the loot works (and the lifetime floor applies to them too).
          </p>
          <ul class="text-xs font-mono text-muted mb-3 space-y-0.5">
            {#each economy.unregistered as u}<li class="truncate">• {u}</li>{/each}
          </ul>
          <button class="btn-primary" onclick={registerTypes} disabled={nornBusy}>
            {nornBusy ? "Working…" : "Register all in economy"}
          </button>
        </div>
      {/if}

      <!-- Loot from installed mods -->
      {#if modLoot && modLoot.mods && modLoot.mods.length}
        <div class="card p-4 mb-5">
          <h4 class="font-semibold mb-1">Loot from installed mods</h4>
          <p class="text-muted text-xs mb-3">
            <span class="font-mono">types.xml</span> files shipped inside your mods. Import one to copy it
            into the mission and register it, so its loot spawns and is covered by the lifetime floor.
          </p>
          <div class="space-y-3">
            {#each modLoot.mods as m}
              <div class="border border-border rounded-md p-3">
                <div class="flex items-center gap-2 mb-1">
                  <span class="font-medium">{m.name}</span>
                  {#if m.expansion}<span class="badge bg-warn/20 text-warn">manages own economy</span>{/if}
                </div>
                {#if m.expansion}
                  <p class="text-warn text-xs mb-2">DayZ Expansion injects its own loot — you usually don't need to import these.</p>
                {/if}
                {#each m.files as f}
                  <div class="flex items-center justify-between gap-3 py-1">
                    <span class="text-xs font-mono truncate">{f.path} <span class="text-muted">· {f.items} items</span></span>
                    {#if f.imported}
                      <span class="badge bg-accent2/20 text-accent shrink-0">imported</span>
                    {:else}
                      <button class="btn-ghost px-2 py-1 text-xs shrink-0" onclick={() => importModTypes(f.path)} disabled={nornBusy}>Import + register</button>
                    {/if}
                  </div>
                {/each}
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Minimum lifetime floor -->
      <div class="card p-4 mb-5">
        <h4 class="font-semibold mb-1">Minimum lifetime floor</h4>
        <p class="text-muted text-xs mb-3">
          Raise every item whose lifetime is below this up to it — across vanilla <span class="font-mono">types.xml</span>
          and any modded types files registered in the economy. The fastest, most reliable fix for
          "modded items despawn too quickly".
        </p>
        <div class="flex flex-wrap items-end gap-2">
          <div>
            <label class="label" for="norn-h">No item despawns faster than (hours)</label>
            <input id="norn-h" class="input w-40" type="number" min="0.1" step="0.5" bind:value={minHours} />
          </div>
          <button class="btn-primary" onclick={applyMinLifetime} disabled={nornBusy}>
            {nornBusy ? "Working…" : "Apply floor"}
          </button>
        </div>
      </div>

      <!-- Globals cleanup timers -->
      <div class="card p-4 mb-5">
        <h4 class="font-semibold mb-1">Cleanup timers (globals.xml)</h4>
        <p class="text-muted text-xs mb-3">
          Fallback despawn timers in seconds. <span class="font-mono">CleanupLifetimeDefault</span> is
          used for items with no explicit lifetime — bump it if loot still disappears.
        </p>
        <div class="grid grid-cols-2 sm:grid-cols-3 gap-3">
          {#each Object.keys(globalsEdit) as k}
            <div>
              <label class="label text-xs" for={`g-${k}`}>{k.replace("CleanupLifetime", "")}</label>
              <input id={`g-${k}`} class="input" type="number" min="0" bind:value={globalsEdit[k]} />
            </div>
          {/each}
        </div>
        <button class="btn-ghost mt-3" onclick={saveGlobals} disabled={nornBusy}>Save timers</button>
      </div>

      <!-- Per-file breakdown -->
      <div class="card divide-y divide-border">
        {#each economy.files as f}
          <div class="flex items-center justify-between px-4 py-2 text-sm">
            <span class="font-mono truncate">{f.path}{#if f.modded}<span class="badge bg-accent2/20 text-accent ml-2">modded</span>{/if}</span>
            <span class="text-muted shrink-0">{f.items} items · min {fmtDur(f.min_lifetime)}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
{/if}
