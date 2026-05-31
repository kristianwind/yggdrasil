<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let targets = $state([]);
  let showCreate = $state(false);
  let form = $state(blank());

  function blank() {
    return {
      name: "",
      type: "local",
      path: "",
      host: "",
      port: 0,
      username: "",
      password: "",
      share: "",
      keep_n: 0,
      keep_days: 0,
    };
  }

  async function load() {
    try {
      targets = await api.get("/backup/targets");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(load);

  async function create() {
    if (!form.name) return toast("Name required", "warn");
    try {
      await api.post("/backup/targets", { ...form, port: Number(form.port) || 0 });
      toast("Target created", "success");
      showCreate = false;
      form = blank();
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function test(t) {
    try {
      await api.post(`/backup/targets/${t.id}/test`);
      toast(`${t.name}: reachable`, "success");
    } catch (e) {
      toast(`${t.name}: ${e.message}`, "error");
    }
  }

  async function del(t) {
    if (!confirm(`Delete target "${t.name}"?`)) return;
    try {
      await api.del(`/backup/targets/${t.id}`);
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
</script>

<div class="flex items-center justify-between mb-2">
  <h1 class="text-2xl font-semibold">Settings — Backup targets</h1>
  <button class="btn-primary" onclick={() => (showCreate = true)}>+ New target</button>
</div>
<p class="text-muted mb-6">
  Where backups are stored. <b>Local</b> also covers an NFS or CIFS share already mounted on the
  host — just point the path at the mountpoint. <b>SFTP</b> and <b>SMB</b> connect directly.
  Credentials are encrypted at rest.
</p>

<div class="card divide-y divide-border">
  {#if targets.length === 0}
    <div class="p-4 text-muted text-sm">No backup targets yet.</div>
  {/if}
  {#each targets as t}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1">
        <div class="font-medium">{t.name} <span class="badge bg-border text-muted ml-1">{t.type}</span></div>
        <div class="text-xs text-muted">
          {t.host ? t.host + " · " : ""}{t.path || "(default)"}
          {#if t.keep_n || t.keep_days}
            · retention: {t.keep_n ? `keep ${t.keep_n}` : ""}{t.keep_n && t.keep_days ? " / " : ""}{t.keep_days
              ? `${t.keep_days}d`
              : ""}
          {/if}
        </div>
      </div>
      <button class="btn-ghost" onclick={() => test(t)}>Test</button>
      <button class="btn-danger" onclick={() => del(t)}>Delete</button>
    </div>
  {/each}
</div>

{#if showCreate}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-lg max-h-[90vh] overflow-auto p-5 space-y-3">
      <h2 class="text-lg font-semibold">New backup target</h2>
      <div>
        <label class="label" for="t-name">Name</label>
        <input id="t-name" class="input" bind:value={form.name} />
      </div>
      <div>
        <label class="label" for="t-type">Type</label>
        <select id="t-type" class="input" bind:value={form.type}>
          <option value="local">Local / NFS / CIFS mount</option>
          <option value="sftp">SFTP</option>
          <option value="smb">SMB / CIFS (direct)</option>
        </select>
      </div>

      {#if form.type !== "local"}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="t-host">Host</label>
            <input id="t-host" class="input" bind:value={form.host} />
          </div>
          <div>
            <label class="label" for="t-port">Port (optional)</label>
            <input id="t-port" class="input" type="number" bind:value={form.port} />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="t-user">Username</label>
            <input id="t-user" class="input" bind:value={form.username} />
          </div>
          <div>
            <label class="label" for="t-pass">Password</label>
            <input id="t-pass" class="input" type="password" bind:value={form.password} />
          </div>
        </div>
      {/if}

      {#if form.type === "smb"}
        <div>
          <label class="label" for="t-share">Share name</label>
          <input id="t-share" class="input" bind:value={form.share} />
        </div>
      {/if}

      <div>
        <label class="label" for="t-path">{form.type === "local" ? "Directory path" : "Remote path"}</label>
        <input id="t-path" class="input" bind:value={form.path} placeholder={form.type === "local" ? "/mnt/backups" : "backups"} />
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="t-keepn">Keep latest N (0 = ∞)</label>
          <input id="t-keepn" class="input" type="number" bind:value={form.keep_n} />
        </div>
        <div>
          <label class="label" for="t-keepd">Keep days (0 = ∞)</label>
          <input id="t-keepd" class="input" type="number" bind:value={form.keep_days} />
        </div>
      </div>

      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showCreate = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={create}>Create target</button>
      </div>
    </div>
  </div>
{/if}
