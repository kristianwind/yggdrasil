<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { user } from "../lib/auth.js";
  import { navigate } from "../lib/router.js";
  import { toast } from "../lib/toast.js";

  let info = $state(null);
  let servers = $state([]);
  let gameskills = $state([]);
  let error = $state("");

  // Map a server's gameskill_id to a friendly rune name (e.g. "Minecraft (Java)").
  let skillName = $derived.by(() => {
    const m = Object.fromEntries(gameskills.map((g) => [g.id, g.name]));
    return (id) => m[id] || id;
  });

  // The most connect-relevant published port for a server (game > query > web > any).
  function primaryPort(s) {
    const p = s.ports || {};
    return p.game || p.query || p.web || Object.values(p)[0] || 0;
  }
  // Compact "added X ago" from an SQLite UTC timestamp ("YYYY-MM-DD HH:MM:SS").
  function relTime(iso) {
    if (!iso) return "";
    const t = new Date(iso.replace(" ", "T") + (iso.endsWith("Z") ? "" : "Z")).getTime();
    if (isNaN(t)) return "";
    const s = (Date.now() - t) / 1000;
    if (s < 60) return "just now";
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    if (s < 2592000) return `${Math.floor(s / 86400)}d ago`;
    return `${Math.floor(s / 2592000)}mo ago`;
  }

  onMount(async () => {
    try {
      [servers, gameskills] = await Promise.all([
        api.get("/servers"),
        api.get("/gameskills").catch(() => []),
      ]);
      if ($user.role === "admin") {
        info = await api.get("/system/info");
        beacon = await api.get("/settings/beacon").catch(() => null);
      }
    } catch (e) {
      error = e.message;
    }
  });

  // Beacon teaser: a gentle, dismissible nudge to opt into the anonymous install
  // ping. Shows only to admins, only while the beacon is off and not dismissed —
  // so it invites once and then gets out of the way (enabling or dismissing hides it).
  let beacon = $state(null);
  let beaconDismissed = $state(localStorage.getItem("ygg_beacon_teaser_dismissed") === "1");
  let enablingBeacon = $state(false);
  let showBeaconTeaser = $derived(
    $user.role === "admin" && beacon && !beacon.enabled && !beaconDismissed,
  );
  async function enableBeacon() {
    enablingBeacon = true;
    try {
      beacon = await api.put("/settings/beacon", { enabled: true });
      toast("Beacon on — thanks for counting yourself in 🌳", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      enablingBeacon = false;
    }
  }
  function dismissBeacon() {
    beaconDismissed = true;
    try {
      localStorage.setItem("ygg_beacon_teaser_dismissed", "1");
    } catch {
      /* ignore */
    }
  }

  // AI ops digest (advisory, admin — cross-server health briefing)
  let opsDigest = $state("");
  let opsBusy = $state(false);
  async function loadOpsDigest() {
    opsBusy = true;
    try {
      const r = await api.post("/ai/health-digest");
      opsDigest = r.summary || "(no summary returned)";
    } catch (e) {
      opsDigest = "";
      error = e.message;
    } finally {
      opsBusy = false;
    }
  }

  // AI actions (Phase 4 — natural language → propose → confirm → run)
  let opsRequest = $state("");
  let planBusy = $state(false);
  let plan = $state(null); // { actions:[{action,server,server_id,ok,problem,reason}], note }
  let execBusy = $state(false);
  let execResults = $state(null); // [{server,action,status,detail}]
  async function proposePlan() {
    if (!opsRequest.trim()) return;
    planBusy = true;
    execResults = null;
    try {
      plan = await api.post("/ai/plan", { request: opsRequest });
    } catch (e) {
      error = e.message;
      plan = null;
    } finally {
      planBusy = false;
    }
  }
  let planOkActions = $derived((plan?.actions || []).filter((a) => a.ok));
  async function runPlan() {
    const actions = planOkActions.map((a) => ({ action: a.action, server_id: a.server_id }));
    if (!actions.length) return;
    execBusy = true;
    try {
      const r = await api.post("/ai/plan/execute", { actions });
      execResults = r.results || [];
      plan = null;
      opsRequest = "";
    } catch (e) {
      error = e.message;
    } finally {
      execBusy = false;
    }
  }

  const stat = (label, value, sub, link) => ({ label, value, sub, link });
  function fmtBytes(n) {
    if (!n) return "—";
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n.toFixed(1)} ${u[i]}`;
  }
  let cards = $derived.by(() => {
    if (!info) {
      return [stat("Servers", servers.length, `${servers.filter((s) => s.status === "running").length} running`)];
    }
    const c = [
      stat("Servers", info.servers, `${info.servers_running} running`, "#/servers"),
      stat("Runes", info.gameskills, "game definitions", "#/runes"),
      stat("Docker", info.docker_ok ? "OK" : "Down", info.arch),
    ];
    // Host CPU/RAM are Linux-only; the API sends cpu_percent = -1 and
    // mem_total_bytes = 0 where unavailable (e.g. the dev Mac).
    if (typeof info.cpu_percent === "number" && info.cpu_percent >= 0) {
      c.push(stat("CPU usage", `${info.cpu_percent.toFixed(0)}%`, `${info.cpu_count} cores`));
    }
    if (info.mem_total_bytes) {
      const pct = Math.round((info.mem_used_bytes / info.mem_total_bytes) * 100);
      c.push(stat("RAM usage", `${pct}%`, `${fmtBytes(info.mem_used_bytes)} of ${fmtBytes(info.mem_total_bytes)}`));
    }
    c.push(
      stat(
        "Disk free",
        fmtBytes(info.disk_free_bytes),
        info.disk_total_bytes ? `of ${fmtBytes(info.disk_total_bytes)}` : "",
      ),
    );
    return c;
  });
</script>

<div class="flex items-start justify-between gap-3 mb-6">
  <div>
    <h1 class="text-2xl font-semibold mb-1">Dashboard</h1>
    <p class="text-muted">Welcome back, {$user.username}.</p>
  </div>
  {#if $user.can_create}
    <button class="btn-primary shrink-0" onclick={() => navigate("/servers?new=1")}>+ New Server</button>
  {/if}
</div>

{#if error}
  <div class="card border-danger p-3 text-danger text-sm mb-4">{error}</div>
{/if}

<div class="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-8">
  {#each cards as c}
    <svelte:element
      this={c.link ? "a" : "div"}
      href={c.link}
      class="card p-4 block {c.link ? 'hover:bg-panel2/50 transition-colors' : ''}"
    >
      <div class="text-muted text-xs uppercase tracking-wide">{c.label}</div>
      <div class="text-2xl font-semibold mt-1">{c.value}</div>
      {#if c.sub}<div class="text-muted text-xs mt-0.5">{c.sub}</div>{/if}
    </svelte:element>
  {/each}
</div>

{#if showBeaconTeaser}
  <div class="card p-4 mb-8 relative border-l-4 border-accent">
    <button
      class="absolute top-2.5 right-3 text-muted hover:text-text text-sm leading-none"
      title="Dismiss"
      aria-label="Dismiss"
      onclick={dismissBeacon}>✕</button>
    <div class="flex items-start gap-4">
      <div class="text-3xl shrink-0 leading-none" aria-hidden="true">📡</div>
      <div class="min-w-0 pr-4">
        <h2 class="text-base font-semibold">Count yourself in</h2>
        <p class="text-sm text-muted mt-1 max-w-2xl">
          Curious how many Yggdrasil panels are out there? So are we. Turn on the beacon to be counted —
          it's off by default and fully anonymous: one daily ping with
          <span class="text-text">only a random ID and the version</span>, nothing else. No IP, no server
          names, no usage data.
        </p>
        <div class="flex items-center gap-4 mt-3">
          <button class="btn-primary text-sm" disabled={enablingBeacon} onclick={enableBeacon}>
            {enablingBeacon ? "Enabling…" : "Enable beacon"}
          </button>
          <a class="text-sm text-accent hover:underline" href="#/settings">See exactly what's sent →</a>
        </div>
      </div>
    </div>
  </div>
{/if}

{#if info?.ai_enabled}
  <div class="card p-4 mb-8">
    <div class="flex items-center gap-2">
      <h2 class="text-lg font-semibold">🤖 Kvasir · Ops digest</h2>
      <button class="btn-primary text-xs ml-auto" disabled={opsBusy} onclick={loadOpsDigest}
        title="Ask Kvasir (your configured AI) for a plain-language health briefing across all servers (advisory).">
        {opsBusy ? "Summarizing…" : "Summarize"}</button>
    </div>
    {#if opsDigest}
      <div class="whitespace-pre-wrap break-words text-sm mt-3">{opsDigest}</div>
      <div class="text-[10px] text-muted mt-2">Advisory only — generated by your configured LLM from current panel data; may contain mistakes.</div>
    {:else}
      <p class="text-muted text-sm mt-1">A quick "anything need attention?" read across your servers — stopped servers, failed backups/tasks, disk & resource pressure.</p>
    {/if}
  </div>
{/if}

{#if info?.ai_actions_enabled}
  <div class="card p-4 mb-8 space-y-3">
    <div class="flex items-center gap-2">
      <h2 class="text-lg font-semibold">🤖 Ask Kvasir</h2>
      <span class="text-xs text-muted">propose → you confirm → run</span>
    </div>
    <form onsubmit={(e) => { e.preventDefault(); proposePlan(); }} class="flex gap-2">
      <input class="input" bind:value={opsRequest} disabled={planBusy}
        placeholder="e.g. safely restart all Minecraft servers; players may be online" />
      <button class="btn-primary shrink-0" disabled={planBusy || !opsRequest.trim()}>{planBusy ? "Thinking…" : "Propose"}</button>
    </form>

    {#if plan}
      {#if plan.note}<div class="text-sm text-muted">{plan.note}</div>{/if}
      {#if plan.actions?.length}
        <div class="card divide-y divide-border">
          {#each plan.actions as a}
            <div class="flex items-center gap-3 px-3 py-2 text-sm {a.ok ? '' : 'opacity-60'}">
              <span class="shrink-0">{a.ok ? "✅" : "🚫"}</span>
              <span class="font-medium">{a.action}</span>
              <span class="text-muted">→ {a.server}</span>
              <span class="text-xs text-muted ml-auto">{a.ok ? a.reason : a.problem}</span>
            </div>
          {/each}
        </div>
      {/if}
      <div class="flex gap-2">
        {#if planOkActions.length}
          <button class="btn-primary" disabled={execBusy} onclick={runPlan}>
            {execBusy ? "Running…" : `Confirm & run ${planOkActions.length} action${planOkActions.length > 1 ? "s" : ""}`}
          </button>
        {/if}
        <button class="btn-ghost" disabled={execBusy} onclick={() => (plan = null)}>Cancel</button>
      </div>
      <div class="text-[10px] text-muted">The AI can only propose restart / safe-restart / stop / start, on servers you control — never wipe, delete or reconfigure. Nothing runs until you confirm.</div>
    {/if}

    {#if execResults}
      <div class="card divide-y divide-border">
        {#each execResults as r}
          <div class="flex items-center gap-3 px-3 py-2 text-sm">
            <span class="shrink-0">{r.status === "ok" ? "✅" : r.status === "skipped" ? "⏭️" : "❌"}</span>
            <span class="font-medium">{r.action}</span>
            <span class="text-muted">→ {r.server}</span>
            <span class="text-xs text-muted ml-auto">{r.detail}</span>
          </div>
        {/each}
      </div>
    {/if}
  </div>
{/if}

<h2 class="text-lg font-semibold mb-3">Recent servers</h2>
<div class="card divide-y divide-border">
  {#if servers.length === 0}
    <div class="p-4 text-muted text-sm">No servers yet. Create one from the Servers page.</div>
  {/if}
  {#each servers.slice(0, 8) as s}
    <a href={`#/servers/${s.id}`} class="flex items-center justify-between gap-3 px-4 py-3 hover:bg-panel2/50">
      <div class="flex items-center gap-3 min-w-0">
        <span
          class="w-2 h-2 rounded-full shrink-0 {s.status === 'running'
            ? 'bg-accent'
            : s.status === 'starting'
              ? 'bg-warn animate-pulse'
              : 'bg-muted/40'}"
          aria-hidden="true"
        ></span>
        <div class="min-w-0">
          <div class="font-medium truncate">{s.name}</div>
          <div class="text-xs text-muted truncate">
            {skillName(s.gameskill_id)}{#if primaryPort(s)} ·
              <span class="font-mono">:{primaryPort(s)}</span>{/if}{#if s.subdomain} ·
              {s.subdomain}{/if} · added {relTime(s.created_at)}
          </div>
        </div>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        {#if !s.installed}
          <span class="badge bg-warn/20 text-warn">needs install</span>
        {/if}
        <span
          class="badge {s.status === 'running'
            ? 'bg-accent2/20 text-accent'
            : s.status === 'starting'
              ? 'bg-warn/20 text-warn'
              : 'bg-border text-muted'}">{s.status}</span
        >
      </div>
    </a>
  {/each}
</div>
