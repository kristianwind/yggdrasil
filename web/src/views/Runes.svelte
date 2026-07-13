<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { navigate } from "../lib/router.js";
  import { user } from "../lib/auth.js";

  // Jump straight into the create-server flow with this rune pre-selected.
  const createServer = (r) => navigate("/servers?new=" + r.id);

  let runes = $state([]);
  let uploading = $state(false);

  const isAdmin = $derived($user?.role === "admin");
  // Non-admins only see runes they can create a server from; admins see all.
  let visibleRunes = $derived(isAdmin ? runes : runes.filter((r) => r.creatable));

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
    const extra = r.builtin
      ? " This is a built-in default rune — it won't be re-added on restart."
      : "";
    if (!confirm(`Delete rune "${r.name}"?${extra}`)) return;
    try {
      await api.del(`/gameskills/${r.id}`);
      toast("Rune deleted", "success");
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // --- Browse + install runes straight from a GitHub repo ---
  let ghOpen = $state(false);
  let ghLoading = $state(false);
  let ghData = $state(null);
  let ghBusy = $state(""); // download_url currently installing
  let ghRepo = $state("kristianwind/yggdrasil");
  let ghPath = $state("community-runes");
  let ghFilter = $state("");
  let ghFiltered = $derived(
    ((ghData && ghData.runes) || []).filter((r) => {
      const q = ghFilter.trim().toLowerCase();
      if (!q) return true;
      return `${r.name || r.filename} ${r.category || ""} ${r.id || ""} ${r.description || ""}`
        .toLowerCase()
        .includes(q);
    }),
  );

  function openGithub() {
    ghOpen = true;
    if (!ghData) loadGithub(false);
  }
  async function loadGithub(refresh) {
    ghLoading = true;
    try {
      const q = new URLSearchParams({ repo: ghRepo.trim(), path: ghPath.trim() });
      if (refresh) q.set("refresh", "1");
      ghData = await api.get(`/gameskills/github?${q}`);
    } catch (e) {
      toast(e.message, "error");
      ghData = null;
    } finally {
      ghLoading = false;
    }
  }
  async function installGh(rune) {
    ghBusy = rune.download_url;
    try {
      const r = await api.post("/gameskills/install-from-github", { download_url: rune.download_url });
      toast(`Installed rune: ${r.name}`, "success");
      await load(); // refresh the main list
      // mark it installed in the browser without a full GitHub re-fetch
      if (ghData) ghData.runes = ghData.runes.map((x) => (x.id === r.id ? { ...x, installed: true } : x));
    } catch (e) {
      toast(e.message, "error");
    } finally {
      ghBusy = "";
    }
  }
</script>

<!-- Title row: primary action sits on the heading line (like the Servers page). -->
<div class="flex items-center justify-between gap-2 mb-3">
  <h1 class="text-2xl font-semibold">Runes</h1>
  {#if isAdmin}
    <label class="btn-primary cursor-pointer shrink-0">
      {uploading ? "Carving…" : "Carve a rune (upload)"}
      <input type="file" accept=".yaml,.yml" class="hidden" onchange={upload} />
    </label>
  {/if}
</div>
<!-- Secondary actions: view toggle (everyone) + imports (admin), below the title. -->
<div class="flex flex-wrap items-center gap-2 mb-2">
  {#if visibleRunes.length > 0}
    <div class="inline-flex rounded-md border border-border overflow-hidden">
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
  {#if isAdmin}
    <button class="btn-ghost" onclick={openGithub}
      title="Browse and install community runes straight from a GitHub repo folder. Reinstalling an already-installed rune updates it to the latest version.">Browse GitHub</button>
    <label class="btn-ghost cursor-pointer" title="Import a Pterodactyl egg (.json) and convert it into a rune.">
      Import egg
      <input type="file" accept=".json" class="hidden" onchange={importEgg} />
    </label>
    <label class="btn-ghost cursor-pointer" title="Import a rune from an XML definition file.">
      Import XML
      <input type="file" accept=".xml" class="hidden" onchange={importXml} />
    </label>
  {/if}
</div>
<p class="text-muted mb-6">
  {#if isAdmin}
    A Rune is a declarative game/app definition. Upload your own YAML to add new ones.
  {:else}
    The games and apps you can deploy. Pick one and click <b>Create server</b>.
  {/if}
</p>

{#if visibleRunes.length === 0}
  <div class="card p-8 text-center text-muted">
    {isAdmin ? "No runes yet — carve or import one above." : "No runes available to you yet — ask an admin to grant create access."}
  </div>
{:else if view === "table"}
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
        {#each visibleRunes as r}
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
            <td class="px-4 py-2 text-right whitespace-nowrap">
              {#if r.creatable}
                <button class="btn-primary px-2 py-1" onclick={() => createServer(r)}
                  title="Create a new game server from this rune — you'll pick a name and settings next.">Create server</button>
              {/if}
              {#if isAdmin}
                <button class="btn-danger px-2 py-1 ml-1" onclick={() => del(r)}
                  title="Remove this rune from the panel. Existing servers built from it keep running, but you can't create new ones until it's re-added.">Delete</button>
              {/if}
              {#if !r.creatable && !(isAdmin)}
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
    {#each visibleRunes as r}
      <div class="card p-4">
        <div class="flex items-start justify-between">
          <div class="font-medium">{r.name}</div>
          {#if r.builtin}
            <span class="badge bg-border text-muted">built-in</span>
          {/if}
        </div>
        <div class="text-xs text-muted mt-1">{r.category} · v{r.version}</div>
        <div class="text-xs text-muted font-mono mt-1">{r.id}</div>
        {#if r.creatable || (isAdmin)}
          <div class="flex gap-2 mt-3">
            {#if r.creatable}
              <button class="btn-primary flex-1" onclick={() => createServer(r)}>Create server</button>
            {/if}
            {#if isAdmin}
              <button class="btn-danger {r.creatable ? 'px-4' : 'flex-1'}" onclick={() => del(r)}>Delete</button>
            {/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

{#if ghOpen}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-2xl max-h-[90vh] overflow-auto p-5 space-y-4">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">Browse runes on GitHub</h2>
        <button class="btn-ghost px-2 py-1" onclick={() => (ghOpen = false)}>✕</button>
      </div>
      <p class="text-muted text-sm">
        Install community runes directly from a repo's folder of YAML files — no manual download.
      </p>
      <div class="flex flex-wrap gap-2 items-end">
        <div class="flex-1 min-w-[12rem]">
          <label class="label" for="ghRepo">Repository (owner/name)</label>
          <input id="ghRepo" class="input" bind:value={ghRepo} placeholder="kristianwind/yggdrasil" />
        </div>
        <div class="flex-1 min-w-[10rem]">
          <label class="label" for="ghPath">Folder</label>
          <input id="ghPath" class="input" bind:value={ghPath} placeholder="community-runes" />
        </div>
        <button class="btn-ghost" onclick={() => loadGithub(true)} disabled={ghLoading}>
          {ghLoading ? "Loading…" : "Reload"}
        </button>
      </div>

      {#if ghLoading}
        <div class="text-muted text-sm">Fetching from GitHub…</div>
      {:else if !ghData}
        <div class="text-muted text-sm">Couldn't load the listing — check the repo/folder and try Reload.</div>
      {:else if !ghData.runes.length}
        <div class="text-muted text-sm">No <span class="font-mono">.yaml</span> runes found in that folder.</div>
      {:else}
        <input class="input" placeholder="Filter {ghData.runes.length} runes…" bind:value={ghFilter} />
        {#if !ghFiltered.length}
          <div class="text-muted text-sm">No runes match “{ghFilter}”.</div>
        {/if}
        <div class="card divide-y divide-border">
          {#each ghFiltered as r}
            <div class="flex items-center gap-3 p-3">
              <div class="min-w-0 flex-1">
                <div class="font-medium truncate">
                  {r.name || r.filename}
                  {#if r.category}<span class="text-muted text-xs font-normal">· {r.category}</span>{/if}
                </div>
                {#if r.description}
                  <div class="text-xs text-muted mt-0.5 line-clamp-2">{r.description}</div>
                {/if}
                {#if r.parse_error}
                  <div class="text-xs text-warn mt-0.5">⚠ {r.parse_error}</div>
                {:else if r.id}
                  <div class="text-xs text-muted font-mono mt-0.5">{r.id}</div>
                {/if}
              </div>
              {#if r.installed}
                <span class="badge bg-accent2/15 text-accent shrink-0">installed</span>
                <button class="btn-ghost text-xs shrink-0" onclick={() => installGh(r)} disabled={ghBusy === r.download_url}
                  title="Re-download this rune from GitHub and overwrite the installed copy with the latest version. All existing servers using it pick up the changes (no need to recreate them).">
                  Reinstall
                </button>
              {:else if !r.parse_error}
                <button class="btn-primary shrink-0" onclick={() => installGh(r)} disabled={ghBusy === r.download_url}>
                  {ghBusy === r.download_url ? "Installing…" : "Install"}
                </button>
              {/if}
            </div>
          {/each}
        </div>
        <p class="text-muted text-xs">
          {ghData.repo}/{ghData.path} @ {ghData.ref}
        </p>
      {/if}
    </div>
  </div>
{/if}
