<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let { serverId } = $props();

  let path = $state("");
  let entries = $state([]);
  let editing = $state(null); // { path, content }
  let saving = $state(false);

  async function list() {
    try {
      entries = await api.get(`/servers/${serverId}/files?path=${encodeURIComponent(path)}`);
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(list);

  function open(entry) {
    if (entry.is_dir) {
      path = entry.path;
      editing = null;
      list();
    } else {
      edit(entry);
    }
  }

  async function edit(entry) {
    try {
      const res = await api.get(`/servers/${serverId}/files/content?path=${encodeURIComponent(entry.path)}`);
      editing = { path: entry.path, content: res.content };
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function save() {
    saving = true;
    try {
      await api.put(`/servers/${serverId}/files/content`, { path: editing.path, content: editing.content });
      toast("Saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      saving = false;
    }
  }

  function up() {
    const parts = path.split("/").filter(Boolean);
    parts.pop();
    path = parts.join("/");
    editing = null;
    list();
  }

  async function upload(e) {
    const file = e.target.files?.[0];
    if (!file) return;
    const fd = new FormData();
    fd.append("path", path);
    fd.append("file", file);
    try {
      await api.post(`/servers/${serverId}/files/upload`, fd);
      toast("Uploaded", "success");
      list();
    } catch (err) {
      toast(err.message, "error");
    }
    e.target.value = "";
  }
</script>

{#if editing}
  <div class="flex items-center justify-between mb-2">
    <div class="font-mono text-sm text-muted">{editing.path}</div>
    <div class="flex gap-2">
      <button class="btn-ghost" onclick={() => (editing = null)}>Close</button>
      <button class="btn-primary" onclick={save} disabled={saving}>{saving ? "Saving…" : "Save"}</button>
    </div>
  </div>
  <textarea class="input font-mono h-[55vh] resize-none" bind:value={editing.content} spellcheck="false"
  ></textarea>
{:else}
  <div class="flex items-center gap-2 mb-3">
    <button class="btn-ghost px-2 py-1" onclick={up} disabled={!path}>↑</button>
    <span class="font-mono text-sm text-muted">/{path}</span>
    <label class="btn-ghost ml-auto cursor-pointer">
      Upload
      <input type="file" class="hidden" onchange={upload} />
    </label>
  </div>
  <div class="card divide-y divide-border">
    {#if entries.length === 0}
      <div class="p-4 text-muted text-sm">Empty directory.</div>
    {/if}
    {#each entries as entry}
      <button class="w-full text-left px-4 py-2 hover:bg-panel2/50 flex items-center justify-between" onclick={() => open(entry)}>
        <span class="flex items-center gap-2">
          <span>{entry.is_dir ? "📁" : "📄"}</span>{entry.name}
        </span>
        {#if !entry.is_dir}<span class="text-xs text-muted">{entry.size} B</span>{/if}
      </button>
    {/each}
  </div>
{/if}
