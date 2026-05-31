<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let schedules = $state([]);
  let servers = $state([]);
  let realms = $state([]);
  let targets = $state([]);
  let templates = $state([]);
  let showCreate = $state(false);
  let form = $state(blank());

  function blank() {
    return {
      name: "",
      scope: "server",
      server_id: "",
      realm_id: "",
      cron_expr: "0 4 * * *",
      action: "restart",
      args: { skip_if_players: "true", target_id: "", command: "", template_id: "", minutes: "5" },
    };
  }

  const actions = [
    ["restart", "Restart"],
    ["start", "Start"],
    ["stop", "Stop"],
    ["backup", "Backup"],
    ["message", "In-game message"],
    ["command", "Console command"],
    ["update", "Update (re-install)"],
  ];

  async function load() {
    try {
      [schedules, servers, realms, targets, templates] = await Promise.all([
        api.get("/schedules"),
        api.get("/servers"),
        api.get("/realms").catch(() => []),
        api.get("/backup/targets").catch(() => []),
        api.get("/templates").catch(() => []),
      ]);
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(load);

  function serverName(id) {
    return servers.find((s) => s.id === id)?.name || id;
  }

  async function create() {
    if (!form.name) return toast("Name required", "warn");
    const payload = {
      name: form.name,
      cron_expr: form.cron_expr,
      action: form.action,
      server_id: form.scope === "server" ? form.server_id : "",
      realm_id: form.scope === "realm" ? form.realm_id : "",
      args: {},
    };
    if (form.scope === "server" && !form.server_id) return toast("Pick a server", "warn");
    if (form.scope === "realm" && !form.realm_id) return toast("Pick a realm", "warn");
    // Only include relevant args per action.
    if (form.action === "backup") payload.args.target_id = form.args.target_id;
    if (form.action === "command") payload.args.command = form.args.command;
    if (form.action === "message") {
      payload.args.template_id = form.args.template_id;
      payload.args.minutes = form.args.minutes;
    }
    if (form.action === "restart" || form.action === "update")
      payload.args.skip_if_players = form.args.skip_if_players;
    try {
      await api.post("/schedules", payload);
      toast("Schedule created", "success");
      showCreate = false;
      form = blank();
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function toggle(s) {
    try {
      await api.put(`/schedules/${s.id}`, { enabled: !s.enabled });
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function run(s) {
    try {
      await api.post(`/schedules/${s.id}/run`);
      toast("Triggered", "info");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function del(s) {
    if (!confirm(`Delete schedule "${s.name}"?`)) return;
    try {
      await api.del(`/schedules/${s.id}`);
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function scopeLabel(s) {
    if (s.server_id) return serverName(s.server_id);
    if (s.realm_id) return "realm: " + (realms.find((r) => r.id === s.realm_id)?.name || s.realm_id);
    return "all servers";
  }
</script>

<div class="flex items-center justify-between mb-6">
  <h1 class="text-2xl font-semibold">Schedules</h1>
  <button class="btn-primary" onclick={() => (showCreate = true)}>+ New schedule</button>
</div>

<div class="card divide-y divide-border">
  {#if schedules.length === 0}
    <div class="p-4 text-muted text-sm">
      No schedules yet. Automate backups, restarts, in-game warnings and updates.
    </div>
  {/if}
  {#each schedules as s}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1 min-w-0">
        <div class="font-medium">
          {s.name}
          <span class="badge bg-border text-muted ml-1">{s.action}</span>
          {#if !s.enabled}<span class="badge bg-danger/20 text-danger ml-1">disabled</span>{/if}
        </div>
        <div class="text-xs text-muted font-mono">{s.cron_expr} · {scopeLabel(s)}</div>
      </div>
      <button class="btn-ghost" onclick={() => run(s)}>Run now</button>
      <button class="btn-ghost" onclick={() => toggle(s)}>{s.enabled ? "Disable" : "Enable"}</button>
      <button class="btn-danger" onclick={() => del(s)}>Delete</button>
    </div>
  {/each}
</div>

<p class="text-muted text-xs mt-3">
  Cron format: <code>min hour day month weekday</code> (5 fields), e.g. <code>0 4 * * *</code> =
  every day at 04:00. Restart/update can skip when players are online.
</p>

{#if showCreate}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-lg max-h-[90vh] overflow-auto p-5 space-y-3">
      <h2 class="text-lg font-semibold">New schedule</h2>
      <div>
        <label class="label" for="s-name">Name</label>
        <input id="s-name" class="input" bind:value={form.name} />
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="s-scope">Scope</label>
          <select id="s-scope" class="input" bind:value={form.scope}>
            <option value="server">Single server</option>
            <option value="realm">Realm</option>
            <option value="global">All servers</option>
          </select>
        </div>
        {#if form.scope === "server"}
          <div>
            <label class="label" for="s-server">Server</label>
            <select id="s-server" class="input" bind:value={form.server_id}>
              <option value="">— choose —</option>
              {#each servers as srv}<option value={srv.id}>{srv.name}</option>{/each}
            </select>
          </div>
        {:else if form.scope === "realm"}
          <div>
            <label class="label" for="s-realm">Realm</label>
            <select id="s-realm" class="input" bind:value={form.realm_id}>
              <option value="">— choose —</option>
              {#each realms as r}<option value={r.id}>{r.name}</option>{/each}
            </select>
          </div>
        {/if}
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="s-action">Action</label>
          <select id="s-action" class="input" bind:value={form.action}>
            {#each actions as [val, label]}<option value={val}>{label}</option>{/each}
          </select>
        </div>
        <div>
          <label class="label" for="s-cron">Cron (min hour day month weekday)</label>
          <input id="s-cron" class="input font-mono" bind:value={form.cron_expr} />
        </div>
      </div>

      {#if form.action === "backup"}
        <div>
          <label class="label" for="s-target">Backup target</label>
          <select id="s-target" class="input" bind:value={form.args.target_id}>
            <option value="">— choose —</option>
            {#each targets as t}<option value={t.id}>{t.name}</option>{/each}
          </select>
        </div>
      {:else if form.action === "command"}
        <div>
          <label class="label" for="s-cmd">Console / RCON command</label>
          <input id="s-cmd" class="input font-mono" bind:value={form.args.command} placeholder="say Hello!" />
        </div>
      {:else if form.action === "message"}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="s-tmpl">Message template</label>
            <select id="s-tmpl" class="input" bind:value={form.args.template_id}>
              <option value="">— choose —</option>
              {#each templates as t}<option value={t.id}>{t.name}</option>{/each}
            </select>
          </div>
          <div>
            <label class="label" for="s-min">{"{{minutes}}"} value</label>
            <input id="s-min" class="input" bind:value={form.args.minutes} />
          </div>
        </div>
      {:else if form.action === "restart" || form.action === "update"}
        <label class="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            class="accent-accent2 w-4 h-4"
            checked={form.args.skip_if_players === "true"}
            onchange={(e) => (form.args.skip_if_players = e.target.checked ? "true" : "false")}
          />
          Skip if players are online
        </label>
      {/if}

      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showCreate = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={create}>Create schedule</button>
      </div>
    </div>
  </div>
{/if}
