<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { confirmDialog, promptDialog } from "../lib/dialog.js";
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

  // Which installed runes the community catalog has moved past. A rune's version
  // is the only signal that a local copy has drifted from its source, and nothing
  // compared the two — you had to open Browse GitHub and check by eye.
  //
  // Admin-only, like the rest of rune management. Failure is quiet on purpose:
  // GitHub being unreachable shouldn't put an error over a page that otherwise
  // works, and the endpoint returns a note rather than pretending all is current.
  let runeUpdates = $state({});
  let updatingRune = $state(null);
  async function loadRuneUpdates() {
    if (!isAdmin) return;
    try {
      const r = await api.get("/gameskills/updates");
      runeUpdates = Object.fromEntries((r.updates ?? []).map((u) => [u.id, u]));
    } catch {
      runeUpdates = {};
    }
  }

  async function updateRune(u) {
    if (
      !(await confirmDialog({
        title: `Update ${u.name}`,
        body:
          `Update from v${u.installed_version} to v${u.available_version}?\n\n` +
          `This replaces the rune definition. Existing servers keep their own settings, ` +
          `but they are built from this rune — so the new version applies the next time one ` +
          `is started, restarted or reinstalled.`,
        confirmText: "Update",
      }))
    )
      return;
    updatingRune = u.id;
    try {
      await api.post("/gameskills/install-from-github", { download_url: u.download_url });
      toast(`${u.name} updated to v${u.available_version}`, "success");
      await load();
      await loadRuneUpdates();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      updatingRune = null;
    }
  }

  onMount(async () => {
    await load();
    loadRuneUpdates();
  });

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

  // Import a plain docker-compose file as a rune.
  let showCompose = $state(false);
  let composeText = $state("");
  let composeName = $state("");
  let composeMain = $state("");
  let composeBusy = $state(false);
  let composeWarnings = $state([]);
  let composeDoneId = $state("");
  async function importCompose() {
    if (!composeName.trim()) return toast("Give the app a name", "warn");
    if (!composeText.trim()) return toast("Paste a docker-compose file", "warn");
    composeBusy = true;
    composeWarnings = [];
    composeDoneId = "";
    try {
      const res = await api.post("/gameskills/import-compose", {
        name: composeName.trim(),
        compose: composeText,
        main: composeMain.trim(),
      });
      await load();
      composeWarnings = res.warnings || [];
      composeDoneId = res.id;
      if (composeWarnings.length) {
        toast(`Rune “${res.name}” created — review the notes`, "success");
      } else {
        toast(`Rune “${res.name}” created`, "success");
        showCompose = false;
        navigate("/servers?new=" + res.id);
      }
    } catch (e) {
      toast(e.message, "error");
    } finally {
      composeBusy = false;
    }
  }

  // Restart every running server built from this rune.
  //
  // A rune edit does not reach a running server on its own — Restart recreates the
  // container, which is how a changed PUID, startup command or image gets picked
  // up. Before this you had to remember which servers used the rune and click each
  // one; on the game box that is eight Minecraft servers.
  //
  // The list is fetched and named in the confirm on purpose. "Restart 8 servers" is
  // a different decision from "restart the one you were looking at", and the only
  // honest way to ask is to say which ones.
  async function restartServers(r) {
    let affected;
    try {
      const res = await api.get(`/gameskills/${r.id}/servers`);
      affected = res.servers || [];
    } catch (e) {
      toast(e.message, "error");
      return;
    }
    if (!affected.length) {
      toast("No running servers use this rune", "info");
      return;
    }
    const names = affected.map((x) => x.name).join(", ");
    const ok = await confirmDialog({
      title: `Restart ${affected.length} server${affected.length > 1 ? "s" : ""}?`,
      body:
        `${names}\n\n` +
        "Each is recreated so it picks up the current rune, one at a time — players " +
        "come back one server at a time too. Stopped servers are left alone; they " +
        "pick the rune up when someone starts them.",
      confirmText: "Restart them",
    });
    if (!ok) return;
    try {
      const res = await api.post(`/gameskills/${r.id}/restart-servers`, {});
      toast(`Restarting ${res.started} server(s) — you'll get a message when it's done`, "success");
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function del(r) {
    const extra = r.builtin
      ? " This is a built-in default rune — it won't be re-added on restart."
      : "";
    if (!(await confirmDialog({ title: "Delete rune", body: `Delete "${r.name}"?${extra}`, danger: true, confirmText: "Delete" }))) return;
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
  let ghError = $state("");
  let ghBusy = $state(""); // download_url currently installing
  let ghRepo = $state("kristianwind/yggdrasil");
  let ghPath = $state("community-runes");
  let ghRef = $state("main");
  let repos = $state([]); // saved repositories (incl. the built-in catalog)
  let savingRepo = $state(false);
  let ghToken = $state(""); // optional token for THIS repo; never read back from the server
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
    loadRepos();
    if (!ghData) loadGithub(false);
  }
  async function loadRepos() {
    repos = await api.get("/rune-repos").catch(() => []);
  }
  function pickRepo(rp) {
    ghRepo = rp.repo;
    ghPath = rp.path;
    ghRef = rp.ref || "main";
    loadGithub(false);
  }
  async function saveRepo() {
    savingRepo = true;
    try {
      await api.post("/rune-repos", {
        name: ghRepo.trim(), repo: ghRepo.trim(), path: ghPath.trim(), ref: ghRef.trim(),
        token: ghToken.trim(),
      });
      toast("Repository saved", "success");
      ghToken = "";
      await loadRepos();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingRepo = false;
    }
  }
  // Attach (or clear) a token on a repository that's already saved, without
  // having to remove and re-add it.
  async function saveRepoToken() {
    if (!currentRepo || currentRepo.default) return;
    savingRepo = true;
    try {
      await api.put(`/rune-repos/${currentRepo.id}`, { token: ghToken.trim() });
      toast(ghToken.trim() ? "Token saved for this repository" : "Token removed", "success");
      ghToken = "";
      await loadRepos();
      loadGithub(true);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingRepo = false;
    }
  }
  async function removeRepo(rp) {
    if (!(await confirmDialog({ title: "Remove repository", body: `Remove "${rp.name}"?`, danger: true, confirmText: "Remove" }))) return;
    try {
      await api.del(`/rune-repos/${rp.id}`);
      await loadRepos();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  const currentRepo = $derived(
    repos.find((rp) => rp.repo === ghRepo.trim() && rp.path === ghPath.trim()),
  );
  const repoSaved = $derived(!!currentRepo);
  // The listing is cached for ten minutes, and nothing said so. A rune updated
  // in the catalogue and then updated from here returned the OLD version twice
  // in a row, with the button appearing to work both times. Showing the age —
  // and offering the refetch that already existed behind ?refresh=1 — is the
  // difference between "nothing happened" and "you are looking at a copy".
  function cacheAge(seconds) {
    if (!seconds || seconds < 60) return "just now";
    const m = Math.round(seconds / 60);
    return m === 1 ? "1 min ago" : `${m} min ago`;
  }

  async function loadGithub(refresh) {
    ghLoading = true;
    ghError = "";
    try {
      const q = new URLSearchParams({ repo: ghRepo.trim(), path: ghPath.trim() });
      if (ghRef.trim()) q.set("ref", ghRef.trim());
      if (refresh) q.set("refresh", "1");
      ghData = await api.get(`/gameskills/github?${q}`);
    } catch (e) {
      // Keep the reason visible in the dialog — a toast disappears before it's read,
      // and the message says whether this is a private repo, a bad ref or a rate limit.
      ghError = e.message || "Couldn't load the listing.";
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
    <button class="btn-ghost" onclick={() => { showCompose = true; composeWarnings = []; composeDoneId = ""; }}
      title="Paste a docker-compose file and turn it into a rune — for bringing an existing compose app into the panel.">
      From compose
    </button>
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
            <td class="px-4 py-2 text-muted whitespace-nowrap">
              v{r.version}
              {#if runeUpdates[r.id]}
                <button class="badge bg-warn/20 text-warn ml-1 hover:bg-warn/30"
                  disabled={updatingRune === r.id}
                  onclick={() => updateRune(runeUpdates[r.id])}
                  title={`The catalog has v${runeUpdates[r.id].available_version}. Click to update this rune.`}>
                  {updatingRune === r.id ? "updating…" : `↑ v${runeUpdates[r.id].available_version}`}
                </button>
              {/if}
            </td>
            <td class="px-4 py-2 text-right whitespace-nowrap">
              {#if r.creatable}
                <button class="btn-primary px-2 py-1" onclick={() => createServer(r)}
                  title="Create a new game server from this rune — you'll pick a name and settings next.">Create server</button>
              {/if}
              {#if isAdmin}
                <button class="btn-ghost px-2 py-1 ml-1" onclick={() => restartServers(r)}
                  title="Recreate every running server built from this rune, one at a time, so they pick up the current version of it. Stopped servers are left alone.">Restart its servers</button>
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
        <div class="text-xs text-muted mt-1">
          {r.category} · v{r.version}
          {#if runeUpdates[r.id]}
            <button class="badge bg-warn/20 text-warn ml-1 hover:bg-warn/30"
              disabled={updatingRune === r.id}
              onclick={() => updateRune(runeUpdates[r.id])}
              title={`The catalog has v${runeUpdates[r.id].available_version}. Click to update this rune.`}>
              {updatingRune === r.id ? "updating…" : `↑ v${runeUpdates[r.id].available_version}`}
            </button>
          {/if}
        </div>
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
        <button class="btn-ghost px-2 py-1" aria-label="Close" onclick={() => (ghOpen = false)}>✕</button>
      </div>
      <p class="text-muted text-sm">
        Install community runes directly from a repo's folder of YAML files — no manual download.
        Runes remember where they came from, so you're told when any of their source repos has a newer version.
      </p>

      <!-- Saved repositories: pick one to switch sources, or save the current one -->
      {#if repos.length}
        <div class="flex flex-wrap gap-1.5 items-center">
          <span class="text-xs text-muted">Repositories:</span>
          {#each repos as rp}
            <span class="inline-flex items-center rounded-full border border-border text-xs overflow-hidden">
              <button class="px-2.5 py-1 hover:bg-panel2 {ghRepo === rp.repo && ghPath === rp.path ? 'bg-accent/15 text-accent' : ''}"
                onclick={() => pickRepo(rp)}
                title="{rp.repo}/{rp.path} @ {rp.ref}{rp.has_token ? ' · uses its own GitHub token' : ''}"
                >{rp.name}{#if rp.has_token}<span class="ml-1 text-xs" aria-label="has its own token">🔑</span>{/if}</button>
              {#if !rp.default}
                <button class="px-1.5 py-1 text-muted hover:text-danger border-l border-border" title="Remove this repository" onclick={() => removeRepo(rp)}>✕</button>
              {/if}
            </span>
          {/each}
        </div>
      {/if}

      <div class="flex flex-wrap gap-2 items-end">
        <div class="flex-1 min-w-[10rem]">
          <label class="label" for="ghRepo">Repository (owner/name)</label>
          <input id="ghRepo" class="input" bind:value={ghRepo} placeholder="kristianwind/yggdrasil" />
        </div>
        <div class="flex-1 min-w-[8rem]">
          <label class="label" for="ghPath">Folder</label>
          <input id="ghPath" class="input" bind:value={ghPath} placeholder="community-runes" />
        </div>
        <div class="w-24">
          <label class="label" for="ghRef">Branch</label>
          <input id="ghRef" class="input" bind:value={ghRef} placeholder="main" />
        </div>
        <div class="flex-1 min-w-[9rem]">
          <label class="label" for="ghToken">Token for this repo (optional)</label>
          <input id="ghToken" class="input" type="password" autocomplete="off" bind:value={ghToken}
            placeholder={currentRepo?.has_token ? "•••••••• stored" : "ghp_… / github_pat_…"} />
        </div>
        <button class="btn-ghost" onclick={() => loadGithub(true)} disabled={ghLoading}>
          {ghLoading ? "Loading…" : "Reload"}
        </button>
        {#if !repoSaved && ghRepo.trim()}
          <button class="btn-ghost" onclick={saveRepo} disabled={savingRepo}
            title="Save this repository so you can switch back to it later.">{savingRepo ? "Saving…" : "＋ Save repo"}</button>
        {:else if currentRepo && !currentRepo.default && (ghToken.trim() || currentRepo.has_token)}
          <button class="btn-ghost" onclick={saveRepoToken} disabled={savingRepo}
            title="Use this token for this repository instead of the panel-wide one. Leave the field empty to remove it.">
            {savingRepo ? "Saving…" : ghToken.trim() ? "Save token" : "Remove token"}</button>
        {/if}
      </div>
      <p class="text-muted text-xs -mt-1">
        A token here is used for this repository only, in place of the panel-wide one under
        Settings → Integrations. That's what makes two private repos with different owners
        reachable at the same time — a fine-grained token can only ever select repos its own
        owner owns. It's stored encrypted and never shown again.
      </p>

      {#if ghLoading}
        <div class="text-muted text-sm">Fetching from GitHub…</div>
      {:else if !ghData}
        <div class="text-sm text-danger">{ghError || "Couldn't load the listing — check the repo/folder and try Reload."}</div>
        <div class="text-muted text-xs mt-1">
          Folder is a path inside the repo (e.g. <span class="font-mono">yggdrasil</span>), not a GitHub URL —
          leave out <span class="font-mono">/tree/&lt;branch&gt;/</span>. A private repository needs a
          token — either the panel-wide one under Settings → Integrations, or one saved for this
          repository in the field above (required when the repo belongs to someone else).
        </div>
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
        <p class="text-muted text-xs flex items-center gap-2 flex-wrap">
          <span>{ghData.repo}/{ghData.path} @ {ghData.ref}</span>
          {#if ghData.from_cache}
            <span>· cached {cacheAge(ghData.cache_seconds)}</span>
            <button class="underline hover:text-text" onclick={() => loadGithub(true)}>
              fetch again
            </button>
          {:else}
            <span>· just fetched</span>
          {/if}
        </p>
      {/if}
    </div>
  </div>
{/if}

{#if showCompose}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" onclick={(e) => { if (e.target === e.currentTarget && !composeBusy) showCompose = false; }}>
    <div class="card p-4 w-full max-w-2xl max-h-[90vh] overflow-auto space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">Import from docker-compose</h2>
        <button class="btn-ghost px-2 py-1" aria-label="Close" onclick={() => (showCompose = false)}>✕</button>
      </div>
      <p class="text-muted text-sm">
        Paste a <code>docker-compose.yml</code>. The panel turns it into a rune — the main service plus any
        supporting services as sidecars. Volumes with a host path (bind mounts) can't live in a rune; you'll
        get a note to add them as host mounts on the server.
      </p>
      <div>
        <label class="label" for="c-name">App name</label>
        <input id="c-name" class="input" placeholder="e.g. My App" bind:value={composeName} />
      </div>
      <div>
        <label class="label" for="c-main">Main service (optional)</label>
        <input id="c-main" class="input" placeholder="auto-detected from published ports" bind:value={composeMain} />
      </div>
      <div>
        <label class="label" for="c-yaml">docker-compose.yml</label>
        <textarea id="c-yaml" class="input font-mono text-xs h-56" placeholder="services:&#10;  app:&#10;    image: ..." bind:value={composeText}></textarea>
      </div>

      {#if composeWarnings.length}
        <div class="card bg-warn/10 border border-warn/30 p-3">
          <div class="font-medium text-sm mb-1">Rune created — review these notes:</div>
          <ul class="text-xs text-muted list-disc pl-4 space-y-1">
            {#each composeWarnings as wmsg}<li>{wmsg}</li>{/each}
          </ul>
        </div>
      {/if}

      <div class="flex gap-2 pt-1">
        {#if composeDoneId}
          <button class="btn-primary flex-1" onclick={() => { showCompose = false; navigate("/servers?new=" + composeDoneId); }}>
            Create a server from it →</button>
          <button class="btn-ghost" onclick={() => (showCompose = false)}>Close</button>
        {:else}
          <button class="btn-ghost flex-1" onclick={() => (showCompose = false)} disabled={composeBusy}>Cancel</button>
          <button class="btn-primary flex-1" onclick={importCompose} disabled={composeBusy}>
            {composeBusy ? "Converting…" : "Convert to rune"}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}
