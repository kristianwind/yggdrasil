<script>
  import { onMount } from "svelte";
  import { api, getToken } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let entries = $state([]);
  let loading = $state(false);
  let filters = $state({ q: "", action: "", user: "", from: "", to: "" });

  function queryString() {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(filters)) if (v.trim()) p.set(k, v.trim());
    return p.toString();
  }

  async function load() {
    loading = true;
    try {
      const qs = queryString();
      entries = await api.get(`/audit?limit=500${qs ? "&" + qs : ""}`);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      loading = false;
    }
  }

  function clearFilters() {
    filters = { q: "", action: "", user: "", from: "", to: "" };
    load();
  }

  async function exportCsv() {
    try {
      const qs = queryString();
      const res = await fetch(`/api/audit/export${qs ? "?" + qs : ""}`, {
        headers: { Authorization: `Bearer ${getToken()}` },
      });
      if (!res.ok) throw new Error("export failed");
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "yggdrasil-audit.csv";
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      toast(e.message, "error");
    }
  }

  onMount(load);
</script>

<div class="flex items-center justify-between gap-3 mb-4">
  <h1 class="text-2xl font-semibold">Audit log</h1>
  <button class="btn-ghost shrink-0" onclick={exportCsv}>⬇ Export CSV</button>
</div>

<form
  class="card p-3 mb-4 grid gap-2 sm:grid-cols-2 lg:grid-cols-6 items-end"
  onsubmit={(e) => {
    e.preventDefault();
    load();
  }}
>
  <div class="lg:col-span-2">
    <label class="text-xs text-muted" for="a-q">Search</label>
    <input id="a-q" class="input" placeholder="user, action, resource…" bind:value={filters.q} />
  </div>
  <div>
    <label class="text-xs text-muted" for="a-action">Action</label>
    <input id="a-action" class="input" placeholder="e.g. server." bind:value={filters.action} />
  </div>
  <div>
    <label class="text-xs text-muted" for="a-user">User</label>
    <input id="a-user" class="input" placeholder="username" bind:value={filters.user} />
  </div>
  <div>
    <label class="text-xs text-muted" for="a-from">From</label>
    <input id="a-from" class="input" type="date" bind:value={filters.from} />
  </div>
  <div>
    <label class="text-xs text-muted" for="a-to">To</label>
    <input id="a-to" class="input" type="date" bind:value={filters.to} />
  </div>
  <div class="flex gap-2 sm:col-span-2 lg:col-span-6">
    <button class="btn-primary" type="submit" disabled={loading}>{loading ? "…" : "Apply"}</button>
    <button class="btn-ghost" type="button" onclick={clearFilters}>Clear</button>
    <span class="text-xs text-muted self-center ml-auto">{entries.length} entries</span>
  </div>
</form>

<div class="card overflow-auto">
  <table class="w-full text-sm">
    <thead class="text-muted text-xs uppercase">
      <tr class="border-b border-border">
        <th class="text-left px-4 py-2">Time</th>
        <th class="text-left px-4 py-2">User</th>
        <th class="text-left px-4 py-2">Action</th>
        <th class="text-left px-4 py-2">Resource</th>
        <th class="text-left px-4 py-2">IP</th>
      </tr>
    </thead>
    <tbody>
      {#each entries as e}
        <tr class="border-b border-border/50" title={e.detail || ""}>
          <td class="px-4 py-2 text-muted whitespace-nowrap">{e.ts}</td>
          <td class="px-4 py-2">{e.username || "—"}</td>
          <td class="px-4 py-2 font-mono">{e.action}</td>
          <td class="px-4 py-2 font-mono text-muted truncate max-w-xs">{e.resource}</td>
          <td class="px-4 py-2 text-muted whitespace-nowrap">{e.ip || "—"}</td>
        </tr>
      {/each}
    </tbody>
  </table>
  {#if entries.length === 0}
    <div class="p-4 text-muted text-sm">{loading ? "Loading…" : "No matching audit entries."}</div>
  {/if}
</div>
