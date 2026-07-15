<script>
  // Realms are server groups, and the scope a delegate's permissions can be
  // granted against. Until now they could only be created through the API, even
  // though the whole scoped-grant model depends on having one — so an admin who
  // wanted to give someone "every Minecraft server" had to reach for curl.
  //
  // They also appear on their own: creating a server files it under a realm named
  // after the rune's category, creating that realm if it's missing. So this list
  // is usually somewhere to tidy up rather than somewhere to start.
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let realms = $state([]);
  let loading = $state(true);
  let busy = $state(false);

  let showForm = $state(false);
  let editing = $state(null); // realm being renamed, or null when creating
  let form = $state({ name: "", description: "" });

  export async function load() {
    loading = true;
    try {
      realms = await api.get("/realms");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      loading = false;
    }
  }
  load();

  function openCreate() {
    editing = null;
    form = { name: "", description: "" };
    showForm = true;
  }

  function openEdit(r) {
    editing = r;
    form = { name: r.name, description: r.description || "" };
    showForm = true;
  }

  async function save() {
    if (!form.name.trim()) return toast("Name required", "warn");
    busy = true;
    try {
      if (editing) {
        await api.put(`/realms/${editing.id}`, { name: form.name.trim(), description: form.description });
        toast(`Renamed to ${form.name.trim()}`, "success");
      } else {
        await api.post("/realms", { name: form.name.trim(), description: form.description });
        toast(`Realm ${form.name.trim()} created`, "success");
      }
      showForm = false;
      await load();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      busy = false;
    }
  }

  async function remove(r) {
    const n = r.server_count ?? 0;
    const warning =
      n > 0
        ? `Delete realm "${r.name}"?\n\n${n} server${n === 1 ? "" : "s"} will be moved out of it. The servers themselves are not deleted.\n\nAny permission granted against this realm stops applying.`
        : `Delete realm "${r.name}"?`;
    if (!confirm(warning)) return;
    busy = true;
    try {
      await api.del(`/realms/${r.id}`);
      toast(`Realm ${r.name} deleted`, "success");
      await load();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      busy = false;
    }
  }
</script>

<h2 class="text-xl font-semibold mb-2">Realms</h2>
<div class="card p-4 mb-10">
  <p class="text-sm text-muted mb-3">
    A realm is a group of servers, and the scope you can grant a delegate against — "every
    server in <em>Minecraft</em>" rather than one at a time. New servers are filed under a
    realm named after their rune's category, so most of these appear on their own.
  </p>

  {#if loading}
    <p class="text-sm text-muted">Loading…</p>
  {:else if !realms.length}
    <p class="text-sm text-muted">No realms yet. One appears the first time you create a server.</p>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead>
          <tr class="text-muted text-left">
            <th class="py-1 pr-3 font-medium">Name</th>
            <th class="py-1 pr-3 font-medium">Description</th>
            <th class="py-1 pr-3 font-medium">Servers</th>
            <th class="py-1 font-medium text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {#each realms as r (r.id)}
            <tr class="border-t border-border">
              <td class="py-2 pr-3 font-medium">{r.name}</td>
              <td class="py-2 pr-3 text-muted">{r.description || "—"}</td>
              <td class="py-2 pr-3">
                {#if r.server_count}
                  <span class="badge bg-border text-muted">{r.server_count}</span>
                {:else}
                  <span class="text-muted">empty</span>
                {/if}
              </td>
              <td class="py-2 text-right whitespace-nowrap">
                <button class="btn-ghost text-xs" disabled={busy} onclick={() => openEdit(r)}
                  title="Rename this realm or change its description.">Rename</button>
                <button class="btn-ghost text-xs text-danger" disabled={busy} onclick={() => remove(r)}
                  title="Delete this realm. Its servers are moved out of it, not deleted.">Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  <div class="mt-3">
    <button class="btn-ghost" disabled={busy} onclick={openCreate}
      title="Create an empty realm — useful for grouping servers before they exist, or for scoping a delegate's permissions.">
      + New realm
    </button>
  </div>
</div>

{#if showForm}
  <div class="fixed inset-0 bg-black/60 grid place-items-center z-40 p-4" role="presentation"
    onclick={(e) => { if (e.target === e.currentTarget) showForm = false; }}>
    <div class="card p-5 w-full max-w-md space-y-3">
      <h2 class="text-lg font-semibold">{editing ? `Rename ${editing.name}` : "New realm"}</h2>
      <div>
        <label class="label" for="realm-name">Name</label>
        <input id="realm-name" class="input" bind:value={form.name} placeholder="Minecraft" />
      </div>
      <div>
        <label class="label" for="realm-desc">Description (optional)</label>
        <input id="realm-desc" class="input" bind:value={form.description} placeholder="The survival servers" />
      </div>
      {#if editing && editing.server_count}
        <p class="text-xs text-muted">
          {editing.server_count} server{editing.server_count === 1 ? "" : "s"} stay in this realm; only the
          label changes.
        </p>
      {/if}
      <div class="flex gap-2 justify-end pt-1">
        <button class="btn-ghost" onclick={() => (showForm = false)}>Cancel</button>
        <button class="btn-primary" disabled={busy} onclick={save}>
          {busy ? "Saving…" : editing ? "Rename" : "Create"}
        </button>
      </div>
    </div>
  </div>
{/if}
