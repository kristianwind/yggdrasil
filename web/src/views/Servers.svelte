<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { navigate, route } from "../lib/router.js";
  import { get } from "svelte/store";
  import VarForm from "../components/VarForm.svelte";

  let servers = $state([]);
  let realms = $state([]);
  let gameskills = $state([]);
  let loading = $state(true);

  // View mode (grid cards vs. compact table), remembered per browser.
  let view = $state(localStorage.getItem("ygg_servers_view") || "grid");
  function setView(v) {
    view = v;
    localStorage.setItem("ygg_servers_view", v);
  }

  // Create modal state
  let showCreate = $state(false);
  let selectedSkill = $state(null);
  let skillDetail = $state(null);
  let form = $state({ name: "", env: {}, cpu_percent: 0, memory_mb: 0 });
  let creating = $state(false);

  async function load() {
    loading = true;
    try {
      [servers, realms, gameskills] = await Promise.all([
        api.get("/servers"),
        api.get("/realms"),
        api.get("/gameskills"),
      ]);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      loading = false;
    }
  }
  onMount(async () => {
    await load();
    // Opened from the Dashboard's "+ New Server" button (#/servers?new=1):
    // launch the create modal, then drop the param so a refresh doesn't re-open it.
    if (get(route).query.get("new") && gameskills.length > 0) {
      await openCreate();
      navigate("/servers");
    }
  });

  // Group servers by realm name for display.
  let grouped = $derived.by(() => {
    const byId = Object.fromEntries(realms.map((r) => [r.id, r.name]));
    const g = {};
    for (const s of servers) {
      const key = byId[s.realm_id] || "Ungrouped";
      (g[key] ||= []).push(s);
    }
    return g;
  });

  async function openCreate() {
    selectedSkill = gameskills[0]?.id || null;
    await loadSkill();
    form = { name: "", env: {}, cpu_percent: 0, memory_mb: 0 };
    showCreate = true;
  }
  async function loadSkill() {
    if (!selectedSkill) return;
    try {
      skillDetail = await api.get(`/gameskills/${selectedSkill}`);
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function create() {
    if (!form.name) return toast("Name is required", "warn");
    creating = true;
    try {
      const env = {};
      for (const [k, v] of Object.entries(form.env)) env[k] = String(v);
      const res = await api.post("/servers", {
        name: form.name,
        gameskill_id: selectedSkill,
        env,
        cpu_percent: Number(form.cpu_percent) || 0,
        memory_mb: Number(form.memory_mb) || 0,
      });
      toast("Server created", "success");
      showCreate = false;
      navigate(`/servers/${res.id}`);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      creating = false;
    }
  }

  async function action(s, verb) {
    try {
      await api.post(`/servers/${s.id}/${verb}`);
      toast(`${s.name}: ${verb}`, "success");
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
</script>

<div class="flex items-center justify-between mb-6">
  <h1 class="text-2xl font-semibold">Servers</h1>
  <div class="flex items-center gap-2">
    {#if servers.length > 0}
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
    <button class="btn-primary" onclick={openCreate} disabled={gameskills.length === 0}>
      + New server
    </button>
  </div>
</div>

{#if loading}
  <div class="text-muted">Loading…</div>
{:else if servers.length === 0}
  <div class="card p-8 text-center text-muted">
    No servers yet. Click <b>New server</b> to deploy one from a Rune.
  </div>
{:else}
  {#each Object.entries(grouped) as [realm, list]}
    <h2 class="text-sm uppercase tracking-wide text-muted mt-6 mb-2">{realm}</h2>
    {#if view === "table"}
      <div class="card overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-muted text-xs uppercase tracking-wide border-b border-border">
            <tr>
              <th class="text-left font-medium px-4 py-2">Name</th>
              <th class="text-left font-medium px-4 py-2">Rune</th>
              <th class="text-left font-medium px-4 py-2">Status</th>
              <th class="text-left font-medium px-4 py-2 hidden sm:table-cell">Ports</th>
              <th class="text-right font-medium px-4 py-2">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each list as s}
              <tr class="hover:bg-panel2/40">
                <td class="px-4 py-2">
                  <a href={`#/servers/${s.id}`} class="font-medium hover:underline">{s.name}</a>
                </td>
                <td class="px-4 py-2 text-muted">{s.gameskill_id}</td>
                <td class="px-4 py-2">
                  <span
                    class="badge {s.status === 'running' ? 'bg-accent2/20 text-accent' : s.status === 'starting' ? 'bg-warn/20 text-warn' : 'bg-border text-muted'}"
                    >{s.status}</span
                  >
                </td>
                <td class="px-4 py-2 hidden sm:table-cell">
                  {#if s.ports && Object.keys(s.ports).length}
                    <div class="flex flex-wrap gap-1">
                      {#each Object.entries(s.ports) as [name, port]}
                        <span class="badge bg-panel2 border border-border text-muted font-mono text-[11px]">
                          {name}:{port}
                        </span>
                      {/each}
                    </div>
                  {:else}
                    <span class="text-muted">—</span>
                  {/if}
                </td>
                <td class="px-4 py-2">
                  <div class="flex gap-2 justify-end">
                    {#if s.status === "running" || s.status === "starting"}
                      <button class="btn-ghost px-2 py-1" onclick={() => action(s, "restart")}>Restart</button>
                      <button class="btn-ghost px-2 py-1" onclick={() => action(s, "stop")}>Stop</button>
                    {:else}
                      <button class="btn-primary px-2 py-1" onclick={() => action(s, "start")}>Start</button>
                    {/if}
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {#each list as s}
          <div class="card p-4">
            <div class="flex items-start justify-between">
              <a href={`#/servers/${s.id}`} class="font-medium hover:underline">{s.name}</a>
              <span
                class="badge {s.status === 'running' ? 'bg-accent2/20 text-accent' : s.status === 'starting' ? 'bg-warn/20 text-warn' : 'bg-border text-muted'}"
                >{s.status}</span
              >
            </div>
            <div class="text-xs text-muted mt-1">{s.gameskill_id}</div>
            {#if s.ports && Object.keys(s.ports).length}
              <div class="flex flex-wrap gap-1 mt-2">
                {#each Object.entries(s.ports) as [name, port]}
                  <span class="badge bg-panel2 border border-border text-muted font-mono text-[11px]">
                    {name}:{port}
                  </span>
                {/each}
              </div>
            {/if}
            <div class="flex gap-2 mt-3">
              {#if s.status === "running" || s.status === "starting"}
                <button class="btn-ghost flex-1" onclick={() => action(s, "restart")}>Restart</button>
                <button class="btn-ghost flex-1" onclick={() => action(s, "stop")}>Stop</button>
              {:else}
                <button class="btn-primary flex-1" onclick={() => action(s, "start")}>Start</button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/each}
{/if}

{#if showCreate}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-lg max-h-[90vh] overflow-auto p-5 space-y-4">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">New server</h2>
        <button class="btn-ghost px-2 py-1" onclick={() => (showCreate = false)}>✕</button>
      </div>

      <div>
        <label class="label" for="skill">Rune (game)</label>
        <select
          id="skill"
          class="input"
          bind:value={selectedSkill}
          onchange={loadSkill}
        >
          {#each gameskills as g}
            <option value={g.id}>{g.name}</option>
          {/each}
        </select>
      </div>

      <div>
        <label class="label" for="name">Server name</label>
        <input id="name" class="input" bind:value={form.name} placeholder="My Server" />
      </div>

      {#if skillDetail}
        <VarForm variables={skillDetail.variables || []} bind:values={form.env} />
      {/if}

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="cpu">CPU limit (%, 0=unlimited)</label>
          <input id="cpu" class="input" type="number" bind:value={form.cpu_percent} />
        </div>
        <div>
          <label class="label" for="mem">RAM limit (MB, 0=unlimited)</label>
          <input id="mem" class="input" type="number" bind:value={form.memory_mb} />
        </div>
      </div>

      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showCreate = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={create} disabled={creating}>
          {creating ? "Creating…" : "Create server"}
        </button>
      </div>
    </div>
  </div>
{/if}
