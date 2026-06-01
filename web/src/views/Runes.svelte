<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let runes = $state([]);
  let uploading = $state(false);

  // View mode (grid cards vs. compact table), remembered per browser.
  let view = $state(localStorage.getItem("ygg_runes_view") || "grid");
  function setView(v) {
    view = v;
    localStorage.setItem("ygg_runes_view", v);
  }

  async function load() {
    try {
      runes = await api.get("/gameskills");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(load);

  async function upload(e) {
    const file = e.target.files?.[0];
    if (!file) return;
    uploading = true;
    try {
      const text = await file.text();
      const res = await fetch("/api/gameskills", {
        method: "POST",
        headers: { Authorization: `Bearer ${localStorage.getItem("ygg_token") || ""}` },
        body: text,
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || "upload failed");
      toast(`Carved rune: ${data.name}`, "success");
      await load();
    } catch (err) {
      toast(err.message, "error");
    } finally {
      uploading = false;
      e.target.value = "";
    }
  }

  async function importEgg(e) {
    const file = e.target.files?.[0];
    if (!file) return;
    uploading = true;
    try {
      const text = await file.text();
      const res = await fetch("/api/gameskills/import-egg", {
        method: "POST",
        headers: { Authorization: `Bearer ${localStorage.getItem("ygg_token") || ""}` },
        body: text,
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || "import failed");
      toast(`Imported egg: ${data.name}`, "success");
      await load();
    } catch (err) {
      toast(err.message, "error");
    } finally {
      uploading = false;
      e.target.value = "";
    }
  }

  async function importXml(e) {
    const file = e.target.files?.[0];
    if (!file) return;
    uploading = true;
    try {
      const text = await file.text();
      const res = await fetch("/api/gameskills/import-xml", {
        method: "POST",
        headers: { Authorization: `Bearer ${localStorage.getItem("ygg_token") || ""}` },
        body: text,
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || "import failed");
      toast(`Imported: ${data.name}`, "success");
      await load();
    } catch (err) {
      toast(err.message, "error");
    } finally {
      uploading = false;
      e.target.value = "";
    }
  }

  async function del(r) {
    if (!confirm(`Delete rune "${r.name}"?`)) return;
    try {
      await api.del(`/gameskills/${r.id}`);
      toast("Rune deleted", "success");
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
</script>

<div class="flex items-center justify-between mb-2">
  <h1 class="text-2xl font-semibold">Runes</h1>
  <div class="flex gap-2 items-center">
    {#if runes.length > 0}
      <div class="inline-flex rounded-md border border-border overflow-hidden mr-1">
        <button
          class="px-2.5 py-1.5 text-sm {view === 'grid' ? 'bg-panel2 text-fg' : 'text-muted hover:bg-panel2/50'}"
          title="Grid view"
          aria-label="Grid view"
          onclick={() => setView("grid")}>▦</button
        >
        <button
          class="px-2.5 py-1.5 text-sm border-l border-border {view === 'table' ? 'bg-panel2 text-fg' : 'text-muted hover:bg-panel2/50'}"
          title="Table view"
          aria-label="Table view"
          onclick={() => setView("table")}>☰</button
        >
      </div>
    {/if}
    <label class="btn-ghost cursor-pointer">
      Import egg
      <input type="file" accept=".json" class="hidden" onchange={importEgg} />
    </label>
    <label class="btn-ghost cursor-pointer">
      Import XML
      <input type="file" accept=".xml" class="hidden" onchange={importXml} />
    </label>
    <label class="btn-primary cursor-pointer">
      {uploading ? "Carving…" : "Carve a rune (upload)"}
      <input type="file" accept=".yaml,.yml" class="hidden" onchange={upload} />
    </label>
  </div>
</div>
<p class="text-muted mb-6">A Rune is a declarative game definition. Upload your own YAML to add new games.</p>

{#if view === "table"}
  <div class="card overflow-x-auto">
    <table class="w-full text-sm">
      <thead class="text-muted text-xs uppercase tracking-wide border-b border-border">
        <tr>
          <th class="text-left font-medium px-4 py-2">Name</th>
          <th class="text-left font-medium px-4 py-2">Category</th>
          <th class="text-left font-medium px-4 py-2 hidden sm:table-cell">ID</th>
          <th class="text-left font-medium px-4 py-2">Version</th>
          <th class="text-right font-medium px-4 py-2">Actions</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each runes as r}
          <tr class="hover:bg-panel2/40">
            <td class="px-4 py-2">
              <span class="font-medium">{r.name}</span>
              {#if r.builtin}
                <span class="badge bg-border text-muted ml-2">built-in</span>
              {/if}
            </td>
            <td class="px-4 py-2 text-muted">{r.category}</td>
            <td class="px-4 py-2 text-muted font-mono text-xs hidden sm:table-cell">{r.id}</td>
            <td class="px-4 py-2 text-muted">v{r.version}</td>
            <td class="px-4 py-2 text-right">
              {#if !r.builtin}
                <button class="btn-danger px-2 py-1" onclick={() => del(r)}>Delete</button>
              {:else}
                <span class="text-muted">—</span>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{:else}
  <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
    {#each runes as r}
      <div class="card p-4">
        <div class="flex items-start justify-between">
          <div class="font-medium">{r.name}</div>
          {#if r.builtin}
            <span class="badge bg-border text-muted">built-in</span>
          {/if}
        </div>
        <div class="text-xs text-muted mt-1">{r.category} · v{r.version}</div>
        <div class="text-xs text-muted font-mono mt-1">{r.id}</div>
        {#if !r.builtin}
          <button class="btn-danger mt-3 w-full" onclick={() => del(r)}>Delete</button>
        {/if}
      </div>
    {/each}
  </div>
{/if}
