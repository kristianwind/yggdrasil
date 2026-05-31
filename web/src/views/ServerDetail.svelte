<script>
  import { onMount, onDestroy } from "svelte";
  import { api, wsURL } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { navigate } from "../lib/router.js";
  import FileManager from "../components/FileManager.svelte";

  let { id } = $props();

  let server = $state(null);
  let tab = $state("console");
  let stats = $state(null);
  let statsTimer;

  // Console
  let lines = $state([]);
  let cmd = $state("");
  let ws = $state(null);
  let termEl;

  async function loadServer() {
    try {
      server = await api.get(`/servers/${id}`);
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
    if (server?.status === "running") connectConsole();
    pollStats();
    statsTimer = setInterval(pollStats, 4000);
  });
  onDestroy(() => {
    closeWS();
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
    {#if server.status === "running"}
      <button class="btn-ghost" onclick={() => action("restart")}>Restart</button>
      <button class="btn-ghost" onclick={() => action("stop")}>Stop</button>
    {:else}
      <button class="btn-primary" onclick={() => action("start")}>Start</button>
    {/if}
    <button class="btn-danger ml-auto" onclick={del}>Delete</button>
  </div>

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
        <div class="text-xs text-muted">Mem limit</div>
        <div class="text-lg font-semibold">
          {stats.mem_limit_mb ? stats.mem_limit_mb.toFixed(0) + " MB" : "—"}
        </div>
      </div>
    </div>
  {/if}

  <!-- Tabs -->
  <div class="flex gap-1 border-b border-border mb-4">
    {#each [["console", "Console"], ["files", "Files"]] as [key, label]}
      <button
        class="px-4 py-2 text-sm border-b-2 -mb-px {tab === key
          ? 'border-accent text-text'
          : 'border-transparent text-muted hover:text-text'}"
        onclick={() => (tab = key)}>{label}</button
      >
    {/each}
  </div>

  {#if tab === "console"}
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
  {/if}
{/if}
