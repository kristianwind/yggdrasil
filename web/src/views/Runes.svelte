<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let runes = $state([]);
  let uploading = $state(false);

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
  <label class="btn-primary cursor-pointer">
    {uploading ? "Carving…" : "Carve a rune (upload)"}
    <input type="file" accept=".yaml,.yml" class="hidden" onchange={upload} />
  </label>
</div>
<p class="text-muted mb-6">A Rune is a declarative game definition. Upload your own YAML to add new games.</p>

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
