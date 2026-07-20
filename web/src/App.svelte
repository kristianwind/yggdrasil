<script>
  import { onMount } from "svelte";
  import { route, navigate } from "./lib/router.js";
  import { user, loadUser, logout } from "./lib/auth.js";
  import { getTheme, toggleTheme } from "./lib/theme.js";
  import { api } from "./lib/api.js";
  import Toasts from "./components/Toasts.svelte";
  import Login from "./views/Login.svelte";
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
  let menuGuardUntil = 0; // ignore ☰ taps until this time (ms) — see below
  let wasAuthed = false;
  let theme = $state(getTheme());
  let runeUpdateCount = $state(0); // installed runes the catalog has moved past (admin only)

  onMount(async () => {
    await loadUser();
    if (!location.hash) navigate("/");
    ready = true;
    api.get("/version").then((v) => (build = v)).catch(() => {});
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

  // Redirect to login when unauthenticated (except on the login route).
  $effect(() => {
    if (ready && !$user && $route.path !== "/login") navigate("/login");
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

{#if !ready}
  <div class="h-screen grid place-items-center text-muted">Loading…</div>
{:else if !$user}
  <Login />
{:else}
  <!-- Desktop: lock to the viewport so only <main> scrolls (no stray page scroll).
       Mobile keeps min-h-screen to avoid the iOS 100vh/URL-bar cutoff. -->
  <div class="min-h-screen md:h-screen md:overflow-hidden flex">
    <!-- Sidebar -->
    <aside
      class="fixed md:sticky md:top-0 z-40 inset-y-0 md:inset-y-auto left-0 w-52 md:h-screen bg-panel border-r border-border
             flex flex-col transition-transform {mobileOpen ? '' : '-translate-x-full md:translate-x-0'}"
      style="padding-top: env(safe-area-inset-top); padding-bottom: env(safe-area-inset-bottom);"
    >
      <button
        class="shrink-0 px-5 py-4 text-lg font-semibold flex items-center gap-2 hover:bg-panel2/50 text-left"
        title="Go to dashboard"
        onclick={() => {
          navigate("/");
          mobileOpen = false;
        }}
      >
        <span>🌳</span> Yggdrasil Panel
      </button>
      <nav class="flex-1 min-h-0 overflow-y-auto px-2 space-y-1">
        {#each nav as item}
          {#if !item.admin || $user.role === "admin"}
            <button
              class="w-full text-left px-3 py-2 rounded-md text-sm flex items-center gap-3
                     {$route.path === item.path || ($route.parts[0] === item.path.slice(1) && item.path !== '/')
                       ? 'bg-panel2 text-text'
                       : 'text-muted hover:bg-panel2/60'}"
              onclick={() => {
                navigate(item.path);
                mobileOpen = false;
              }}
            >
              <span class="w-5 text-center">{item.icon}</span>{item.label}
              {#if item.path === "/runes" && runeUpdateCount > 0}
                <span
                  class="ml-auto badge bg-warn/20 text-warn min-w-5 text-center"
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
            class="flex items-center gap-1 px-2 pb-1 text-xs text-muted hover:text-text"
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
              class="flex items-center gap-1 mx-2 mb-1 px-2 py-1 rounded bg-warn/15 text-warn text-xs hover:bg-warn/25"
              title="A newer release is available"
            >
              ↑ Update available: {build.latest}
            </a>
          {/if}
        {/if}
        <div class="px-2 py-1 text-muted truncate">{$user.username} · {$user.role}</div>
        <div class="flex gap-1 mt-1">
          <button class="btn-ghost flex-1" onclick={logout}>Sign out</button>
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
        <span class="font-semibold">🌳 Yggdrasil Panel</span>
      </header>
      <main class="flex-1 p-4 md:p-6 overflow-auto">
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
