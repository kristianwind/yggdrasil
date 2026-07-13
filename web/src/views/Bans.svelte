<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let bans = $state([]);
  let servers = $state([]);
  let showCreate = $state(false);
  let form = $state({ player_name: "", player_id: "", server_id: "", reason: "" });

  // Violation auto-action rules
  let rules = $state([]);
  let showRule = $state(false);
  let ruleForm = $state(blankRule());
  function blankRule() {
    return { name: "", pattern: "", threshold: 3, window_minutes: 5, action: "ban", scope_global: true };
  }

  async function load() {
    try {
      [bans, servers, rules] = await Promise.all([
        api.get("/bans"),
        api.get("/servers"),
        api.get("/violations").catch(() => []),
      ]);
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(load);

  async function createRule() {
    if (!ruleForm.name || !ruleForm.pattern) return toast("Name and pattern required", "warn");
    try {
      await api.post("/violations", {
        ...ruleForm,
        threshold: Number(ruleForm.threshold),
        window_minutes: Number(ruleForm.window_minutes),
      });
      toast("Rule created", "success");
      showRule = false;
      ruleForm = blankRule();
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function toggleRule(rl) {
    try {
      await api.put(`/violations/${rl.id}`, { enabled: !rl.enabled });
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function deleteRule(rl) {
    if (!confirm(`Delete rule "${rl.name}"?`)) return;
    try {
      await api.del(`/violations/${rl.id}`);
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function create() {
    if (!form.player_name) return toast("Player name required", "warn");
    try {
      const res = await api.post("/bans", form);
      toast(`Banned (pushed to ${res.pushed} server${res.pushed === 1 ? "" : "s"})`, "success");
      showCreate = false;
      form = { player_name: "", player_id: "", server_id: "", reason: "" };
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function unban(b) {
    if (!confirm(`Unban ${b.player_name}?`)) return;
    try {
      const res = await api.del(`/bans/${b.id}`);
      toast(`Unbanned (pushed to ${res.pushed} server${res.pushed === 1 ? "" : "s"})`, "success");
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
</script>

<div class="flex items-center justify-between mb-2">
  <h1 class="text-2xl font-semibold">Bans</h1>
  <button class="btn-primary" onclick={() => (showCreate = true)}
    title="Add a player to the ban list and push the ban to the matching running server(s) — on one server or everywhere.">+ Ban a player</button>
</div>
<p class="text-muted mb-6">
  A cross-server ban list. Ban a player on one server or everywhere at once — the ban command is
  pushed to running servers whose game supports it (Minecraft, Rust, DayZ BattlEye).
</p>

<div class="card divide-y divide-border">
  {#if bans.length === 0}
    <div class="p-4 text-muted text-sm">No bans.</div>
  {/if}
  {#each bans as b}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1 min-w-0">
        <div class="font-medium">
          {b.player_name}
          <span class="badge bg-border text-muted ml-1">{b.server_name}</span>
        </div>
        <div class="text-xs text-muted truncate">
          {b.reason || "(no reason)"} · by {b.banned_by || "—"} · {b.created_at}
        </div>
      </div>
      <button class="btn-ghost" onclick={() => unban(b)}
        title="Remove this ban from the list and send the unban command to the affected server(s).">Unban</button>
    </div>
  {/each}
</div>

<!-- Violation auto-action rules -->
<div class="flex items-center justify-between mt-10 mb-2">
  <h2 class="text-xl font-semibold">Auto-action rules</h2>
  <button class="btn-primary" onclick={() => (showRule = true)}
    title="Create a rule that watches server logs and auto-kicks or auto-bans a player when a pattern recurs past a threshold.">+ New rule</button>
</div>
<p class="text-muted mb-4 text-sm">
  Watch running servers' logs for a regex pattern (capture group 1 = player). When it recurs past
  the threshold within the window, auto-kick or auto-ban — optionally across every server.
</p>
<div class="card divide-y divide-border">
  {#if rules.length === 0}
    <div class="p-4 text-muted text-sm">No rules.</div>
  {/if}
  {#each rules as rl}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1 min-w-0">
        <div class="font-medium">
          {rl.name}
          <span class="badge bg-border text-muted ml-1">{rl.action}{rl.scope_global ? " · global" : ""}</span>
          {#if !rl.enabled}<span class="badge bg-danger/20 text-danger ml-1">disabled</span>{/if}
        </div>
        <div class="text-xs text-muted font-mono truncate">
          /{rl.pattern}/ ≥{rl.threshold} in {rl.window_minutes}m
        </div>
      </div>
      <button class="btn-ghost" onclick={() => toggleRule(rl)}
        title={rl.enabled ? "Pause this rule — it stays but stops watching logs." : "Resume watching logs with this rule."}>{rl.enabled ? "Disable" : "Enable"}</button>
      <button class="btn-danger" onclick={() => deleteRule(rl)} title="Delete this auto-action rule.">Delete</button>
    </div>
  {/each}
</div>

{#if showRule}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-md p-5 space-y-3">
      <h2 class="text-lg font-semibold">New auto-action rule</h2>
      <div>
        <label class="label" for="r-name">Name</label>
        <input id="r-name" class="input" bind:value={ruleForm.name} placeholder="NoCheatPlus flags" />
      </div>
      <div>
        <label class="label" for="r-pattern">Log regex (group 1 = player)</label>
        <input id="r-pattern" class="input font-mono" bind:value={ruleForm.pattern}
          placeholder="Player (\\w+) failed .*check" />
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="r-thr">Threshold</label>
          <input id="r-thr" class="input" type="number" bind:value={ruleForm.threshold} />
        </div>
        <div>
          <label class="label" for="r-win">Window (minutes)</label>
          <input id="r-win" class="input" type="number" bind:value={ruleForm.window_minutes} />
        </div>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="r-action">Action</label>
          <select id="r-action" class="input" bind:value={ruleForm.action}>
            <option value="ban">Ban</option>
            <option value="kick">Kick</option>
          </select>
        </div>
        <label class="flex items-center gap-2 text-sm mt-6">
          <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={ruleForm.scope_global} />
          Ban on all servers
        </label>
      </div>
      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showRule = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={createRule}>Create rule</button>
      </div>
    </div>
  </div>
{/if}

{#if showCreate}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-md p-5 space-y-3">
      <h2 class="text-lg font-semibold">Ban a player</h2>
      <div>
        <label class="label" for="b-name">Player name / ID</label>
        <input id="b-name" class="input" bind:value={form.player_name} placeholder="Notch or SteamID/GUID" />
      </div>
      <div>
        <label class="label" for="b-scope">Scope</label>
        <select id="b-scope" class="input" bind:value={form.server_id}>
          <option value="">All servers</option>
          {#each servers as s}<option value={s.id}>{s.name}</option>{/each}
        </select>
      </div>
      <div>
        <label class="label" for="b-reason">Reason</label>
        <input id="b-reason" class="input" bind:value={form.reason} placeholder="Cheating" />
      </div>
      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showCreate = false)}>Cancel</button>
        <button class="btn-danger flex-1" onclick={create}>Ban</button>
      </div>
    </div>
  </div>
{/if}
