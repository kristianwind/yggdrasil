<script>
  // Global command palette (⌘K / Ctrl+K): fuzzy-jump to any page, server or quick
  // action. Self-contained — listens for the shortcut on the window, and for a
  // "ygg:cmdk" custom event so a visible search affordance can open it too.
  import { tick, onMount, onDestroy } from "svelte";
  import { navigate } from "../lib/router.js";
  import { user, logout } from "../lib/auth.js";
  import { toggleTheme } from "../lib/theme.js";
  import { api } from "../lib/api.js";

  let open = $state(false);
  let query = $state("");
  let sel = $state(0);
  let servers = $state([]);
  let loaded = $state(false);
  let inputEl = $state(null);

  const pages = [
    { label: "Dashboard", path: "/", icon: "📊" },
    { label: "Servers", path: "/servers", icon: "🖥️" },
    { label: "Runes", path: "/runes", icon: "ᚱ" },
    { label: "Schedules", path: "/schedules", icon: "⏰" },
    { label: "Domains", path: "/domains", icon: "🌐" },
    { label: "Bans", path: "/bans", icon: "🚫", admin: true },
    { label: "Users", path: "/users", icon: "👥", admin: true },
    { label: "Audit log", path: "/audit", icon: "📜", admin: true },
    { label: "Settings", path: "/settings", icon: "⚙️", admin: true },
  ];

  async function ensureServers() {
    if (loaded) return;
    loaded = true;
    try {
      servers = await api.get("/servers");
    } catch {
      servers = [];
    }
  }

  async function openPalette() {
    open = true;
    query = "";
    sel = 0;
    ensureServers();
    await tick();
    inputEl?.focus();
  }
  function close() {
    open = false;
  }

  const items = $derived.by(() => {
    const isAdmin = $user?.role === "admin";
    const out = [];
    for (const p of pages) {
      if (p.admin && !isAdmin) continue;
      out.push({ icon: p.icon, label: p.label, hint: "Page", run: () => navigate(p.path) });
    }
    for (const s of servers) {
      out.push({ icon: "🖥️", label: s.name, hint: s.status, run: () => navigate(`/servers/${s.id}`) });
    }
    if ($user?.can_create) {
      out.push({ icon: "➕", label: "New server", hint: "Action", run: () => navigate("/servers?new=1") });
    }
    out.push({ icon: "🌓", label: "Toggle theme", hint: "Action", run: () => toggleTheme() });
    out.push({ icon: "🚪", label: "Sign out", hint: "Action", run: () => logout() });
    return out;
  });

  // Subsequence fuzzy match so "aul" finds "Audit log".
  function fuzzy(hay, q) {
    hay = hay.toLowerCase();
    let i = 0;
    for (const ch of q) {
      i = hay.indexOf(ch, i);
      if (i === -1) return false;
      i++;
    }
    return true;
  }
  const filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter((it) => fuzzy(it.label, q) || (it.hint && it.hint.toLowerCase().includes(q)));
  });

  $effect(() => {
    if (sel >= filtered.length) sel = Math.max(0, filtered.length - 1);
  });

  function choose(it) {
    close();
    it.run();
  }

  function onKey(e) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      open ? close() : openPalette();
      return;
    }
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      close();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      sel = Math.min(sel + 1, filtered.length - 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      sel = Math.max(sel - 1, 0);
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (filtered[sel]) choose(filtered[sel]);
    }
  }

  onMount(() => window.addEventListener("ygg:cmdk", openPalette));
  onDestroy(() => window.removeEventListener("ygg:cmdk", openPalette));
</script>

<svelte:window onkeydown={onKey} />

{#if open}
  <div
    class="fixed inset-0 z-[100] bg-black/50 flex items-start justify-center pt-[14vh] px-4"
    onclick={close}
    role="presentation"
  >
    <div class="card w-full max-w-xl overflow-hidden shadow-2xl" onclick={(e) => e.stopPropagation()} role="presentation">
      <input
        bind:this={inputEl}
        bind:value={query}
        class="w-full px-4 py-3 bg-transparent outline-none border-b border-border text-sm"
        placeholder="Jump to a page, server or action…"
        aria-label="Command palette search"
      />
      <div class="max-h-80 overflow-y-auto py-1">
        {#if filtered.length === 0}
          <div class="px-4 py-6 text-center text-muted text-sm">No matches</div>
        {/if}
        {#each filtered as it, i}
          <button
            class="w-full text-left px-4 py-2 flex items-center gap-3 text-sm {i === sel
              ? 'bg-panel2 text-text'
              : 'text-muted hover:bg-panel2/60'}"
            onmouseenter={() => (sel = i)}
            onclick={() => choose(it)}
          >
            <span class="w-5 text-center">{it.icon}</span>
            <span class="truncate flex-1">{it.label}</span>
            <span class="text-[10px] uppercase tracking-wide opacity-60 shrink-0">{it.hint}</span>
          </button>
        {/each}
      </div>
      <div class="px-4 py-2 border-t border-border text-[11px] text-muted flex gap-4">
        <span>↑↓ navigate</span><span>↵ open</span><span>esc close</span>
      </div>
    </div>
  </div>
{/if}
