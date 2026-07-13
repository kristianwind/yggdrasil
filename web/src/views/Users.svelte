<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import PermissionEditor from "../components/PermissionEditor.svelte";
  import PasswordField from "../components/PasswordField.svelte";

  let users = $state([]);
  let showCreate = $state(false);
  let form = $state({ username: "", password: "", role: "user" });
  let permUser = $state(null); // user whose permissions are being edited
  let editUser = $state(null); // user being edited (role / password)
  let editForm = $state({ role: "user", password: "" });

  function openEdit(u) {
    editUser = u;
    editForm = { role: u.role, password: "" };
  }
  async function saveEdit() {
    try {
      const payload = { role: editForm.role };
      if (editForm.password) payload.password = editForm.password; // blank = keep current
      await api.put(`/users/${editUser.id}`, payload);
      toast("User updated", "success");
      editUser = null;
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

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
      <button class="btn-ghost" onclick={() => openEdit(u)} title="Change this user's username, role or password.">Edit</button>
      {#if u.role !== "admin"}
        <button class="btn-ghost" onclick={() => (permUser = u)}
          title="Grant this user access to specific servers, realms or games, and choose what they can do (view, control, console, files, backups, schedules).">Permissions</button>
      {/if}
      <button class="btn-ghost" onclick={() => toggleDisabled(u)}
        title={u.disabled ? "Re-enable sign-in for this user." : "Block this user from signing in without deleting the account. Ends their active sessions."}>
        {u.disabled ? "Enable" : "Disable"}
      </button>
      <button class="btn-danger" onclick={() => del(u)} title="Permanently delete this user account.">Delete</button>
    </div>
  {/each}
</div>

{#if permUser}
  <PermissionEditor user={permUser} onclose={() => (permUser = null)} />
{/if}

<p class="text-muted text-xs mt-4">
  Global admins have full access. For other users, click <b>Permissions</b> to grant scoped
  access — per realm, per game type, or per server.
</p>

{#if editUser}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-sm p-5 space-y-4">
      <h2 class="text-lg font-semibold">Edit {editUser.username}</h2>
      <div>
        <label class="label" for="e-role">Role</label>
        <select id="e-role" class="input" bind:value={editForm.role}>
          <option value="user">User</option>
          <option value="admin">Global admin</option>
        </select>
      </div>
      <div>
        <label class="label" for="e-pw">New password</label>
        <PasswordField id="e-pw" bind:value={editForm.password} autocomplete="new-password" />
        <p class="text-xs text-muted mt-1">
          Leave blank to keep the current password. Saving signs the user out of existing sessions.
        </p>
      </div>
      <div class="flex gap-2">
        <button class="btn-ghost flex-1" onclick={() => (editUser = null)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={saveEdit}>Save</button>
      </div>
    </div>
  </div>
{/if}

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
        <PasswordField id="pw" bind:value={form.password} autocomplete="new-password" />
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
