<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { user } from "../lib/auth.js";

  let info = $state(null);
  let servers = $state([]);
  let error = $state("");

  onMount(async () => {
    try {
      servers = await api.get("/servers");
      if ($user.role === "admin") info = await api.get("/system/info");
    } catch (e) {
      error = e.message;
    }
  });

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

<h1 class="text-2xl font-semibold mb-1">Dashboard</h1>
<p class="text-muted mb-6">Welcome back, {$user.username}.</p>

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

<h2 class="text-lg font-semibold mb-3">Recent servers</h2>
<div class="card divide-y divide-border">
  {#if servers.length === 0}
    <div class="p-4 text-muted text-sm">No servers yet. Create one from the Servers page.</div>
  {/if}
  {#each servers.slice(0, 8) as s}
    <a href={`#/servers/${s.id}`} class="flex items-center justify-between px-4 py-3 hover:bg-panel2/50">
      <span class="font-medium">{s.name}</span>
      <span
        class="badge {s.status === 'running' ? 'bg-accent2/20 text-accent' : s.status === 'starting' ? 'bg-warn/20 text-warn' : 'bg-border text-muted'}"
        >{s.status}</span
      >
    </a>
  {/each}
</div>
