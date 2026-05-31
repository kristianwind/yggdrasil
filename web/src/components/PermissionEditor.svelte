<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let { user, onclose } = $props();

  let catalog = $state({ permissions: [], scope_types: [] });
  let realms = $state([]);
  let gameskills = $state([]);
  let servers = $state([]);
  let grants = $state([]);
  let saving = $state(false);

  const permLabels = {
    "server.view": "View status",
    "server.control": "Start / stop / restart",
    "server.console": "Console & RCON",
    "server.files": "Edit files",
    "server.create": "Create servers",
    "server.delete": "Delete servers",
    "server.backup": "Backups",
    "server.schedule": "Schedules",
  };

  onMount(async () => {
    try {
      [catalog, realms, gameskills, servers, grants] = await Promise.all([
        api.get("/permissions/catalog"),
        api.get("/realms"),
        api.get("/gameskills"),
        api.get("/servers"),
        api.get(`/users/${user.id}/permissions`),
      ]);
    } catch (e) {
      toast(e.message, "error");
    }
  });

  function addGrant() {
    grants = [...grants, { scope_type: "global", scope_id: "", perms: [] }];
  }
  function removeGrant(i) {
    grants = grants.filter((_, idx) => idx !== i);
  }
  function togglePerm(grant, perm) {
    if (grant.perms.includes(perm)) grant.perms = grant.perms.filter((p) => p !== perm);
    else grant.perms = [...grant.perms, perm];
    grants = grants;
  }

  function scopeOptions(type) {
    if (type === "realm") return realms.map((r) => ({ id: r.id, label: r.name }));
    if (type === "gameskill") return gameskills.map((g) => ({ id: g.id, label: g.name }));
    if (type === "server") return servers.map((s) => ({ id: s.id, label: s.name }));
    return [];
  }

  async function save() {
    saving = true;
    try {
      // Drop empty grants; ensure non-global has a scope_id.
      const clean = grants.filter((g) => g.perms.length > 0);
      for (const g of clean) {
        if (g.scope_type !== "global" && !g.scope_id) {
          toast("Each non-global grant needs a target", "warn");
          saving = false;
          return;
        }
      }
      await api.put(`/users/${user.id}/permissions`, clean);
      toast("Permissions saved", "success");
      onclose?.();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      saving = false;
    }
  }
</script>

<div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
  <div class="card w-full max-w-2xl max-h-[90vh] overflow-auto p-5 space-y-4">
    <div class="flex items-center justify-between">
      <h2 class="text-lg font-semibold">Permissions — {user.username}</h2>
      <button class="btn-ghost px-2 py-1" onclick={() => onclose?.()}>✕</button>
    </div>

    {#if user.role === "admin"}
      <div class="card bg-accent2/10 border-accent2/40 text-accent text-sm px-4 py-2">
        This user is a global admin and already has full access. Grants below are ignored.
      </div>
    {/if}

    <p class="text-muted text-sm">
      Grant sets of permissions at a scope: globally, or limited to a realm, a game type, or a
      single server. A user can have different permissions per scope.
    </p>

    {#each grants as grant, i}
      <div class="card p-3 space-y-3">
        <div class="flex gap-2 items-end">
          <div class="flex-1">
            <label class="label" for={`scope-${i}`}>Scope</label>
            <select
              id={`scope-${i}`}
              class="input"
              bind:value={grant.scope_type}
              onchange={() => {
                grant.scope_id = "";
                grants = grants;
              }}
            >
              {#each catalog.scope_types as st}
                <option value={st}>{st}</option>
              {/each}
            </select>
          </div>
          {#if grant.scope_type !== "global"}
            <div class="flex-1">
              <label class="label" for={`scopeid-${i}`}>Target</label>
              <select id={`scopeid-${i}`} class="input" bind:value={grant.scope_id}>
                <option value="">— choose —</option>
                {#each scopeOptions(grant.scope_type) as opt}
                  <option value={opt.id}>{opt.label}</option>
                {/each}
              </select>
            </div>
          {/if}
          <button class="btn-danger" onclick={() => removeGrant(i)}>Remove</button>
        </div>

        <div class="grid grid-cols-2 sm:grid-cols-4 gap-2">
          {#each catalog.permissions as perm}
            <label class="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                class="accent-accent2 w-4 h-4"
                checked={grant.perms.includes(perm)}
                onchange={() => togglePerm(grant, perm)}
              />
              <span>{permLabels[perm] || perm}</span>
            </label>
          {/each}
        </div>
      </div>
    {/each}

    <button class="btn-ghost w-full" onclick={addGrant}>+ Add grant</button>

    <div class="flex gap-2 pt-2">
      <button class="btn-ghost flex-1" onclick={() => onclose?.()}>Cancel</button>
      <button class="btn-primary flex-1" onclick={save} disabled={saving}>
        {saving ? "Saving…" : "Save permissions"}
      </button>
    </div>
  </div>
</div>
