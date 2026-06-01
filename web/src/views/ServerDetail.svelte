<script>
  import { onMount, onDestroy } from "svelte";
  import { api, wsURL } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { navigate } from "../lib/router.js";
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
      });
      toast("Saved — restart to apply (reinstall for file-baked values)", "success");
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingEdit = false;
    }
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

  async function runInstall() {
    try {
      await api.post(`/servers/${id}/install`);
      tab = "install";
      connectInstallLog();
      toast("Install started", "info");
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
    await loadServer();
    if (server && !server.installed) {
      tab = "install";
      connectInstallLog();
    } else if (server?.status === "running") {
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
    <span class="badge {server.status === 'running' ? 'bg-accent2/20 text-accent' : 'bg-border text-muted'}"
      >{server.status}</span
    >
  </div>
  <div class="text-muted text-sm mb-4">{server.gameskill_id}</div>

  <!-- Controls + live stats -->
  <div class="flex flex-wrap items-center gap-2 mb-4">
    {#if !server.installed}
      <button class="btn-primary" onclick={runInstall} disabled={server.install_status === "installing"}>
        {server.install_status === "installing" ? "Installing…" : server.install_status === "error" ? "Retry install" : "Install"}
      </button>
    {:else if server.status === "running"}
      <button class="btn-ghost" onclick={() => action("restart")}>Restart</button>
      <button class="btn-ghost" onclick={() => action("stop")}>Stop</button>
    {:else}
      <button class="btn-primary" onclick={() => action("start")}>Start</button>
      <button class="btn-ghost" onclick={runInstall}>Reinstall</button>
    {/if}
    <button class="btn-danger ml-auto" onclick={del}>Delete</button>
  </div>

  {#if !server.installed}
    <div class="card border-warn/40 bg-warn/10 text-warn text-sm px-4 py-2 mb-4">
      This server isn't installed yet. Click <b>Install</b> to download/build it; progress shows below.
    </div>
  {/if}

  {#if stats && server.status === "running"}
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
    {#each [["console", "Console"], ["files", "Files"], ["backups", "Backups"], ["settings", "Settings"], ["anticheat", "Anti-cheat"], ["install", "Install log"]] as [key, label]}
      <button
        class="px-4 py-2 text-sm border-b-2 -mb-px {tab === key
          ? 'border-accent text-text'
          : 'border-transparent text-muted hover:text-text'}"
        onclick={() => {
          tab = key;
          if (key === "install" && !installWs) connectInstallLog();
          if (key === "backups") loadBackups();
          if (key === "settings") openEdit();
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
        placeholder={server.status === "running" ? "Type a console command…" : "Server is stopped"}
        disabled={server.status !== "running"}
      />
      <button class="btn-primary" disabled={server.status !== "running"}>Send</button>
    </form>
    {#if server.status === "running" && (!ws || ws.readyState !== 1)}
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
        <div class="card bg-warn/10 border-warn/40 text-warn text-xs px-3 py-2">
          Changes apply on the next <b>restart</b>. Values written into config files at install time
          (e.g. RCON password, world seed) need a <b>Reinstall</b> to fully apply — back up your
          world first, as reinstall can regenerate config.
        </div>
        <div class="flex gap-2">
          <button class="btn-primary" onclick={saveEdit} disabled={savingEdit}>
            {savingEdit ? "Saving…" : "Save changes"}
          </button>
          <button class="btn-ghost" onclick={runInstall}>Reinstall / update game</button>
        </div>
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
            <div class="text-sm truncate">{b.created_at}</div>
            <div class="text-xs text-muted">
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
  {/if}
{/if}
