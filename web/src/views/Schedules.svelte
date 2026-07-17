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
  let editingId = $state(null); // null = creating, otherwise the schedule being edited
  let form = $state(blank());

  // Per-schedule run-log state (id → open / rows / loading).
  let logsOpen = $state({});
  let logsData = $state({});
  let logsLoading = $state({});

  function blank() {
    return {
      name: "",
      scope: "server",
      server_id: "",
      realm_id: "",
      cron_expr: "0 4 * * *",
      action: "restart",
      args: { skip_if_players: "true", target_id: "", command: "", template_id: "", minutes: "5", seconds: "30" },
    };
  }

  // The panel fills this one in itself, so the form never asks for it.
  const computedVars = ["server_name"];

  // The placeholders the chosen template actually uses. Asking per template beats
  // a fixed list of inputs: a countdown needs {{seconds}}, a backup warning needs
  // nothing at all, and a template someone writes tomorrow needs whatever it says.
  let templateVars = $derived(
    placeholders(templates.find((t) => t.id === form.args.template_id)?.body || "")
      .filter((v) => !computedVars.includes(v)),
  );

  function placeholders(body) {
    return [...new Set([...body.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]))];
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

  function openCreate() {
    editingId = null;
    form = blank();
    showCreate = true;
  }

  // Pre-fill the form from an existing schedule for editing.
  function openEdit(s) {
    editingId = s.id;
    form = {
      name: s.name,
      scope: s.server_id ? "server" : s.realm_id ? "realm" : "global",
      server_id: s.server_id || "",
      realm_id: s.realm_id || "",
      cron_expr: s.cron_expr,
      action: s.action,
      // merge stored args over the blank defaults so every field binds cleanly
      args: { ...blank().args, ...(s.args || {}) },
    };
    showCreate = true;
  }

  async function save() {
    if (!form.name) return toast("Name required", "warn");
    if (form.scope === "server" && !form.server_id) return toast("Pick a server", "warn");
    if (form.scope === "realm" && !form.realm_id) return toast("Pick a realm", "warn");
    const payload = {
      name: form.name,
      cron_expr: form.cron_expr,
      action: form.action,
      server_id: form.scope === "server" ? form.server_id : "",
      realm_id: form.scope === "realm" ? form.realm_id : "",
      args: {},
    };
    // Only include relevant args per action.
    if (form.action === "backup") payload.args.target_id = form.args.target_id;
    if (form.action === "command") payload.args.command = form.args.command;
    if (form.action === "message") {
      payload.args.template_id = form.args.template_id;
      for (const v of templateVars) {
        if (!form.args[v]) return toast(`Give {{${v}}} a value`, "warn");
        payload.args[v] = form.args[v];
      }
    }
    if (form.action === "restart" || form.action === "update")
      payload.args.skip_if_players = form.args.skip_if_players;
    try {
      if (editingId) {
        await api.put(`/schedules/${editingId}`, payload);
        toast("Schedule updated", "success");
      } else {
        await api.post("/schedules", payload);
        toast("Schedule created", "success");
      }
      showCreate = false;
      editingId = null;
      form = blank();
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function toggleLog(s) {
    const open = !logsOpen[s.id];
    logsOpen = { ...logsOpen, [s.id]: open };
    if (open) {
      logsLoading = { ...logsLoading, [s.id]: true };
      try {
        logsData = { ...logsData, [s.id]: await api.get(`/schedules/${s.id}/runs`) };
      } catch (e) {
        toast(e.message, "error");
      } finally {
        logsLoading = { ...logsLoading, [s.id]: false };
      }
    }
  }

  function fmtTime(t) {
    if (!t) return "";
    const d = new Date(t.includes("Z") || t.includes("+") ? t : t.replace(" ", "T") + "Z");
    return isNaN(d) ? t : d.toLocaleString();
  }
  function statusClass(st) {
    if (st === "ok") return "bg-accent2/20 text-accent2";
    if (st === "error") return "bg-danger/20 text-danger";
    return "bg-border text-muted"; // skipped / other
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
  <button class="btn-primary" onclick={openCreate}
    title="Create a recurring task on a cron schedule — backup, restart, update, in-game message or console command.">+ New schedule</button>
</div>

<div class="card divide-y divide-border">
  {#if schedules.length === 0}
    <div class="p-4 text-muted text-sm">
      No schedules yet. Automate backups, restarts, in-game warnings and updates.
    </div>
  {/if}
  {#each schedules as s}
    <div class="px-4 py-3">
      <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
        <div class="flex-1 min-w-0">
          <div class="font-medium">
            {s.name}
            <span class="badge bg-border text-muted ml-1">{s.action}</span>
            {#if !s.enabled}<span class="badge bg-danger/20 text-danger ml-1">disabled</span>{/if}
          </div>
          <div class="text-xs text-muted font-mono break-words">{s.cron_expr} · {scopeLabel(s)}</div>
        </div>
        <div class="flex flex-wrap gap-2 sm:justify-end sm:shrink-0">
          <button class="btn-ghost px-2" onclick={() => toggleLog(s)}
            title="Show the recent run history for this schedule (when it fired, and the result).">{logsOpen[s.id] ? "Hide log" : "Log"}</button>
          <button class="btn-ghost px-2" onclick={() => run(s)}
            title="Trigger this schedule once, right now, regardless of its cron time.">Run now</button>
          <button class="btn-ghost px-2" onclick={() => openEdit(s)} title="Edit this schedule's action, timing and scope.">Edit</button>
          <button class="btn-ghost px-2" onclick={() => toggle(s)}
            title={s.enabled ? "Pause this schedule — it stays but won't fire until re-enabled." : "Resume this schedule so it fires on its cron time again."}>{s.enabled ? "Disable" : "Enable"}</button>
          <button class="btn-danger px-2" onclick={() => del(s)} title="Delete this schedule permanently.">Delete</button>
        </div>
      </div>
      {#if logsOpen[s.id]}
        <div class="mt-3 rounded-md border border-border bg-panel2/40 p-2">
          {#if logsLoading[s.id]}
            <div class="text-xs text-muted px-1 py-1">Loading…</div>
          {:else if (logsData[s.id] || []).length === 0}
            <div class="text-xs text-muted px-1 py-1">No runs yet — fires on schedule or via “Run now”.</div>
          {:else}
            <table class="w-full text-xs">
              <thead class="text-muted">
                <tr>
                  <th class="text-left font-medium px-1 py-1">When</th>
                  <th class="text-left font-medium px-1 py-1">Server</th>
                  <th class="text-left font-medium px-1 py-1">Status</th>
                  <th class="text-left font-medium px-1 py-1">Detail</th>
                </tr>
              </thead>
              <tbody>
                {#each logsData[s.id] as run}
                  <tr class="border-t border-border/60">
                    <td class="px-1 py-1 whitespace-nowrap text-muted">{fmtTime(run.ran_at)}</td>
                    <td class="px-1 py-1">{run.server_name || "—"}</td>
                    <td class="px-1 py-1"><span class="badge {statusClass(run.status)}">{run.status}</span></td>
                    <td class="px-1 py-1 text-muted break-all">{run.detail}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/if}
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
      <h2 class="text-lg font-semibold">{editingId ? "Edit schedule" : "New schedule"}</h2>
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
          <select id="s-action" class="input" bind:value={form.action}
            title="What this schedule does when it fires. Restart recreates the container (applies rune/env/mod changes, no game update); Update re-runs install/SteamCMD to fetch the newest game version + mods; Backup archives the data; Message sends an in-game broadcast; Command runs a raw console/RCON command.">
            {#each actions as [val, label]}<option value={val}>{label}</option>{/each}
          </select>
        </div>
        <div>
          <label class="label" for="s-cron">Cron (min hour day month weekday)</label>
          <input id="s-cron" class="input font-mono" bind:value={form.cron_expr}
            title="Standard cron: minute hour day month weekday. Examples — '0 4 * * *' = every day at 04:00; '0 */6 * * *' = every 6 hours; '30 5 * * 1' = 05:30 every Monday." />
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
          {#each templateVars as v (v)}
            <div>
              <label class="label" for="s-var-{v}">{`{{${v}}}`} value</label>
              <input id="s-var-{v}" class="input" bind:value={form.args[v]} />
            </div>
          {/each}
        </div>
        {#if form.args.template_id}
          <p class="text-xs text-muted font-mono">
            {templates.find((t) => t.id === form.args.template_id)?.body}
          </p>
        {/if}
      {:else if form.action === "restart" || form.action === "update"}
        <label class="flex items-center gap-2 text-sm"
          title="If anyone is connected when this fires, skip it this time instead of interrupting them. The task runs on its next scheduled time when the server is empty.">
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
        <button class="btn-ghost flex-1" onclick={() => { showCreate = false; editingId = null; }}>Cancel</button>
        <button class="btn-primary flex-1" onclick={save}>{editingId ? "Save changes" : "Create schedule"}</button>
      </div>
    </div>
  </div>
{/if}
