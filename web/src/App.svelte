<script>
  import { onMount } from "svelte";
  import { route, navigate } from "./lib/router.js";
  import { user, loadUser, logout } from "./lib/auth.js";
  import { getTheme, toggleTheme } from "./lib/theme.js";
  import { api } from "./lib/api.js";
  import { toast } from "./lib/toast.js";
  import { DISCORD_INVITE } from "./lib/links.js";
  import Toasts from "./components/Toasts.svelte";
  import CommandPalette from "./components/CommandPalette.svelte";
  import ConfirmDialog from "./components/ConfirmDialog.svelte";
  import Login from "./views/Login.svelte";
  import ResetPassword from "./views/ResetPassword.svelte";
  import Dashboard from "./views/Dashboard.svelte";
  import Servers from "./views/Servers.svelte";
  import ServerDetail from "./views/ServerDetail.svelte";
  import Runes from "./views/Runes.svelte";
  import Users from "./views/Users.svelte";
  import Audit from "./views/Audit.svelte";
  import Settings from "./views/Settings.svelte";
  import Schedules from "./views/Schedules.svelte";
  import Bans from "./views/Bans.svelte";
  import Domains from "./views/Domains.svelte";

  let ready = $state(false);
  let build = $state(null); // { version, repo }
  let mobileOpen = $state(false);
  // Desktop-only: collapse the sidebar to an icon rail (persisted). Never affects
  // the mobile drawer — every collapse style below is md:-scoped, width is md:w-16.
  let collapsed = $state(localStorage.getItem("ygg_sidebar_collapsed") === "1");
  function toggleCollapsed() {
    collapsed = !collapsed;
    localStorage.setItem("ygg_sidebar_collapsed", collapsed ? "1" : "0");
  }
  let menuGuardUntil = 0; // ignore ☰ taps until this time (ms) — see below
  let wasAuthed = false;
  let theme = $state(getTheme());
  let runeUpdateCount = $state(0); // installed runes the catalog has moved past (admin only)

  onMount(async () => {
    await loadUser();
    if (!location.hash) navigate("/");
    ready = true;
    loadBeaconNotice();
    api.get("/version").then((v) => {
      build = v;
      // Reflect a custom panel name in the browser tab too, so several panels
      // open in different tabs are distinguishable at a glance.
      if (v?.panel_name) document.title = v.panel_name;
    }).catch(() => {});
  });

  // Beacon disclosure. The beacon is on by default, so the panel has to say so
  // unprompted rather than leave it for whoever opens Settings — an install that
  // phones home without mentioning it is exactly what a self-hosted audience
  // does not forgive. Raised once, for admins only (nobody else can change it),
  // and dismissing it is a decision either way.
  let beaconNotice = $state(null); // { url, instance_id, version } once known
  async function loadBeaconNotice() {
    if (!$user || $user.role !== "admin") return;
    try {
      const b = await api.get("/settings/beacon");
      beaconNotice = b?.notice_pending ? b : null;
    } catch {
      /* not fatal — the notice reappears on the next load */
    }
  }
  async function ackBeacon(keep) {
    try {
      await api.post("/settings/beacon/notice", { keep });
      beaconNotice = null;
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // Security advisories. Pulled from the project's repo (never pushed — there is
  // no registry of installs), filtered server-side to the ones that apply to the
  // version running here, and dismissed per advisory for the whole panel.
  let advisories = $state([]);
  async function loadAdvisories() {
    if (!$user || $user.role !== "admin") return;
    try {
      const r = await api.get("/advisories");
      advisories = r?.advisories || [];
    } catch {
      /* not fatal — retried on the next load */
    }
  }
  async function ackAdvisory(id) {
    try {
      await api.post(`/advisories/${encodeURIComponent(id)}/ack`, {});
      advisories = advisories.filter((a) => a.id !== id);
    } catch (e) {
      toast(e.message, "error");
    }
  }
  // Keyed on the user, not onMount: onMount runs while the login page is showing,
  // where $user is still null, so a loader called there never runs again for
  // somebody who just signed in. (The beacon notice above has that shape and is
  // saved by the Dashboard fetching the same endpoint for its own card.) Reads
  // only ready/$user, so this fires once when the session appears — and the
  // six-hour server-side cache makes a repeat free anyway.
  $effect(() => {
    if (ready && $user) loadAdvisories();
  });

  // Surface rune updates in the nav so an admin sees them without opening Runes.
  // The endpoint is admin-only and shares a 10-minute GitHub cache, so refetching
  // on navigation is cheap and keeps the badge fresh after an update-and-leave.
  async function refreshRuneUpdates() {
    if (!$user || $user.role !== "admin") {
      runeUpdateCount = 0;
      return;
    }
    try {
      const r = await api.get("/gameskills/updates");
      runeUpdateCount = (r.updates ?? []).length;
    } catch {
      /* transient error — leave the last known count rather than flicker to 0 */
    }
  }
  $effect(() => {
    void $route.path; // re-check on every navigation
    if (ready && $user) refreshRuneUpdates();
  });

  // Redirect to login when unauthenticated (except on the login/reset routes —
  // reset is reachable from an emailed link without an active session).
  $effect(() => {
    if (ready && !$user && $route.path !== "/login" && $route.path !== "/reset") navigate("/login");
    if (ready && $user && $route.path === "/login") navigate("/");
    // On the transition to logged-in, land on the dashboard with the menu closed
    // and briefly ignore the ☰ button: on an iOS home-screen PWA the tap that
    // dismisses the passkey sheet can pass through to the freshly-rendered menu
    // button ("ghost tap"), which otherwise pops the nav open right after login.
    if (ready && $user && !wasAuthed) {
      mobileOpen = false;
      menuGuardUntil = Date.now() + 900;
    }
    wasAuthed = ready && !!$user;
  });

  // Any route change closes the mobile nav drawer.
  $effect(() => {
    void $route.path;
    mobileOpen = false;
  });

  const nav = [
    { path: "/", label: "Dashboard", icon: "📊" },
    { path: "/servers", label: "Servers", icon: "🖥️" },
    { path: "/runes", label: "Runes", icon: "ᚱ" },
    { path: "/schedules", label: "Schedules", icon: "⏰" },
    { path: "/domains", label: "Domains", icon: "🌐" },
    { path: "/bans", label: "Bans", icon: "🚫", admin: true },
    { path: "/users", label: "Users", icon: "👥", admin: true },
    { path: "/audit", label: "Audit log", icon: "📜", admin: true },
    { path: "/settings", label: "Settings", icon: "⚙️", admin: true },
  ];
</script>

<Toasts />
<ConfirmDialog />

{#if !ready}
  <div class="h-screen grid place-items-center text-muted">Loading…</div>
{:else if !$user}
  {#if $route.path === "/reset"}
    <ResetPassword />
  {:else}
    <Login />
  {/if}
{:else}
  <CommandPalette />
  <!-- Desktop: lock to the viewport so only <main> scrolls (no stray page scroll).
       Mobile keeps min-h-screen to avoid the iOS 100vh/URL-bar cutoff. -->
  <div class="min-h-screen md:h-screen md:overflow-hidden flex">
    <!-- Sidebar -->
    <aside
      class="fixed md:sticky md:top-0 z-40 inset-y-0 md:inset-y-auto left-0 w-52 {collapsed ? 'md:w-16' : 'md:w-52'} md:h-screen bg-panel border-r border-border
             flex flex-col transition-[width,transform] duration-200 {mobileOpen ? '' : '-translate-x-full md:translate-x-0'}"
      style="padding-top: env(safe-area-inset-top); padding-bottom: env(safe-area-inset-bottom);"
    >
      <!-- Collapse/expand the rail — a small chip straddling the divider line, the
           spot people expect it. Desktop only; mobile uses the ☰ drawer. -->
      <button
        class="hidden md:flex absolute top-4 -right-3 z-50 w-6 h-6 items-center justify-center rounded-full border border-border bg-panel text-muted hover:text-text hover:bg-panel2 shadow-sm transition-colors"
        onclick={toggleCollapsed}
        title={collapsed ? "Expand menu" : "Collapse menu"}
        aria-label={collapsed ? "Expand menu" : "Collapse menu"}
      >{collapsed ? "›" : "‹"}</button>
      <button
        class="shrink-0 py-4 text-base font-semibold flex items-center gap-2 hover:bg-panel2/50 text-left px-4 {collapsed ? 'md:px-0 md:justify-center' : ''}"
        title="Go to dashboard"
        onclick={() => {
          navigate("/");
          mobileOpen = false;
        }}
      >
        <span class="shrink-0">🌳</span><span class="truncate {collapsed ? 'md:hidden' : ''}">{build?.panel_name || "Yggdrasil Panel"}</span>
      </button>
      <!-- Command palette trigger — keeps ⌘K discoverable and works on touch. -->
      <button
        class="mx-2 mb-1 flex items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm text-muted hover:bg-panel2/60 hover:text-text {collapsed ? 'md:justify-center md:px-0' : ''}"
        title="Search — jump to a page, server or action (⌘K)"
        onclick={() => {
          window.dispatchEvent(new CustomEvent("ygg:cmdk"));
          mobileOpen = false;
        }}
      >
        <span class="w-5 text-center">🔍</span>
        <span class="{collapsed ? 'md:hidden' : ''}">Search</span>
        <kbd class="ml-auto text-[10px] border border-border rounded px-1 opacity-70 {collapsed ? 'md:hidden' : ''}">⌘K</kbd>
      </button>
      <nav class="flex-1 min-h-0 overflow-y-auto px-2 space-y-1">
        {#each nav as item}
          {#if !item.admin || $user.role === "admin"}
            <button
              title={item.label}
              class="w-full text-left px-3 py-2 rounded-md text-sm flex items-center gap-3 {collapsed ? 'md:justify-center md:gap-0' : ''}
                     {$route.path === item.path || ($route.parts[0] === item.path.slice(1) && item.path !== '/')
                       ? 'bg-panel2 text-text'
                       : 'text-muted hover:bg-panel2/60'}"
              onclick={() => {
                navigate(item.path);
                mobileOpen = false;
              }}
            >
              <span class="w-5 text-center relative">
                {item.icon}
                {#if item.path === "/runes" && runeUpdateCount > 0 && collapsed}
                  <!-- Collapsed rail: a dot on the icon stands in for the count badge. -->
                  <span class="hidden md:block absolute -top-1 -right-1 w-2 h-2 rounded-full bg-warn" aria-hidden="true"></span>
                {/if}
              </span>
              <span class="truncate {collapsed ? 'md:hidden' : ''}">{item.label}</span>
              {#if item.path === "/runes" && runeUpdateCount > 0}
                <span
                  class="ml-auto badge bg-warn/20 text-warn min-w-5 text-center {collapsed ? 'md:hidden' : ''}"
                  title="{runeUpdateCount} rune update{runeUpdateCount === 1 ? '' : 's'} available"
                >{runeUpdateCount}</span>
              {/if}
            </button>
          {/if}
        {/each}
      </nav>
      <div class="shrink-0 p-3 border-t border-border text-sm">
        {#if build}
          <a
            href={build.repo}
            target="_blank"
            rel="noopener"
            class="flex items-center gap-1 px-2 pb-1 text-xs text-muted hover:text-text {collapsed ? 'md:hidden' : ''}"
            title="View Yggdrasil on GitHub"
          >
            🌳 Yggdrasil {build.version}
            <span class="opacity-60">↗</span>
          </a>
          {#if build.update_available}
            <a
              href={`${build.repo}/releases/latest`}
              target="_blank"
              rel="noopener"
              class="flex items-center gap-1 mx-2 mb-1 px-2 py-1 rounded bg-warn/15 text-warn text-xs hover:bg-warn/25 {collapsed ? 'md:hidden' : ''}"
              title="A newer release is available"
            >
              ↑ Update available: {build.latest}
            </a>
          {/if}
        {/if}
        <a
          href={DISCORD_INVITE}
          target="_blank"
          rel="noopener"
          class="flex items-center gap-1 px-2 pb-1 text-xs text-muted hover:text-text {collapsed ? 'md:hidden' : ''}"
          title="Join the Yggdrasil community on Discord"
        >
          💬 Discord
          <span class="opacity-60">↗</span>
        </a>
        <div class="px-2 py-1 text-muted truncate {collapsed ? 'md:hidden' : ''}">{$user.username} · {$user.role}</div>
        <div class="flex gap-1 mt-1 {collapsed ? 'md:flex-col' : ''}">
          <button class="btn-ghost flex-1" onclick={logout} title="Sign out">
            <span class="{collapsed ? 'md:hidden' : ''}">Sign out</span>
            {#if collapsed}<span class="hidden md:inline" aria-hidden="true">🚪</span>{/if}
          </button>
          <button
            class="btn-ghost px-3"
            aria-label="Toggle light/dark theme"
            title="Toggle light/dark theme"
            onclick={() => (theme = toggleTheme())}
          >
            {theme === "light" ? "🌙" : "☀️"}
          </button>
        </div>
      </div>
    </aside>

    {#if mobileOpen}
      <button
        class="fixed inset-0 z-30 bg-black/50 md:hidden"
        aria-label="Close menu"
        onclick={() => (mobileOpen = false)}
      ></button>
    {/if}

    <!-- Main -->
    <div class="flex-1 min-w-0 flex flex-col">
      <header
        class="md:hidden sticky top-0 z-30 flex items-center gap-3 px-4 py-3 border-b border-border bg-panel"
        style="padding-top: calc(0.75rem + env(safe-area-inset-top));"
      >
        <button class="btn-ghost px-2 py-1" aria-label="Open menu" onclick={() => { if (Date.now() >= menuGuardUntil) mobileOpen = true; }}>☰</button>
        <span class="font-semibold">🌳 {build?.panel_name || "Yggdrasil Panel"}</span>
      </header>
      <main class="flex-1 p-4 md:p-6 overflow-auto">
        {#each advisories as a (a.id)}
          <!-- Deliberately plain text plus one link. An advisory is never
               something the panel acts on, and it is the most attractive thing
               here to hijack, so nothing in it is rendered as markup. -->
          <div
            class="card p-4 mb-4 border {a.severity === 'critical'
              ? 'border-danger/60 bg-danger/5'
              : a.severity === 'high'
                ? 'border-warn/60 bg-warn/5'
                : 'border-border'}"
            role={a.severity === "critical" ? "alert" : "status"}
          >
            <div class="font-semibold mb-1">
              {a.severity === "critical" ? "🔴" : a.severity === "high" ? "🟠" : "ℹ️"}
              Security advisory{a.severity === "critical" ? " — act now" : ""}: {a.title}
            </div>
            <p class="text-sm text-muted mb-2 whitespace-pre-line">{a.detail}</p>
            {#if a.fixed_in}
              <p class="text-sm mb-2">
                Fixed in <span class="font-mono text-xs">{a.fixed_in}</span> — you are on
                <span class="font-mono text-xs">{build?.version || "?"}</span>.
              </p>
            {/if}
            <div class="flex flex-wrap gap-2">
              <!-- Only offer the update path when there is something to update TO.
                   An advisory published ahead of its patch (fixed_in empty) is a
                   mitigation to read, and sending the reader to Updates for a
                   release that does not exist wastes the one action they have. -->
              {#if a.fixed_in}
                <a class="btn-primary" href="#/settings">Settings → Updates</a>
              {/if}
              {#if a.url}
                <a class="btn-ghost" href={a.url} target="_blank" rel="noopener noreferrer">Read the advisory ↗</a>
              {/if}
              <button class="btn-ghost" onclick={() => ackAdvisory(a.id)}>Dismiss</button>
            </div>
          </div>
        {/each}
        {#if beaconNotice}
          <!-- Shown once. It names the two fields verbatim rather than saying
               "anonymous usage data", because the whole point is that the reader
               can check the claim instead of trusting it. -->
          <div class="card p-4 mb-4 border border-accent2/40">
            <div class="font-semibold mb-1">📡 This panel reports two things to the Yggdrasil project</div>
            <p class="text-sm text-muted mb-2">
              Once a day it sends a random id that identifies this install to nobody
              (<span class="font-mono text-xs">{beaconNotice.instance_id}</span>) and its version
              (<span class="font-mono text-xs">{beaconNotice.version}</span>) to
              <span class="font-mono text-xs">{beaconNotice.url}</span>. That is the entire message —
              no server names, no addresses, no configuration, and the collector stores no IP.
              It exists so the project can see roughly how many people run it.
            </p>
            <div class="flex flex-wrap gap-2">
              <button class="btn-primary" onclick={() => ackBeacon(true)}>Keep it on</button>
              <button class="btn-ghost" onclick={() => ackBeacon(false)}>Turn it off</button>
              <a class="btn-ghost" href="#/settings">Settings → Beacon</a>
            </div>
          </div>
        {/if}
        {#if $route.parts[0] === "servers" && $route.parts[1]}
          <ServerDetail id={$route.parts[1]} />
        {:else if $route.parts[0] === "servers"}
          <Servers />
        {:else if $route.parts[0] === "runes"}
          <Runes />
        {:else if $route.parts[0] === "schedules"}
          <Schedules />
        {:else if $route.parts[0] === "domains"}
          <Domains />
        {:else if $route.parts[0] === "bans"}
          <Bans />
        {:else if $route.parts[0] === "users"}
          <Users />
        {:else if $route.parts[0] === "audit"}
          <Audit />
        {:else if $route.parts[0] === "settings"}
          <Settings />
        {:else}
          <Dashboard />
        {/if}
      </main>
    </div>
  </div>
{/if}
