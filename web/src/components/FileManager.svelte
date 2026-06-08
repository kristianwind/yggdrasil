<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";

  let { serverId } = $props();

  let path = $state("");
  let entries = $state([]);
  let editing = $state(null); // { path, content }
  let saving = $state(false);

  // Structured editing for key=value config files (.properties — e.g. Minecraft
  // server.properties). mode is "form" or "raw"; fields preserves the original
  // line order (comments/blanks kept as-is) so saving round-trips cleanly.
  let mode = $state("raw");
  let fields = $state([]);
  const canForm = (p) => /\.(properties|env|cfg|conf)$/i.test(p);

  function parseProps(text) {
    return text.split("\n").map((line) => {
      const t = line.trimStart();
      const eq = line.indexOf("=");
      if (t === "" || t.startsWith("#") || t.startsWith("!") || eq === -1) {
        return { type: "other", raw: line };
      }
      return { type: "kv", key: line.slice(0, eq).trim(), value: line.slice(eq + 1) };
    });
  }
  function serializeProps(fs) {
    return fs.map((f) => (f.type === "kv" ? `${f.key}=${f.value}` : f.raw)).join("\n");
  }
  const humanize = (k) => k.replace(/[-_.]/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
  const isBool = (v) => v.trim() === "true" || v.trim() === "false";
  function toForm() {
    fields = parseProps(editing.content);
    mode = "form";
  }
  function toRaw() {
    editing.content = serializeProps(fields);
    mode = "raw";
  }

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
      // Default to the friendly form view for recognised key=value config files.
      if (canForm(entry.path)) {
        fields = parseProps(res.content);
        mode = "form";
      } else {
        mode = "raw";
      }
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function save() {
    saving = true;
    try {
      // In form mode, fold edited fields back into the raw content first.
      const content = mode === "form" ? serializeProps(fields) : editing.content;
      editing.content = content;
      await api.put(`/servers/${serverId}/files/content`, { path: editing.path, content });
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

  let uploading = $state(false);
  let uploadMsg = $state("");
  let dragOver = $state(false);

  // Upload a batch of { file, rel } (rel = subdir relative to the current path, ""
  // for top level). The single-file endpoint is called once per file; subfolders
  // are preserved by passing the joined path.
  async function uploadFiles(items) {
    if (!items.length || uploading) return;
    uploading = true;
    let ok = 0;
    let fail = 0;
    for (const { file, rel } of items) {
      const dest = [path, rel].filter(Boolean).join("/");
      const fd = new FormData();
      fd.append("path", dest);
      fd.append("file", file);
      try {
        await api.post(`/servers/${serverId}/files/upload`, fd);
        ok++;
      } catch {
        fail++;
      }
      uploadMsg = `Uploading… ${ok + fail}/${items.length}`;
    }
    uploading = false;
    uploadMsg = "";
    toast(`Uploaded ${ok} file${ok !== 1 ? "s" : ""}${fail ? `, ${fail} failed` : ""}`, fail ? "warn" : "success");
    list();
  }

  function onPick(e) {
    const items = [...(e.target.files || [])].map((f) => ({ file: f, rel: "" }));
    uploadFiles(items);
    e.target.value = "";
  }

  // Walk a dropped FileSystemEntry (file or directory) recursively, collecting
  // files with their subpath so a whole site folder keeps its structure.
  function walkEntry(entry, prefix) {
    return new Promise((resolve) => {
      if (entry.isFile) {
        entry.file(
          (file) => resolve([{ file, rel: prefix }]),
          () => resolve([]),
        );
      } else if (entry.isDirectory) {
        const reader = entry.createReader();
        const all = [];
        const sub = prefix ? `${prefix}/${entry.name}` : entry.name;
        const readBatch = () =>
          reader.readEntries(async (batch) => {
            if (!batch.length) {
              const nested = await Promise.all(all.map((c) => walkEntry(c, sub)));
              resolve(nested.flat());
              return;
            }
            all.push(...batch);
            readBatch();
          }, () => resolve([]));
        readBatch();
      } else {
        resolve([]);
      }
    });
  }

  async function onDrop(e) {
    e.preventDefault();
    dragOver = false;
    const dt = e.dataTransfer;
    const roots = [...(dt.items || [])]
      .map((it) => (it.webkitGetAsEntry ? it.webkitGetAsEntry() : null))
      .filter(Boolean);
    let items;
    if (roots.length) {
      const lists = await Promise.all(roots.map((r) => walkEntry(r, "")));
      items = lists.flat();
    } else {
      items = [...(dt.files || [])].map((f) => ({ file: f, rel: "" }));
    }
    uploadFiles(items);
  }
</script>

{#if editing}
  <div class="flex items-center justify-between mb-2 gap-2 flex-wrap">
    <div class="font-mono text-sm text-muted">{editing.path}</div>
    <div class="flex gap-2 items-center">
      {#if canForm(editing.path)}
        <div class="inline-flex rounded-md border border-border overflow-hidden mr-1">
          <button
            class="px-2.5 py-1 text-sm {mode === 'form' ? 'bg-panel2 text-fg' : 'text-muted hover:bg-panel2/50'}"
            onclick={toForm}>Form</button
          >
          <button
            class="px-2.5 py-1 text-sm border-l border-border {mode === 'raw' ? 'bg-panel2 text-fg' : 'text-muted hover:bg-panel2/50'}"
            onclick={toRaw}>Raw</button
          >
        </div>
      {/if}
      <button class="btn-ghost" onclick={() => (editing = null)}>Close</button>
      <button class="btn-primary" onclick={save} disabled={saving}>{saving ? "Saving…" : "Save"}</button>
    </div>
  </div>
  {#if mode === "form"}
    <div class="card p-4 h-[55vh] overflow-auto">
      {#each fields as f, i}
        {#if f.type === "kv"}
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 items-center py-1.5 border-b border-border/50">
            <label class="text-sm" for={`fld-${i}`}>
              {humanize(f.key)}
              <span class="block text-[11px] text-muted font-mono">{f.key}</span>
            </label>
            {#if isBool(f.value)}
              <select id={`fld-${i}`} class="input" bind:value={fields[i].value}>
                <option value="true">true</option>
                <option value="false">false</option>
              </select>
            {:else}
              <input id={`fld-${i}`} class="input" bind:value={fields[i].value} spellcheck="false" />
            {/if}
          </div>
        {/if}
      {/each}
      {#if !fields.some((f) => f.type === "kv")}
        <div class="text-muted text-sm">No key=value settings found — use the Raw view.</div>
      {/if}
    </div>
  {:else}
    <textarea class="input font-mono h-[55vh] resize-none" bind:value={editing.content} spellcheck="false"
    ></textarea>
  {/if}
{:else}
  <div class="flex items-center gap-2 mb-3">
    <button class="btn-ghost px-2 py-1" onclick={up} disabled={!path}>↑</button>
    <span class="font-mono text-sm text-muted">/{path}</span>
    {#if uploading}<span class="text-xs text-muted">{uploadMsg}</span>{/if}
    <label class="btn-ghost ml-auto cursor-pointer">
      Upload files
      <input type="file" class="hidden" multiple onchange={onPick} />
    </label>
  </div>
  <!-- Drop zone: drag a folder or several files anywhere over the listing. -->
  <div
    role="region"
    aria-label="File drop zone"
    class="card divide-y divide-border relative transition {dragOver ? 'ring-2 ring-accent2' : ''}"
    ondragover={(e) => { e.preventDefault(); dragOver = true; }}
    ondragleave={(e) => { e.preventDefault(); dragOver = false; }}
    ondrop={onDrop}
  >
    {#if dragOver}
      <div class="absolute inset-0 z-10 grid place-items-center bg-panel/80 pointer-events-none text-sm text-accent2 font-medium">
        Drop to upload to /{path}
      </div>
    {/if}
    {#if entries.length === 0}
      <div class="p-4 text-muted text-sm">Empty directory — drag &amp; drop files or a folder here, or use “Upload files”.</div>
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
