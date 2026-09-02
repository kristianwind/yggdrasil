<script>
  // Statistics — where the machine's CPU, RAM and disk actually go.
  //
  // The Dashboard's three host sparklines can say the box is at 93%, but never
  // which of the things on it is responsible. This page answers that, and it
  // deliberately answers it twice, because the two answers are different:
  //
  //   - the FLEET view ranks servers against each other, and
  //   - the FILESYSTEM view splits the disk into server data, Docker and the rest.
  //
  // On a panel box the second one is usually the real story: Restart re-pulls an
  // image and leaves the superseded one untagged, so Docker accumulates without
  // limit while the servers barely move. Showing only a per-server ranking would
  // answer the question confidently and point at the wrong thing.
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api.js";
  import { livePoll } from "../lib/livePoll.js";
  import { navigate } from "../lib/router.js";
  import { confirmDialog } from "../lib/dialog.js";
  import { toast } from "../lib/toast.js";
  import Sparkline from "../components/Sparkline.svelte";

  let stats = $state(null);
  let error = $state("");
  let hostMetrics = $state([]);
  let hours = $state(24);
  let metric = $state("disk"); // which column the fleet table ranks by
  let stop;

  async function load() {
    try {
      stats = await api.get("/system/stats");
      error = "";
    } catch (e) {
      error = e.message;
    }
  }
  async function loadHistory() {
    try {
      hostMetrics = await api.get(`/system/metrics?hours=${hours}`);
    } catch {
      hostMetrics = [];
    }
  }

  onMount(() => {
    load();
    stop = livePoll(load, 10000);
  });
  onDestroy(() => stop?.());
  $effect(() => {
    hours; // tracked, so changing the window reloads
    loadHistory();
  });

  const cpuSeries = $derived(hostMetrics.map((m) => m.cpu));
  const ramSeries = $derived(
    hostMetrics.map((m) => (m.mem_total_mb > 0 ? (m.mem_used_mb / m.mem_total_mb) * 100 : -1)),
  );
  const diskSeries = $derived(
    hostMetrics.map((m) => (m.disk_total_mb > 0 ? (m.disk_used_mb / m.disk_total_mb) * 100 : -1)),
  );

  function fmtBytes(n) {
    if (n == null || isNaN(n)) return "—";
    if (n < 1024) return `${n} B`;
    const u = ["KB", "MB", "GB", "TB"];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < u.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${u[i]}`;
  }
  const pct = (a, b) => (b > 0 ? (a / b) * 100 : 0);

  // The disk split, as segments of one bar. Order is fixed rather than sorted by
  // size so the bar does not reshuffle itself under the reader between polls.
  //
  // Colour carries the grouping, because the grouping is the point of the chart:
  // one green for what is yours, one hue in three weights for the three things
  // Docker holds, and neutral grey for the remainder. The first cut used
  // --c-accent for volumes and --c-accent2 for server data, which is worse than
  // it sounds: in the LIGHT theme those two variables are the same green
  // (45 164 78), so the legend showed two identical swatches for the two
  // categories the page most wants you to tell apart — and the greens visually
  // filed Docker's volumes alongside server data, which is exactly the wrong
  // reading. Never assume accent and accent2 differ; in one theme they do not.
  const segments = $derived.by(() => {
    const d = stats?.disk;
    if (!d) return [];
    const k = d.docker;
    const out = [
      { key: "servers", label: "Server data", bytes: d.server_data_bytes, color: "rgb(var(--c-accent2))" },
    ];
    if (k) {
      // Amber, not red: images filling the disk is the normal state of a panel
      // box, not a fault. It should draw the eye without reading as an alarm —
      // the alarm, if there is one, is the reclaimable box underneath.
      out.push(
        { key: "images", label: "Docker images", bytes: k.images_bytes, color: "rgb(var(--c-warn))" },
        { key: "volumes", label: "Docker volumes", bytes: k.volumes_bytes, color: "rgb(var(--c-warn) / 0.62)" },
        { key: "build", label: "Build cache", bytes: k.build_cache_bytes, color: "rgb(var(--c-warn) / 0.34)" },
      );
    }
    out.push({ key: "other", label: "Everything else", bytes: d.other_bytes, color: "rgb(var(--c-muted) / 0.75)" });
    return out.filter((s) => s.bytes > 0);
  });

  // Total space Docker would hand back to a prune, across all three of its pools.
  const reclaimable = $derived.by(() => {
    const k = stats?.disk?.docker;
    if (!k) return 0;
    return k.images_reclaimable + k.volumes_reclaimable + k.build_cache_reclaimable;
  });

  const ranked = $derived.by(() => {
    const list = [...(stats?.servers ?? [])];
    const val = (s) =>
      metric === "cpu" ? s.cpu_percent : metric === "mem" ? s.mem_mb : s.disk_mb < 0 ? -1 : s.disk_mb;
    return list.sort((a, b) => val(b) - val(a));
  });
  const rankMax = $derived.by(() => {
    const val = (s) =>
      metric === "cpu" ? s.cpu_percent : metric === "mem" ? s.mem_mb : Math.max(s.disk_mb, 0);
    return Math.max(1, ...ranked.map(val));
  });
  function rankValue(s) {
    if (metric === "cpu") return { v: s.cpu_percent, text: `${s.cpu_percent.toFixed(1)}%` };
    if (metric === "mem") return { v: s.mem_mb, text: fmtBytes(s.mem_mb * 1024 * 1024) };
    // disk_mb is -1 for a directory that has never been walked. Saying "0 B" there
    // would be a claim rather than an admission.
    if (s.disk_mb < 0) return { v: 0, text: "not measured yet", unknown: true };
    return { v: s.disk_mb, text: fmtBytes(s.disk_mb * 1024 * 1024) };
  }

  let pruning = $state(false);
  async function reclaim() {
    // Spell out what will and will not be touched. "Free up space" is the kind of
    // button people press without reading, and the reassuring half — that no
    // server can be affected — is the half worth putting in front of them.
    const ok = await confirmDialog({
      title: "Reclaim disk space",
      body:
        `This removes Docker images nothing refers to — untagged leftovers from ` +
        `re-pulls — and should free about ${fmtBytes(reclaimable)}.\n\n` +
        `No server is affected. Images in use by a container, running or stopped, ` +
        `cannot be removed and are not touched.`,
      confirmText: "Reclaim space",
    });
    if (!ok) return;
    pruning = true;
    try {
      const r = await api.post("/system/prune-images");
      toast(`Freed ${fmtBytes(r.freed_bytes)} from ${r.deleted} images.`, "success");
      await load();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      pruning = false;
    }
  }

  const diskPct = $derived(stats ? pct(stats.disk.used_bytes, stats.disk.total_bytes) : 0);
  const memPct = $derived(stats ? pct(stats.host.mem_used_mb, stats.host.mem_total_mb) : 0);
</script>

<div class="flex items-baseline justify-between flex-wrap gap-2">
  <div>
    <h1 class="text-xl font-semibold">Statistics</h1>
    <p class="text-muted text-sm mt-1">
      What this machine's CPU, memory and disk are being spent on — the whole box, and each server's
      share of it.
    </p>
  </div>
  <div class="inline-flex rounded-lg overflow-hidden border border-border text-xs">
    {#each [[24, "24h"], [72, "3d"], [168, "7d"]] as [h, lbl]}
      <button
        class="px-2 py-1 {hours === h ? 'bg-panel2 text-text' : 'text-muted hover:bg-panel2/50'}"
        onclick={() => (hours = h)}>{lbl}</button>
    {/each}
  </div>
</div>

{#if error}
  <div class="card p-4 mt-4 text-danger">{error}</div>
{:else if !stats}
  <div class="card p-4 mt-4 text-muted">Loading…</div>
{:else}
  <!-- Point-in-time, then the same three as trends. -->
  <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mt-4">
    <div class="card p-3">
      <div class="text-xs uppercase tracking-wide text-muted">CPU</div>
      <div class="text-2xl font-semibold mt-1">
        {stats.host.cpu_percent >= 0 ? `${stats.host.cpu_percent.toFixed(0)}%` : "—"}
      </div>
      <div class="text-xs text-muted">{stats.host.cpu_count} cores</div>
    </div>
    <div class="card p-3">
      <div class="text-xs uppercase tracking-wide text-muted">Memory</div>
      <div class="text-2xl font-semibold mt-1">{memPct.toFixed(0)}%</div>
      <div class="text-xs text-muted">
        {fmtBytes(stats.host.mem_used_mb * 1024 * 1024)} of {fmtBytes(stats.host.mem_total_mb * 1024 * 1024)}
      </div>
    </div>
    <div class="card p-3">
      <div class="text-xs uppercase tracking-wide text-muted">Disk</div>
      <div class="text-2xl font-semibold mt-1 {diskPct >= 90 ? 'text-danger' : diskPct >= 80 ? 'text-warn' : ''}">
        {diskPct.toFixed(0)}%
      </div>
      <div class="text-xs text-muted">{fmtBytes(stats.disk.free_bytes)} free of {fmtBytes(stats.disk.total_bytes)}</div>
    </div>
  </div>

  {#if hostMetrics.length >= 2}
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mt-3">
      <Sparkline values={cpuSeries} label="CPU" unit="%" color="rgb(var(--c-accent2))" format={(v) => v.toFixed(0)} />
      <Sparkline values={ramSeries} label="RAM" unit="%" color="rgb(var(--c-accent))" format={(v) => v.toFixed(0)} />
      <Sparkline values={diskSeries} label="Disk" unit="%" color="rgb(var(--c-warn))" format={(v) => v.toFixed(0)} />
    </div>
  {/if}

  <!-- Where the disk went. This is the section that answers "we are at 93%". -->
  <div class="card p-4 mt-4">
    <div class="flex items-baseline justify-between flex-wrap gap-2">
      <h2 class="font-semibold">Where the disk went</h2>
      <span class="text-xs text-muted">
        {fmtBytes(stats.disk.used_bytes)} used of {fmtBytes(stats.disk.total_bytes)}
      </span>
    </div>

    {#if stats.disk.docker_error}
      <p class="text-sm text-warn mt-2">
        Docker could not be asked how much it is holding ({stats.disk.docker_error}), so only server
        data is broken out below.
      </p>
    {/if}

    <div class="flex h-3 rounded overflow-hidden mt-3 bg-panel2">
      {#each segments as seg}
        <!-- The separator is an inset shadow rather than a flex gap: gaps would push
             the widths past 100% and clip the last segment, quietly distorting the
             one thing the bar is for. -->
        <div
          style="width:{pct(seg.bytes, stats.disk.used_bytes)}%; background:{seg.color};
                 box-shadow: inset -1px 0 0 rgb(var(--c-panel))"
          title="{seg.label}: {fmtBytes(seg.bytes)}"></div>
      {/each}
    </div>

    <div class="mt-3 space-y-1.5">
      {#each segments as seg}
        <div class="flex items-center gap-2 text-sm">
          <span class="w-2.5 h-2.5 rounded-sm shrink-0" style="background:{seg.color}"></span>
          <span class="flex-1">{seg.label}</span>
          <span class="text-muted text-xs">{pct(seg.bytes, stats.disk.used_bytes).toFixed(0)}%</span>
          <span class="font-medium tabular-nums w-20 text-right">{fmtBytes(seg.bytes)}</span>
        </div>
      {/each}
    </div>

    {#if reclaimable > 0}
      <!-- The one actionable number on the page. Restart recreates a server and
           re-pulls its image, so the superseded one is left untagged and nothing
           ever removes it; on a long-lived box this is usually the largest single
           thing on the disk. The panel deliberately does not offer a button —
           deleting images is a host-level decision, and a prune while a pull is in
           flight is not something to trigger from a web page. -->
      <div class="mt-4 rounded-lg border border-border p-3 bg-panel2/40">
        <div class="text-sm">
          <span class="font-semibold">{fmtBytes(reclaimable)}</span> of that is reclaimable — layers,
          volumes and cache nothing refers to any more.
          {#if stats.disk.docker && stats.disk.docker.images_unused_count > 0}
            {stats.disk.docker.images_unused_count} of {stats.disk.docker.images_count} images are unused.
          {/if}
        </div>
        <p class="text-xs text-muted mt-2">
          These pile up because restarting a server re-pulls its image and leaves the old one behind
          untagged. Reclaiming removes only images nothing points at — no server is affected, and an
          image any container still uses cannot be removed even if it wanted to be.
        </p>
        <div class="flex items-center gap-3 mt-3 flex-wrap">
          <button class="btn {pruning ? 'is-busy' : ''}" onclick={reclaim} disabled={pruning}>
            {pruning ? "Reclaiming…" : "Reclaim space"}
          </button>
          <span class="text-xs text-muted">
            or on the host:
            <code class="px-1 py-0.5 rounded bg-panel2 text-text">sudo docker image prune -f</code>
          </span>
        </div>
      </div>
    {/if}

    {#if stats.disk.server_data_known < stats.disk.server_data_total}
      <p class="text-xs text-muted mt-3">
        Server data covers {stats.disk.server_data_known} of {stats.disk.server_data_total} servers —
        directories are measured once an hour, so a recently added server is not counted yet.
      </p>
    {/if}
  </div>

  <!-- Fleet ranking. -->
  <div class="card p-4 mt-4">
    <div class="flex items-baseline justify-between flex-wrap gap-2">
      <h2 class="font-semibold">Servers, by what they use</h2>
      <div class="inline-flex rounded-lg overflow-hidden border border-border text-xs">
        {#each [["disk", "Disk"], ["mem", "Memory"], ["cpu", "CPU"]] as [k, lbl]}
          <button
            class="px-2 py-1 {metric === k ? 'bg-panel2 text-text' : 'text-muted hover:bg-panel2/50'}"
            onclick={() => (metric = k)}>{lbl}</button>
        {/each}
      </div>
    </div>

    {#if metric === "disk"}
      <p class="text-xs text-muted mt-1">
        Data directories, measured hourly{stats.disk.server_data_sampled_at
          ? ` (last ${stats.disk.server_data_sampled_at} UTC)`
          : ""}. A stopped server still uses its disk.
      </p>
    {:else}
      <p class="text-xs text-muted mt-1">
        Latest sample within the last 15 minutes. A stopped server has no sample and reads as zero.
      </p>
    {/if}

    {#if ranked.length === 0}
      <p class="text-muted text-sm mt-3">No servers yet.</p>
    {:else}
      <div class="mt-3 space-y-1">
        {#each ranked as s (s.id)}
          {@const rv = rankValue(s)}
          <button
            class="w-full text-left rounded-lg px-2 py-1.5 hover:bg-panel2/60 transition"
            onclick={() => navigate(`/servers/${s.id}`)}>
            <div class="flex items-center gap-2 text-sm">
              <span
                class="w-1.5 h-1.5 rounded-full shrink-0 {s.status === 'running'
                  ? 'bg-accent2'
                  : 'bg-muted'}"></span>
              <span class="flex-1 truncate">{s.name}</span>
              <span class="text-xs {rv.unknown ? 'text-muted italic' : 'font-medium tabular-nums'}">
                {rv.text}
              </span>
            </div>
            <div class="h-1 rounded bg-panel2 mt-1 overflow-hidden">
              <div
                class="h-full rounded"
                style="width:{pct(rv.v, rankMax)}%; background:{metric === 'disk'
                  ? 'rgb(var(--c-warn))'
                  : metric === 'mem'
                    ? 'rgb(var(--c-accent))'
                    : 'rgb(var(--c-accent2))'}"></div>
            </div>
          </button>
        {/each}
      </div>
    {/if}
  </div>
{/if}
