<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let bans = $state([]);
  let servers = $state([]);
  let showCreate = $state(false);
  let form = $state({ player_name: "", player_id: "", server_id: "", reason: "" });

  async function load() {
    try {
      [bans, servers] = await Promise.all([api.get("/bans"), api.get("/servers")]);
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(load);

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
  <button class="btn-primary" onclick={() => (showCreate = true)}>+ Ban a player</button>
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
      <button class="btn-ghost" onclick={() => unban(b)}>Unban</button>
    </div>
  {/each}
</div>

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
