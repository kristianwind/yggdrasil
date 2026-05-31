<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let users = $state([]);
  let showCreate = $state(false);
  let form = $state({ username: "", password: "", role: "user" });

  async function load() {
    try {
      users = await api.get("/users");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(load);

  async function create() {
    if (!form.username || !form.password) return toast("Username and password required", "warn");
    try {
      await api.post("/users", form);
      toast("User created", "success");
      showCreate = false;
      form = { username: "", password: "", role: "user" };
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function toggleDisabled(u) {
    try {
      await api.put(`/users/${u.id}`, { disabled: !u.disabled });
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function del(u) {
    if (!confirm(`Delete user "${u.username}"?`)) return;
    try {
      await api.del(`/users/${u.id}`);
      toast("User deleted", "success");
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
</script>

<div class="flex items-center justify-between mb-6">
  <h1 class="text-2xl font-semibold">Users</h1>
  <button class="btn-primary" onclick={() => (showCreate = true)}>+ New user</button>
</div>

<div class="card divide-y divide-border">
  {#each users as u}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1">
        <div class="font-medium">{u.username}</div>
        <div class="text-xs text-muted">{u.role}{u.disabled ? " · disabled" : ""}</div>
      </div>
      <button class="btn-ghost" onclick={() => toggleDisabled(u)}>
        {u.disabled ? "Enable" : "Disable"}
      </button>
      <button class="btn-danger" onclick={() => del(u)}>Delete</button>
    </div>
  {/each}
</div>

<p class="text-muted text-xs mt-4">
  Scoped per-realm / per-game permissions arrive in a later phase. For now users are either
  global admins or basic users.
</p>

{#if showCreate}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-sm p-5 space-y-4">
      <h2 class="text-lg font-semibold">New user</h2>
      <div>
        <label class="label" for="un">Username</label>
        <input id="un" class="input" bind:value={form.username} />
      </div>
      <div>
        <label class="label" for="pw">Password</label>
        <input id="pw" class="input" type="password" bind:value={form.password} />
      </div>
      <div>
        <label class="label" for="role">Role</label>
        <select id="role" class="input" bind:value={form.role}>
          <option value="user">User</option>
          <option value="admin">Global admin</option>
        </select>
      </div>
      <div class="flex gap-2">
        <button class="btn-ghost flex-1" onclick={() => (showCreate = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={create}>Create</button>
      </div>
    </div>
  </div>
{/if}
