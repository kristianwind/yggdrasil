<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let entries = $state([]);

  onMount(async () => {
    try {
      entries = await api.get("/audit?limit=200");
    } catch (e) {
      toast(e.message, "error");
    }
  });
</script>

<h1 class="text-2xl font-semibold mb-6">Audit log</h1>

<div class="card overflow-auto">
  <table class="w-full text-sm">
    <thead class="text-muted text-xs uppercase">
      <tr class="border-b border-border">
        <th class="text-left px-4 py-2">Time</th>
        <th class="text-left px-4 py-2">User</th>
        <th class="text-left px-4 py-2">Action</th>
        <th class="text-left px-4 py-2">Resource</th>
      </tr>
    </thead>
    <tbody>
      {#each entries as e}
        <tr class="border-b border-border/50">
          <td class="px-4 py-2 text-muted whitespace-nowrap">{e.ts}</td>
          <td class="px-4 py-2">{e.username || "—"}</td>
          <td class="px-4 py-2 font-mono">{e.action}</td>
          <td class="px-4 py-2 font-mono text-muted">{e.resource}</td>
        </tr>
      {/each}
    </tbody>
  </table>
  {#if entries.length === 0}
    <div class="p-4 text-muted text-sm">No audit entries yet.</div>
  {/if}
</div>
