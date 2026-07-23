<script>
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api.js";
  import { user } from "../lib/auth.js";
  import { navigate } from "../lib/router.js";
  import { toast } from "../lib/toast.js";
  import Sparkline from "../components/Sparkline.svelte";

  let info = $state(null);
  let fleet = $state(null); // aggregate across game servers: running, players, container CPU/RAM
  // Who's online right now — live per-server query (names where the protocol exposes
  // them, e.g. DayZ; a count otherwise). Refreshed on an interval.
  let whosOnline = $state([]);
  let whoTimer = null;
  async function loadWhosOnline() {
    try {
      whosOnline = await api.get("/fleet/players");
    } catch {
      whosOnline = [];
    }
  }
  onMount(() => {
    loadWhosOnline();
    whoTimer = setInterval(loadWhosOnline, 20000);
  });
  onDestroy(() => clearInterval(whoTimer));
  let backupCoverage = $state(null);
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
  // The address players actually connect to: the configured public hostname (or
  // detected IP) + the primary port. Falls back to a subdomain or bare :port.
  let network = $state(null);
  function connectAddr(s) {
    const port = primaryPort(s);
    const host = network?.effective;
    if (host && port) return `${host}:${port}`;
    if (s.subdomain) return s.subdomain;
    return port ? `:${port}` : "";
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

  // The "no recent backup" nudge is dismissable, but honestly: dismissing
  // remembers exactly which servers you acknowledged, so it returns if a *new*
  // server goes unprotected, or after a week. One click can't silence a real gap
  // forever.
  const BACKUP_DISMISS_KEY = "ygg.backupWarnDismissed";
  let backupDismiss = $state(loadBackupDismiss());
  function loadBackupDismiss() {
    try {
      return JSON.parse(localStorage.getItem(BACKUP_DISMISS_KEY)) || { ids: [], at: 0 };
    } catch {
      return { ids: [], at: 0 };
    }
  }
  let showBackupWarning = $derived.by(() => {
    const stale = backupCoverage?.stale ?? [];
    if (!stale.length) return false;
    const acked = new Set(backupDismiss.ids);
    const allAcked = stale.every((s) => acked.has(s.id));
    const recent = Date.now() - backupDismiss.at < 7 * 864e5; // within 7 days
    return !(allAcked && recent);
  });
  function dismissBackupWarning() {
    const stale = backupCoverage?.stale ?? [];
    const names = stale.map((s) => s.name).join(", ");
    backupDismiss = { ids: stale.map((s) => s.id), at: Date.now() };
    localStorage.setItem(BACKUP_DISMISS_KEY, JSON.stringify(backupDismiss));
    toast(
      `Noted — ${names} ${stale.length === 1 ? "isn't" : "aren't"} backed up, and that's your call. ` +
        `Set up a backup whenever you like; this reminder returns if another server goes unprotected.`,
      "info",
    );
  }

  onMount(async () => {
    try {
      [servers, gameskills] = await Promise.all([
        api.get("/servers"),
        api.get("/gameskills").catch(() => []),
      ]);
      network = await api.get("/settings/network").catch(() => null);
      fleet = await api.get("/fleet/summary").catch(() => null);
      if ($user.role === "admin") {
        info = await api.get("/system/info");
        beacon = await api.get("/settings/beacon").catch(() => null);
        backupCoverage = await api.get("/system/backup-coverage").catch(() => null);
      }
    } catch (e) {
      error = e.message;
    }
  });

  // Whole-host resource history for the Dashboard — the machine's CPU/RAM/disk over
  // time, the same trend charts each server page shows for itself. Admin-only, lazy
  // (loads when expanded), and reloads when the time window changes.
  let hostMetrics = $state([]);
  let hostHours = $state(24);
  // Open by default; the user's choice is remembered per browser.
  let showHostHistory = $state(localStorage.getItem("ygg_dash_hosthistory") !== "0");
  function toggleHostHistory() {
    showHostHistory = !showHostHistory;
    localStorage.setItem("ygg_dash_hosthistory", showHostHistory ? "1" : "0");
  }
  async function loadHostMetrics() {
    try {
      hostMetrics = await api.get(`/system/metrics?hours=${hostHours}`);
    } catch {
      hostMetrics = [];
    }
  }
  $effect(() => {
    if (showHostHistory && $user.role === "admin" && !modLayout.hidden.includes("hosthistory")) {
      hostHours; // track for reload on window change
      loadHostMetrics();
    }
  });
  const hostCpuSeries = $derived(hostMetrics.map((m) => m.cpu));
  const hostRamSeries = $derived(
    hostMetrics.map((m) => (m.mem_total_mb > 0 ? (m.mem_used_mb / m.mem_total_mb) * 100 : -1)),
  );
  const hostDiskSeries = $derived(
    hostMetrics.map((m) => (m.disk_total_mb > 0 ? (m.disk_used_mb / m.disk_total_mb) * 100 : -1)),
  );

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

  // Kvasir chat — a panel-grounded conversation over a WebSocket. The server
  // keeps no state (the visible history is sent each turn) and streams the reply
  // back as delta frames. Actions the AI proposes arrive validated and run only
  // through the same confirmed /ai/plan/execute path as before.
  let chatMsgs = $state([]); // {role, content, streamRaw?, actions?, results?, running?}
  let chatInput = $state("");
  let chatBusy = $state(false);
  let chatErr = $state("");
  let chatWS = null;
  let chatBox = $state(null);
  function chatScroll() {
    queueMicrotask(() => chatBox?.scrollTo(0, chatBox.scrollHeight));
  }
  function chatSocket() {
    return new Promise((resolve, reject) => {
      if (chatWS && chatWS.readyState === WebSocket.OPEN) return resolve(chatWS);
      const proto = location.protocol === "https:" ? "wss" : "ws";
      const ws = new WebSocket(`${proto}://${location.host}/api/ai/chat/ws`);
      ws.onopen = () => {
        chatWS = ws;
        resolve(ws);
      };
      ws.onerror = () => reject(new Error("could not open the chat connection"));
      ws.onmessage = (ev) => {
        try {
          chatFrame(JSON.parse(ev.data));
        } catch {
          /* keepalive/noise */
        }
      };
      ws.onclose = () => {
        if (chatWS === ws) chatWS = null;
        if (chatBusy) {
          chatBusy = false;
          chatErr = "the chat connection dropped — send again to reconnect";
        }
      };
    });
  }
  function chatFrame(f) {
    const last = chatMsgs[chatMsgs.length - 1];
    if (!last || last.role !== "assistant") return;
    if (f.type === "delta") {
      last.streamRaw = (last.streamRaw || "") + f.text;
      // Hide the trailing ```actions block while it streams — it becomes buttons.
      const cut = last.streamRaw.indexOf("```actions");
      last.content = cut >= 0 ? last.streamRaw.slice(0, cut) : last.streamRaw;
    } else if (f.type === "done") {
      last.content = f.text;
      last.actions = (f.actions || []).filter((a) => a.ok);
      chatBusy = false;
    } else if (f.type === "error") {
      chatErr = f.error;
      if (!last.content) chatMsgs.pop(); // don't leave an empty bubble behind
      chatBusy = false;
    }
    chatScroll();
  }
  async function chatSend() {
    const text = chatInput.trim();
    if (!text || chatBusy) return;
    chatErr = "";
    chatMsgs.push({ role: "user", content: text });
    chatInput = "";
    chatBusy = true;
    chatScroll();
    // History = everything up to and including the new user turn (not the
    // placeholder the reply streams into).
    const history = chatMsgs
      .map((m) => ({ role: m.role, content: m.content }))
      .slice(-16);
    chatMsgs.push({ role: "assistant", content: "" });
    try {
      const ws = await chatSocket();
      ws.send(JSON.stringify({ messages: history }));
    } catch (e) {
      chatErr = e.message;
      chatBusy = false;
      chatMsgs.pop();
    }
  }
  async function chatRun(msg) {
    const actions = (msg.actions || []).map((a) => ({ action: a.action, server_id: a.server_id }));
    if (!actions.length) return;
    msg.running = true;
    try {
      const r = await api.post("/ai/plan/execute", { actions });
      msg.results = r.results || [];
      msg.actions = [];
    } catch (e) {
      chatErr = e.message;
    } finally {
      msg.running = false;
    }
  }
  onDestroy(() => chatWS?.close());

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
  // --- Customizable module layout: sort + show/hide the dashboard sections ---
  // Per-browser preference (localStorage), like the host-history fold and the
  // sidebar collapse. Each module still applies its own visibility conditions
  // (admin-only, AI enabled, non-empty) — hiding here is on top of those.
  const MODULES = [
    { id: "fleet", label: "Fleet summary", icon: "📊" },
    { id: "cards", label: "Stat cards", icon: "🧮" },
    { id: "hosthistory", label: "Host history", icon: "📈" },
    { id: "opsdigest", label: "Kvasir · Ops digest", icon: "🤖" },
    { id: "askkvasir", label: "Ask Kvasir", icon: "💬" },
    { id: "whosonline", label: "Who's online", icon: "👥" },
    { id: "recent", label: "Recent servers", icon: "🕘" },
  ];
  const MODULE_LABEL = Object.fromEntries(MODULES.map((m) => [m.id, m]));
  const DEFAULT_ORDER = MODULES.map((m) => m.id);
  const MODULES_KEY = "ygg_dash_modules";
  function loadModLayout() {
    try {
      const s = JSON.parse(localStorage.getItem(MODULES_KEY)) || {};
      const known = new Set(DEFAULT_ORDER);
      // Keep the saved order for ids that still exist; append any module added
      // since the layout was saved, in its default position among the leftovers.
      const order = (s.order || []).filter((id) => known.has(id));
      for (const id of DEFAULT_ORDER) if (!order.includes(id)) order.push(id);
      const hidden = (s.hidden || []).filter((id) => known.has(id));
      return { order, hidden };
    } catch {
      return { order: [...DEFAULT_ORDER], hidden: [] };
    }
  }
  let modLayout = $state(loadModLayout());
  function saveModLayout() {
    try {
      localStorage.setItem(MODULES_KEY, JSON.stringify(modLayout));
    } catch {
      /* ignore */
    }
  }
  let showCustomize = $state(false);
  function toggleModule(id) {
    modLayout.hidden = modLayout.hidden.includes(id)
      ? modLayout.hidden.filter((h) => h !== id)
      : [...modLayout.hidden, id];
    saveModLayout();
  }
  function moveModule(i, dir) {
    const j = i + dir;
    if (j < 0 || j >= modLayout.order.length) return;
    const next = [...modLayout.order];
    [next[i], next[j]] = [next[j], next[i]];
    modLayout.order = next;
    saveModLayout();
  }
  // Drag-to-reorder (desktop) — same pattern as the realm manager: rows reorder
  // live as you drag over them, the result is saved on drop.
  let modDragIndex = $state(null);
  function onModDragOver(e, i) {
    e.preventDefault();
    if (modDragIndex === null || modDragIndex === i) return;
    const next = [...modLayout.order];
    const [moved] = next.splice(modDragIndex, 1);
    next.splice(i, 0, moved);
    modLayout.order = next;
    modDragIndex = i;
  }
  function onModDrop() {
    if (modDragIndex === null) return;
    modDragIndex = null;
    saveModLayout();
  }
  function resetModLayout() {
    modLayout = { order: [...DEFAULT_ORDER], hidden: [] };
    saveModLayout();
  }
  let modCustomized = $derived(
    modLayout.hidden.length > 0 || modLayout.order.join() !== DEFAULT_ORDER.join(),
  );

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
  <div class="flex items-center gap-2 shrink-0">
    <button
      class="btn-ghost text-sm {showCustomize ? 'bg-panel2' : ''}"
      title="Reorder or hide the dashboard sections. Saved in this browser."
      onclick={() => (showCustomize = !showCustomize)}>⚙ Customize</button>
    {#if $user.can_create}
      <button class="btn-primary" onclick={() => navigate("/servers?new=1")}>+ New Server</button>
    {/if}
  </div>
</div>

{#if showCustomize}
  <div class="card p-4 mb-6">
    <div class="flex items-center gap-2 mb-2">
      <h2 class="text-sm font-semibold">Dashboard layout</h2>
      <span class="text-xs text-muted">drag (or ▲▼) to reorder · untick to hide · saved in this browser</span>
      <div class="ml-auto flex items-center gap-2">
        {#if modCustomized}
          <button class="btn-ghost text-xs" onclick={resetModLayout}>Reset to default</button>
        {/if}
        <button class="btn-ghost text-xs" onclick={() => (showCustomize = false)}>Done</button>
      </div>
    </div>
    <div class="divide-y divide-border">
      {#each modLayout.order as id, i (id)}
        <div
          class="flex items-center gap-3 py-1.5 text-sm {modDragIndex === i ? 'opacity-40' : ''}"
          draggable={modLayout.order.length > 1}
          ondragstart={() => (modDragIndex = i)}
          ondragover={(e) => onModDragOver(e, i)}
          ondragend={onModDrop}
          role="listitem"
        >
          <!-- Desktop: drag handle. Touch: ▲▼ (native drag doesn't work on touch). -->
          <span class="hidden sm:inline text-muted cursor-grab select-none" title="Drag to reorder" aria-hidden="true">⠿</span>
          <button class="px-1 text-muted hover:text-text disabled:opacity-30" disabled={i === 0}
            onclick={() => moveModule(i, -1)} title="Move up" aria-label="Move {MODULE_LABEL[id].label} up">▲</button>
          <button class="px-1 text-muted hover:text-text disabled:opacity-30" disabled={i === modLayout.order.length - 1}
            onclick={() => moveModule(i, 1)} title="Move down" aria-label="Move {MODULE_LABEL[id].label} down">▼</button>
          <label class="flex items-center gap-2 cursor-pointer min-w-0 {modLayout.hidden.includes(id) ? 'text-muted' : ''}">
            <input type="checkbox" checked={!modLayout.hidden.includes(id)} onchange={() => toggleModule(id)} />
            <span aria-hidden="true">{MODULE_LABEL[id].icon}</span>
            <span class="truncate">{MODULE_LABEL[id].label}</span>
          </label>
        </div>
      {/each}
    </div>
    <p class="text-[10px] text-muted mt-2">
      Sections only appear when they apply — e.g. Host history is admin-only and the Kvasir sections need AI configured —
      so a ticked section can still be absent.
    </p>
  </div>
{/if}

<!-- The dashboard sections ("modules") are declared as snippets and rendered by
     the ordered loop at the bottom, so the user can sort and hide them. Snippet
     declarations render nothing where they stand — only the loop's order matters. -->
{#snippet modFleet()}
{#if fleet && fleet.servers > 0}
  <div class="card px-4 py-2.5 mb-6 flex flex-wrap items-center gap-x-6 gap-y-1 text-sm">
    <a href="#/servers" class="flex items-center gap-1.5 hover:text-text">
      <span
        class="w-2 h-2 rounded-full {fleet.running === fleet.servers
          ? 'bg-accent'
          : fleet.running > 0
            ? 'bg-warn'
            : 'bg-muted/40'}"
        aria-hidden="true"
      ></span>
      <span class="font-semibold">{fleet.running}/{fleet.servers}</span><span class="text-muted">running</span>
    </a>
    <span><span class="font-semibold">👥 {fleet.players}</span> <span class="text-muted">player{fleet.players === 1 ? "" : "s"} online</span></span>
    <span class="text-muted">
      Containers: <span class="text-text font-medium">{fleet.cpu_percent.toFixed(0)}% CPU</span>
      · <span class="text-text font-medium">{fmtBytes(fleet.mem_mb * 1024 * 1024)} RAM</span>
    </span>
  </div>
{/if}
{/snippet}

{#if error}
  <div class="card border-danger p-3 text-danger text-sm mb-4">{error}</div>
{/if}

{#snippet modCards()}
<div class="grid grid-cols-2 sm:grid-cols-3 xl:grid-cols-6 gap-3 mb-8">
  {#each cards as c}
    <svelte:element
      this={c.link ? "a" : "div"}
      href={c.link}
      class="card p-3 block {c.link ? 'hover:bg-panel2/50 transition-colors' : ''}"
    >
      <div class="text-muted text-[11px] uppercase tracking-wide truncate">{c.label}</div>
      <div class="text-xl font-semibold mt-0.5">{c.value}</div>
      {#if c.sub}<div class="text-muted text-xs mt-0.5 truncate">{c.sub}</div>{/if}
    </svelte:element>
  {/each}
</div>
{/snippet}

{#snippet modHostHistory()}
{#if $user.role === "admin" && info}
  <div class="mb-8">
    <div class="flex items-center gap-2">
      <button class="text-sm text-muted hover:text-text" onclick={toggleHostHistory}>
        {showHostHistory ? "▾" : "▸"} 📈 Host history
      </button>
      {#if showHostHistory}
        <div class="inline-flex rounded-md border border-border overflow-hidden text-xs ml-2">
          {#each [[24, "24h"], [72, "3d"], [168, "7d"]] as [h, lbl]}
            <button class="px-2 py-1 {hostHours === h ? 'bg-panel2 text-text' : 'text-muted hover:bg-panel2/50'}"
              onclick={() => (hostHours = h)}>{lbl}</button>
          {/each}
        </div>
      {/if}
    </div>
    {#if showHostHistory}
      {#if hostMetrics.length >= 2}
        <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mt-3">
          <Sparkline values={hostCpuSeries} label="CPU" unit="%" color="rgb(var(--c-accent2))" format={(v) => v.toFixed(0)} />
          <Sparkline values={hostRamSeries} label="RAM" unit="%" color="rgb(var(--c-accent))" format={(v) => v.toFixed(0)} />
          <Sparkline values={hostDiskSeries} label="Disk" unit="%" color="rgb(var(--c-warn))" format={(v) => v.toFixed(0)} />
        </div>
      {:else}
        <p class="text-muted text-sm mt-3">Not enough samples yet — the host is sampled every ~5 minutes. Check back shortly.</p>
      {/if}
    {/if}
  </div>
{/if}
{/snippet}

{#if showBackupWarning}
  <div class="card p-4 mb-8 border-l-4 border-warn relative">
    <button
      class="absolute top-2 right-2 text-muted hover:text-text text-lg leading-none px-1"
      title="Dismiss — you're acknowledging these servers have no backup. The reminder returns if another server goes unprotected, or after a week."
      aria-label="Dismiss backup reminder"
      onclick={dismissBackupWarning}>×</button>
    <h2 class="text-base font-semibold pr-6">
      ⚠️ {backupCoverage.stale.length}
      {backupCoverage.stale.length === 1 ? "server has" : "servers have"} no recent backup
    </h2>
    <p class="text-sm text-muted mt-0.5">
      No successful backup in the last {backupCoverage.threshold_days} days — set up a backup or a schedule so they're covered.
    </p>
    <div class="flex flex-wrap gap-2 mt-3">
      {#each backupCoverage.stale as s}
        <a href={`#/servers/${s.id}`} class="badge bg-panel2 border border-border hover:text-text">
          {s.name}
          <span class="text-muted ml-1">· {s.last_backup ? relTime(s.last_backup) : "never"}</span>
        </a>
      {/each}
    </div>
  </div>
{/if}

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

{#snippet modOpsDigest()}
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
{/snippet}

{#snippet modAskKvasir()}
{#if info?.ai_enabled}
  <div class="card p-4 mb-8 space-y-3">
    <div class="flex items-center gap-2">
      <h2 class="text-lg font-semibold">🤖 Kvasir</h2>
      <span class="text-xs text-muted">chat · knows only this panel · actions always confirmed</span>
      {#if chatMsgs.length}
        <button class="btn-ghost text-xs ml-auto" onclick={() => { chatMsgs = []; chatErr = ""; }}>Clear</button>
      {/if}
    </div>

    {#if chatMsgs.length}
      <div class="space-y-2 max-h-96 overflow-y-auto pr-1" bind:this={chatBox}>
        {#each chatMsgs as m, i}
          {#if m.role === "user"}
            <div class="flex justify-end">
              <div class="bg-accent/15 rounded-lg px-3 py-2 text-sm max-w-[85%] whitespace-pre-wrap break-words">{m.content}</div>
            </div>
          {:else}
            <div class="flex">
              <div class="bg-panel2 rounded-lg px-3 py-2 text-sm max-w-[85%] whitespace-pre-wrap break-words">
                {m.content}{#if chatBusy && i === chatMsgs.length - 1}<span class="animate-pulse">▍</span>{/if}
                {#if m.actions?.length}
                  <div class="mt-2 space-y-1 border-t border-border pt-2">
                    {#each m.actions as a}
                      <div class="text-xs">▸ <span class="font-medium">{a.action}</span> → {a.server}{#if a.reason}<span class="text-muted"> · {a.reason}</span>{/if}</div>
                    {/each}
                    <button class="btn-primary text-xs mt-1" disabled={m.running} onclick={() => chatRun(m)}>
                      {m.running ? "Running…" : `Confirm & run ${m.actions.length} action${m.actions.length > 1 ? "s" : ""}`}
                    </button>
                  </div>
                {/if}
                {#if m.results?.length}
                  <div class="mt-2 space-y-0.5 border-t border-border pt-2">
                    {#each m.results as r}
                      <div class="text-xs">{r.status === "ok" ? "✅" : r.status === "skipped" ? "⏭️" : "❌"} {r.action} → {r.server}{#if r.detail}<span class="text-muted"> · {r.detail}</span>{/if}</div>
                    {/each}
                  </div>
                {/if}
              </div>
            </div>
          {/if}
        {/each}
      </div>
    {:else}
      <p class="text-muted text-sm">
        Ask about your servers — status, trouble, history, "why did X crash?". Kvasir knows only this
        panel, and anything it proposes runs only after you confirm.
      </p>
    {/if}

    {#if chatErr}<div class="text-danger text-sm">{chatErr}</div>{/if}
    <form onsubmit={(e) => { e.preventDefault(); chatSend(); }} class="flex gap-2">
      <input class="input" bind:value={chatInput} disabled={chatBusy}
        placeholder="e.g. anything need my attention today?" />
      <button class="btn-primary shrink-0" disabled={chatBusy || !chatInput.trim()}>{chatBusy ? "…" : "Send"}</button>
    </form>
    <div class="text-[10px] text-muted">
      Kvasir can only propose restart / safe-restart / stop / start on servers you control — never wipe,
      delete or reconfigure — and nothing runs until you confirm. AI-generated; may contain mistakes.
    </div>
  </div>
{/if}
{/snippet}

{#snippet modWhosOnline()}
{#if whosOnline.length}
  <h2 class="text-lg font-semibold mb-3">Who's online</h2>
  <div class="grid grid-cols-1 gap-3 lg:grid-cols-2 mb-8">
    {#each whosOnline as sp}
      <div class="card px-4 py-3">
        <div class="flex items-center justify-between gap-2">
          <span class="font-medium truncate">{sp.name}</span>
          <span class="text-sm text-muted shrink-0">
            {sp.count < 0 ? "no query" : sp.count === 0 ? "empty" : `${sp.count} online`}
          </span>
        </div>
        {#if sp.names?.length}
          <div class="flex flex-wrap gap-1 mt-1.5">
            {#each sp.names as n}
              <span class="badge bg-panel2 border border-border text-muted">{n}</span>
            {/each}
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}
{/snippet}

{#snippet modRecent()}
<h2 class="text-lg font-semibold mb-3">Recent servers</h2>
{#if servers.length === 0}
  <div class="card p-4 mb-8 text-muted text-sm">No servers yet. Create one from the Servers page.</div>
{:else}
  <div class="grid grid-cols-1 gap-3 lg:grid-cols-2 mb-8">
    {#each servers.slice(0, 8) as s}
      <a href={`#/servers/${s.id}`} class="card flex items-center justify-between gap-3 px-4 py-3 hover:bg-panel2/50">
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
            <div class="text-xs text-muted truncate">{skillName(s.gameskill_id)} · added {relTime(s.created_at)}</div>
            {#if connectAddr(s)}
              <div class="text-xs text-muted/90 font-mono truncate">{connectAddr(s)}</div>
            {/if}
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
{/if}
{/snippet}

{#snippet modBody(id)}
  {#if id === "fleet"}{@render modFleet()}
  {:else if id === "cards"}{@render modCards()}
  {:else if id === "hosthistory"}{@render modHostHistory()}
  {:else if id === "opsdigest"}{@render modOpsDigest()}
  {:else if id === "askkvasir"}{@render modAskKvasir()}
  {:else if id === "whosonline"}{@render modWhosOnline()}
  {:else if id === "recent"}{@render modRecent()}
  {/if}
{/snippet}

{#each modLayout.order as id (id)}
  {#if !modLayout.hidden.includes(id)}{@render modBody(id)}{/if}
{/each}
