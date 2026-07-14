<script>
  import { onMount, onDestroy } from "svelte";
  import { api, wsURL } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { navigate } from "../lib/router.js";
  import { user } from "../lib/auth.js";
  import FileManager from "../components/FileManager.svelte";
  import Sparkline from "../components/Sparkline.svelte";
  import VarForm from "../components/VarForm.svelte";

  let { id } = $props();

  let server = $state(null);
  let tab = $state("console");
  let stats = $state(null);
  let status = $state(null); // player-count query result
  let statsTimer;

  // Console
  let lines = $state([]);
  let cmd = $state("");
  let ws = $state(null);
  let termEl = $state(null); // bind:this — $state so Svelte 5 tracks the assignment

  // Install
  let installLines = $state([]);
  let installWs = $state(null);
  let installEl = $state(null); // bind:this — see termEl

  // Backups
  let backups = $state([]);
  let backupTargets = $state([]);
  let selectedTarget = $state("");
  let backupBusy = $state(false);

  // Wipe (reset world/persistence — rune-defined)
  let showWipe = $state(false);
  let wipeBackupFirst = $state(true);
  let wiping = $state(false);
  // Watchdog (auto-heal): flip the per-server toggle; state lives on server.watchdog.
  let watchdogBusy = $state(false);
  async function toggleWatchdog() {
    watchdogBusy = true;
    try {
      const next = !server.watchdog;
      await api.put(`/servers/${id}/watchdog`, { enabled: next });
      server.watchdog = next;
      toast(next ? "Watchdog on — auto-restart if it stops responding" : "Watchdog off", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      watchdogBusy = false;
    }
  }

  // Players tab (live roster + kick / broadcast / lock over RCON)
  let playersData = $state({ supported: true, online: false, players: [], can_kick: false, can_broadcast: false, can_lock: false });
  let playersBusy = $state(false);
  let broadcastMsg = $state("");
  let serverLocked = $state(false);
  let playersTimer = null;

  async function loadPlayers() {
    try {
      playersData = await api.get(`/servers/${id}/players`);
    } catch (e) {
      // leave prior data; a transient RCON hiccup shouldn't blank the tab
    }
  }

  async function kickPlayer(p) {
    const reason = prompt(`Kick ${p.name}? Optional reason:`, "");
    if (reason === null) return; // cancelled
    playersBusy = true;
    try {
      await api.post(`/servers/${id}/players/kick`, { id: p.id, name: p.name, reason });
      toast(`Kicked ${p.name}`, "success");
      setTimeout(loadPlayers, 800);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      playersBusy = false;
    }
  }

  async function sendBroadcast() {
    if (!broadcastMsg.trim()) return;
    playersBusy = true;
    try {
      await api.post(`/servers/${id}/players/broadcast`, { message: broadcastMsg });
      toast("Broadcast sent", "success");
      broadcastMsg = "";
    } catch (e) {
      toast(e.message, "error");
    } finally {
      playersBusy = false;
    }
  }

  async function toggleLock() {
    playersBusy = true;
    try {
      const next = !serverLocked;
      await api.post(`/servers/${id}/players/lock`, { locked: next });
      serverLocked = next;
      toast(next ? "Server locked — no new joins" : "Server unlocked", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      playersBusy = false;
    }
  }

  // Poll the roster while the Players tab is open; stop when it closes.
  $effect(() => {
    if (tab === "players" && server?.players_supported) {
      loadPlayers();
      playersTimer = setInterval(loadPlayers, 10000);
      return () => { clearInterval(playersTimer); playersTimer = null; };
    }
  });

  // Resource history charts (sampled server-side every ~5 min)
  let metrics = $state([]);
  let metricsHours = $state(24);
  let showHistory = $state(false);
  async function loadMetrics() {
    try {
      metrics = await api.get(`/servers/${id}/metrics?hours=${metricsHours}`);
    } catch {
      metrics = [];
    }
  }
  const cpuSeries = $derived(metrics.map((m) => m.cpu));
  const memSeries = $derived(metrics.map((m) => m.mem_mb));
  const playerSeries = $derived(metrics.map((m) => (m.players >= 0 ? m.players : 0)));
  $effect(() => {
    if (showHistory) { metricsHours; loadMetrics(); }
  });

  // Activity feed (parsed admin log — session history)
  let activity = $state({ supported: true, file: "", events: [] });
  let activityBusy = $state(false);
  const activityIcons = { join: "🟢", leave: "⚪", death: "💀", kill: "🔫" };
  // Activity view: raw feed vs. a per-player leaderboard tallied from the events.
  let activityView = $state("feed");
  const leaderboard = $derived.by(() => {
    const by = {};
    for (const ev of activity.events || []) {
      if (!ev.player) continue;
      const p = (by[ev.player] ??= { name: ev.player, kill: 0, death: 0, join: 0 });
      if (ev.type in p) p[ev.type]++;
    }
    return Object.values(by)
      .map((p) => ({ ...p, kd: p.death ? p.kill / p.death : p.kill }))
      .sort((a, b) => b.kill - a.kill || b.kd - a.kd || b.death - a.death);
  });
  async function loadActivity() {
    activityBusy = true;
    try {
      activity = await api.get(`/servers/${id}/admin-log`);
    } catch (e) {
      // leave prior data
    } finally {
      activityBusy = false;
    }
  }
  $effect(() => {
    if (tab === "activity" && server?.admin_log_supported) loadActivity();
  });

  // AI config advisor (advisory — review this server's settings for footguns)
  let configAdvice = $state("");
  let configAdviceBusy = $state(false);
  async function reviewConfig() {
    configAdviceBusy = true;
    try {
      const r = await api.post(`/servers/${id}/config-advice`);
      configAdvice = r.advice || "(no advice returned)";
    } catch (e) {
      toast(e.message, "error");
    } finally {
      configAdviceBusy = false;
    }
  }

  // AI error explainer (advisory — send the visible log to the admin's own LLM)
  let explain = $state("");
  let explainBusy = $state(false);
  async function explainError(context, text) {
    // Send whatever's visible; if it's empty (e.g. a crashed/stopped server with
    // no console output), the server falls back to the container's recent logs.
    explainBusy = true;
    try {
      const r = await api.post(`/servers/${id}/explain`, { log: (text || "").trim(), context });
      explain = r.explanation || "(no explanation returned)";
    } catch (e) {
      toast(e.message, "error");
    } finally {
      explainBusy = false;
    }
  }

  // AI digest ("what happened while away" — advisory, uses the admin's own LLM)
  let digest = $state("");
  let digestBusy = $state(false);
  async function loadDigest() {
    digestBusy = true;
    try {
      const r = await api.post(`/servers/${id}/admin-log/digest`);
      digest = r.summary || "(no summary returned)";
    } catch (e) {
      toast(e.message, "error");
    } finally {
      digestBusy = false;
    }
  }

  // Safe restart (broadcast warnings, optional backup, then restart)
  let showSafe = $state(false);
  let safeBackupFirst = $state(false);
  let safeBusy = $state(false);

  // Auto-restart toggle (a managed schedule under the hood — restart every N hours)
  let showAuto = $state(false);
  let autoBusy = $state(false);
  let autoRestart = $state({ enabled: false, every_hours: 6, warn: true, backup_first: false, target_id: "" });
  const autoHourOptions = [1, 2, 3, 4, 6, 8, 12, 24];

  // Quiet-hours suggestion (mined from sampled player counts) — hints the calmest
  // time of day to schedule restarts.
  let quietHours = $state(null);
  async function loadQuietHours() {
    try {
      quietHours = await api.get(`/servers/${id}/quiet-hours`);
    } catch {
      quietHours = null;
    }
  }
  const hh = (h) => String(h).padStart(2, "0") + ":00";

  async function loadAutoRestart() {
    try {
      autoRestart = await api.get(`/servers/${id}/auto-restart`);
    } catch {
      // non-fatal — leave defaults
    }
  }

  async function saveAutoRestart(enabled) {
    if (enabled && autoRestart.backup_first && !autoRestart.target_id)
      return toast("Pick a backup target, or turn off backup-first", "warn");
    autoBusy = true;
    try {
      autoRestart = await api.put(`/servers/${id}/auto-restart`, { ...autoRestart, enabled });
      toast(enabled ? `Auto-restart on — every ${autoRestart.every_hours}h` : "Auto-restart off", "success");
      showAuto = false;
    } catch (e) {
      toast(e.message, "error");
    } finally {
      autoBusy = false;
    }
  }
  async function doSafeRestart() {
    if (safeBackupFirst && !selectedTarget) return toast("Pick a backup target, or turn off backup-first", "warn");
    safeBusy = true;
    try {
      await api.post(`/servers/${id}/safe-restart`, {
        backup_first: safeBackupFirst,
        target_id: safeBackupFirst ? selectedTarget : "",
      });
      toast(server.restart_warn ? "Players warned — restarting after the countdown" : "Restart scheduled", "success");
      showSafe = false;
    } catch (e) {
      toast(e.message, "error");
    } finally {
      safeBusy = false;
    }
  }

  async function doWipe() {
    if (wipeBackupFirst && !selectedTarget) return toast("Pick a backup target, or turn off backup-first", "warn");
    wiping = true;
    try {
      await api.post(`/servers/${id}/wipe`, {
        backup_first: wipeBackupFirst,
        target_id: wipeBackupFirst ? selectedTarget : "",
      });
      toast("Server wiped", "success");
      showWipe = false;
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      wiping = false;
    }
  }

  async function loadBackups() {
    try {
      [backups, backupTargets] = await Promise.all([
        api.get(`/servers/${id}/backups`),
        api.get("/backup/targets").catch(() => []),
      ]);
      if (!selectedTarget && backupTargets[0]) selectedTarget = backupTargets[0].id;
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function runBackup() {
    if (!selectedTarget) return toast("Create a backup target in Settings first", "warn");
    backupBusy = true;
    try {
      await api.post(`/servers/${id}/backup`, { target_id: selectedTarget });
      toast("Backup started", "info");
      setTimeout(loadBackups, 1500);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      backupBusy = false;
    }
  }

  async function restoreBackup(b) {
    if (!confirm("Restore this backup? The server will be stopped and files overwritten.")) return;
    try {
      await api.post(`/backups/${b.id}/restore`);
      toast("Restored", "success");
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  let verifyingId = $state("");
  async function verifyBackup(b) {
    verifyingId = b.id;
    try {
      const r = await api.post(`/backups/${b.id}/verify`);
      if (r.ok) toast(`✓ Backup is valid — ${r.files} files, ${fmtSize(r.bytes)} uncompressed`, "success");
      else toast(`✗ Backup looks corrupt: ${r.error}`, "error");
      await loadBackups(); // refresh the verified badge
    } catch (e) {
      toast(e.message, "error");
    } finally {
      verifyingId = "";
    }
  }

  async function deleteBackup(b) {
    if (!confirm("Delete this backup?")) return;
    try {
      await api.del(`/backups/${b.id}`);
      await loadBackups();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function fmtSize(n) {
    if (!n) return "—";
    const u = ["B", "KB", "MB", "GB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n.toFixed(1)} ${u[i]}`;
  }
  // Readable local date/time for a backup's created_at (falls back to the raw value).
  function fmtDate(s) {
    if (!s) return "—";
    const d = new Date(s);
    return isNaN(d) ? s : d.toLocaleString();
  }
  // The backup's storage name (the file's basename, e.g. 20260602-150405.tar.gz).
  function backupName(b) {
    const p = (b.path || "").split("/");
    return p[p.length - 1] || b.id;
  }

  // can(perm) — does the caller hold this permission on this server? The API
  // attaches `perms` (effective permissions; admins get all). Drives which tabs
  // and action buttons a delegated user sees, so they never face a button that
  // would just 403.
  const can = (p) => server?.perms?.includes(p) ?? false;

  // Tabs are filtered to what the caller may actually do (Norn/Mods are DayZ-only
  // and need write access; Install log only needs view).
  let tabs = $derived(
    [
      ...(can("server.console") ? [["console", "Console"]] : []),
      ...(server?.players_supported && can("server.console") ? [["players", "Players"]] : []),
      ...(server?.admin_log_supported && can("server.view") ? [["activity", "Activity"]] : []),
      ...(can("server.files") ? [["files", "Files"]] : []),
      ...(can("server.backup") ? [["backups", "Backups"]] : []),
      ...(can("server.control") ? [["settings", "Settings"]] : []),
      ...(can("server.files") ? [["anticheat", "Anti-cheat"]] : []),
      ...(server?.gameskill_id === "dayz" && can("server.control")
        ? [["mods", "Mods"], ["norn", "Norn (loot)"]]
        : []),
      ["install", "Install log"],
    ],
  );

  // Hover help for each tab (English), shown on the tab buttons.
  const tabHelp = {
    console: "Live server console — read output and send commands (or RCON, where the game supports it).",
    players: "Who's connected right now. Kick, broadcast a message, or lock joins.",
    activity: "Recent server activity parsed from the game's admin log — joins, disconnects, deaths, kills.",
    files: "Browse, edit and upload this server's files (configs, mods, world data).",
    backups: "Create, restore and manage backups of this server's data.",
    settings: "Edit this server's variables, resource limits, mods and delegated access.",
    anticheat: "Anti-cheat status and configuration hints for this game.",
    mods: "Workshop mods for this server (add/remove, then Update to download).",
    norn: "DayZ loot economy (Norn) — lifetimes, spawn tuning, mod loot import.",
    install: "Live output from the last install / update / reinstall run.",
  };

  // Keep the active tab valid as perms/tabs resolve — if the current tab isn't
  // available to this user, fall back to the first one they can see. Guard on
  // `server` being loaded: before it is, can() denies everything so `tabs` is just
  // ["install"], and firing then would wrongly strand the user on the install log
  // (it stays valid once real tabs appear, so the fallback never corrects back).
  $effect(() => {
    if (server && tabs.length && !tabs.some(([k]) => k === tab)) {
      tab = tabs[0][0];
    }
  });
  let economy = $state(null);
  let modLoot = $state(null);
  let nornBusy = $state(false);
  let minHours = $state(4);
  let globalsEdit = $state({});
  function fmtDur(sec) {
    if (sec == null || sec < 0) return "—";
    if (sec < 3600) return `${Math.round(sec / 60)} min`;
    if (sec < 86400) return `${(sec / 3600).toFixed(1)} h`;
    return `${(sec / 86400).toFixed(1)} d`;
  }
  async function loadEconomy() {
    [economy, modLoot] = await Promise.all([
      api.get(`/servers/${id}/dayz/economy`).catch(() => null),
      api.get(`/servers/${id}/dayz/mod-loot`).catch(() => null),
    ]);
    globalsEdit = { ...(economy?.globals || {}) };
  }
  async function importModTypes(path) {
    nornBusy = true;
    try {
      const r = await api.post(`/servers/${id}/dayz/import-mod-types`, { path });
      toast(`Imported ${r.imported} into the economy — set a floor + restart`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function applyMinLifetime() {
    if (!(minHours > 0)) return toast("Enter hours > 0", "warn");
    nornBusy = true;
    try {
      const r = await api.post(`/servers/${id}/dayz/min-lifetime`, { hours: Number(minHours) });
      toast(`Raised ${r.changed} item lifetimes to ≥ ${minHours} h — restart to apply`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function saveGlobals() {
    nornBusy = true;
    try {
      const payload = {};
      for (const [k, v] of Object.entries(globalsEdit)) payload[k] = Number(v) || 0;
      const r = await api.post(`/servers/${id}/dayz/globals`, payload);
      toast(`Updated ${r.changed} cleanup timer(s) — restart to apply`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function resetNorn() {
    if (!confirm("Forget all saved Norn settings? Loot files revert to vanilla on the next Update/Reinstall.")) return;
    nornBusy = true;
    try {
      await api.post(`/servers/${id}/dayz/reset`, {});
      toast("Norn settings cleared — Update/Reinstall to restore vanilla loot", "info");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }
  async function registerTypes() {
    nornBusy = true;
    try {
      const r = await api.post(`/servers/${id}/dayz/register-types`, {});
      toast(`Registered ${r.registered} modded types file(s) — apply a lifetime floor + restart`, "success");
      await loadEconomy();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      nornBusy = false;
    }
  }

  // --- Mod control: which configured Workshop mods are installed / still exist ---
  let modStatus = $state(null);
  let modsBusy = $state(false);
  async function loadMods() {
    modStatus = await api.get(`/servers/${id}/dayz/mods`).catch(() => null);
  }
  async function setMods(ids) {
    modsBusy = true;
    try {
      await api.put(`/servers/${id}`, { mods: ids.join(";") });
      toast("Mod list updated — press Update/Reinstall to download + apply", "success");
      await Promise.all([loadServer(), loadMods()]);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modsBusy = false;
    }
  }
  function removeMod(modId) {
    const keep = (modStatus?.mods || []).map((m) => m.id).filter((x) => x !== modId);
    return setMods(keep);
  }
  function addOrphan(modId) {
    const cur = (modStatus?.mods || []).map((m) => m.id);
    return setMods([...cur, modId]);
  }
  function pruneBroken() {
    const broken = (modStatus?.mods || []).filter((m) => !m.installed || m.workshop === "removed");
    if (!broken.length) return;
    if (!confirm(`Remove ${broken.length} missing/removed mod(s) from the load order? Press Update/Reinstall afterwards.`)) return;
    const keep = (modStatus?.mods || []).filter((m) => m.installed && m.workshop !== "removed").map((m) => m.id);
    return setMods(keep);
  }

  let skill = $state(null); // parsed gameskill (for anti-cheat surface + edit form)

  // Edit settings
  let edit = $state(null); // { name, env, cpu_percent, memory_mb }
  let savingEdit = $state(false);
  // Admin-only host bind mounts (e.g. a media library /mnt/mediaserver → /media).
  let hostMounts = $state([]);
  function openEdit() {
    edit = {
      name: server.name,
      env: { ...(server.env || {}) },
      cpu_percent: server.cpu_percent || 0,
      memory_mb: server.memory_mb || 0,
      bm_server_id: server.bm_server_id || "",
      auto_forward: server.auto_forward !== false,
      autostart: server.autostart !== false,
      status_public: !!server.status_public,
      subdomain: server.subdomain || "",
    };
    hostMounts = (server.host_mounts || []).map((m) => ({ host: m.host, container: m.container, rw: !!m.rw }));
  }
  const addMount = () => (hostMounts = [...hostMounts, { host: "", container: "", rw: false }]);
  const removeMount = (i) => (hostMounts = hostMounts.filter((_, j) => j !== i));
  async function saveEdit() {
    savingEdit = true;
    try {
      const env = {};
      for (const [k, v] of Object.entries(edit.env)) env[k] = String(v);
      const payload = {
        name: edit.name,
        env,
        cpu_percent: Number(edit.cpu_percent) || 0,
        memory_mb: Number(edit.memory_mb) || 0,
        bm_server_id: edit.bm_server_id || "",
        auto_forward: !!edit.auto_forward,
        autostart: !!edit.autostart,
        status_public: !!edit.status_public,
        subdomain: edit.subdomain || "",
      };
      // Host mounts are admin-only; only send the field when the caller is an admin
      // (the backend rejects it otherwise), and drop blank rows.
      if ($user?.role === "admin") {
        payload.host_mounts = hostMounts
          .filter((m) => m.host.trim() && m.container.trim())
          .map((m) => ({ host: m.host.trim(), container: m.container.trim(), rw: !!m.rw }));
      }
      await api.put(`/servers/${id}`, payload);
      toast("Saved — restart to apply (reinstall for file-baked values)", "success");
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingEdit = false;
    }
  }

  // --- Per-server user delegation (admin only) ---
  // The permissions that make sense to grant on a single server.
  const DELEGATE_PERMS = [
    ["server.view", "View"],
    ["server.control", "Start / Stop"],
    ["server.console", "Console / RCON"],
    ["server.files", "Files"],
    ["server.backup", "Backups"],
    ["server.schedule", "Schedules"],
    ["server.delete", "Delete"],
  ];
  let allUsers = $state([]);
  let delegates = $state([]); // [{ user_id, username, role, perms: [] }]
  let savingDelegates = $state(false);
  async function loadDelegation() {
    if ($user?.role !== "admin") return;
    try {
      const [users, dels] = await Promise.all([
        api.get("/users"),
        api.get(`/servers/${id}/delegates`),
      ]);
      // Only non-admin users are delegable (admins already have full access).
      allUsers = users.filter((u) => u.role !== "admin");
      delegates = dels;
    } catch (e) {
      toast(e.message, "error");
    }
  }
  function addDelegate(userId) {
    if (!userId) return;
    if (delegates.some((d) => d.user_id === userId)) return;
    const u = allUsers.find((x) => x.id === userId);
    if (!u) return;
    delegates = [...delegates, { user_id: u.id, username: u.username, role: u.role, perms: ["server.view"] }];
  }
  function toggleDelegatePerm(userId, perm) {
    delegates = delegates.map((d) => {
      if (d.user_id !== userId) return d;
      const has = d.perms.includes(perm);
      return { ...d, perms: has ? d.perms.filter((p) => p !== perm) : [...d.perms, perm] };
    });
  }
  function removeDelegate(userId) {
    delegates = delegates.filter((d) => d.user_id !== userId);
  }
  async function saveDelegates() {
    savingDelegates = true;
    try {
      // Drop any delegate with no permissions (equivalent to removing access).
      const payload = delegates
        .filter((d) => d.perms.length > 0)
        .map((d) => ({ user_id: d.user_id, perms: d.perms }));
      await api.put(`/servers/${id}/delegates`, payload);
      toast("Delegated access saved", "success");
      await loadDelegation();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingDelegates = false;
    }
  }
  let undelegatedUsers = $derived(allUsers.filter((u) => !delegates.some((d) => d.user_id === u.id)));

  // Public connect address (from network settings).
  let network = $state(null);
  async function loadNetwork() {
    try {
      network = await api.get("/settings/network");
    } catch (e) {
      /* non-fatal */
    }
  }
  let connectHost = $derived(network?.effective || "");

  // BattleMetrics live status (only when a BM id is configured on the server).
  let bm = $state(null);
  async function loadBM() {
    if (!server?.bm_server_id) {
      bm = null;
      return;
    }
    bm = await api.get(`/servers/${id}/battlemetrics`).catch(() => null);
  }

  // "Online from outside" — probes the server via its public address (see backend).
  let reach = $state(null);
  async function loadReach() {
    if (!server || (server.status !== "running" && server.status !== "starting")) {
      reach = null;
      return;
    }
    reach = await api.get(`/servers/${id}/reachability`).catch(() => null);
  }

  async function loadServer() {
    try {
      const prev = server;
      server = await api.get(`/servers/${id}`);
      // When an install finishes, refresh the console.
      if (prev && !prev.installed && server.installed) {
        toast("Install complete", "success");
      }
      if (!skill && server) {
        skill = await api.get(`/gameskills/${server.gameskill_id}`).catch(() => null);
      }
      loadBM();
      loadReach();
      if (server.installed && can("server.control")) loadAutoRestart();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function connectInstallLog() {
    if (installWs) return;
    installLines = [];
    installWs = new WebSocket(wsURL(`/servers/${id}/install/log`));
    installWs.onmessage = (ev) => {
      installLines = [...installLines.slice(-2000), ev.data];
      queueMicrotask(() => {
        if (installEl) installEl.scrollTop = installEl.scrollHeight;
      });
      // Refresh server state when the install reports completion.
      if (/Install complete|Install FAILED/.test(ev.data)) {
        setTimeout(loadServer, 500);
      }
    };
    installWs.onclose = () => {
      installWs = null;
    };
  }

  let cloning = $state(false);
  async function cloneServer() {
    const name = prompt("Name for the clone:", `${server.name} (copy)`);
    if (name === null) return; // cancelled
    cloning = true;
    try {
      const r = await api.post(`/servers/${id}/clone`, { name: name.trim() });
      toast("Cloning — the copy is installing…", "success");
      navigate(`/servers/${r.id}`);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      cloning = false;
    }
  }

  async function runInstall(confirmFirst = false) {
    if (confirmFirst &&
      !confirm("Update / reinstall this server? It re-runs the install script to fetch the latest version. Back up your world first — config files may be regenerated."))
      return;
    try {
      await api.post(`/servers/${id}/install`);
      tab = "install";
      connectInstallLog();
      toast("Update / reinstall started", "info");
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function connectConsole() {
    closeWS();
    lines = [];
    ws = new WebSocket(wsURL(`/servers/${id}/console`));
    ws.onmessage = (ev) => {
      lines = [...lines.slice(-1000), ev.data];
      queueMicrotask(() => {
        if (termEl) termEl.scrollTop = termEl.scrollHeight;
      });
    };
    ws.onclose = () => {
      lines = [...lines, "[console disconnected]"];
    };
  }
  function closeWS() {
    if (ws) {
      ws.onclose = null;
      ws.close();
      ws = null;
    }
  }

  function sendCmd(e) {
    e.preventDefault();
    if (!cmd.trim() || !ws || ws.readyState !== 1) return;
    ws.send(cmd);
    lines = [...lines, `> ${cmd}`];
    cmd = "";
  }

  async function pollStats() {
    try {
      stats = await api.get(`/servers/${id}/stats`);
    } catch {
      stats = null;
    }
    try {
      status = await api.get(`/servers/${id}/query`);
    } catch {
      status = null;
    }
  }

  async function action(verb) {
    try {
      await api.post(`/servers/${id}/${verb}`);
      toast(verb, "success");
      await loadServer();
      if (verb !== "stop") setTimeout(connectConsole, 800);
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function del() {
    if (!confirm(`Delete "${server.name}"? This removes the container.`)) return;
    try {
      await api.del(`/servers/${id}`);
      toast("Server deleted", "success");
      navigate("/servers");
    } catch (e) {
      toast(e.message, "error");
    }
  }

  onMount(async () => {
    loadNetwork();
    await loadServer();
    // Default to the Console tab. Only jump to the install log when an install is
    // actually running right now — otherwise Console is what you want to see on entry.
    if (server && !server.installed && server.install_status === "installing") {
      tab = "install";
      connectInstallLog();
    } else if (server?.status === "running" || server?.status === "starting") {
      connectConsole();
    }
    pollStats();
    statsTimer = setInterval(pollStats, 4000);
  });
  onDestroy(() => {
    closeWS();
    if (installWs) {
      installWs.onclose = null;
      installWs.close();
    }
    clearInterval(statsTimer);
  });
</script>

{#if !server}
  <div class="text-muted">Loading…</div>
{:else}
  <div class="flex items-center gap-3 mb-1">
    <button class="btn-ghost px-2 py-1" onclick={() => navigate("/servers")}>←</button>
    <h1 class="text-2xl font-semibold">{server.name}</h1>
    <span class="badge {server.status === 'running' ? 'bg-accent2/20 text-accent' : server.status === 'starting' ? 'bg-warn/20 text-warn' : 'bg-border text-muted'}"
      >{server.status}</span
    >
    {#if reach}
      <span
        class="badge {reach.reachable ? 'bg-accent2/20 text-accent' : 'bg-warn/20 text-warn'}"
        title={reach.reachable
          ? `Responds from the internet on ${reach.host}:${reach.port}`
          : `No external reply on ${reach.host}:${reach.port} — check the port forward (or your router's NAT loopback)`}
      >
        {reach.reachable ? "🌐 reachable" : "🌐 not from outside"}
      </span>
    {/if}
    {#if bm && bm.configured}
      <a
        href={bm.url}
        target="_blank"
        rel="noopener"
        class="badge {bm.online ? 'bg-accent2/20 text-accent' : 'bg-border text-muted'}"
        title="BattleMetrics{bm.rank ? ` · rank #${bm.rank}` : ''}"
      >
        BM: {bm.online ? `online ${bm.players}/${bm.max_players}` : bm.status || "offline"}
      </a>
    {/if}
  </div>
  <div class="text-muted text-sm mb-4">{server.gameskill_id}</div>

  <!-- Controls + live stats (each gated on the caller's permissions) -->
  <div class="flex flex-wrap items-center gap-2 mb-4">
    {#if !server.installed}
      {#if can("server.control")}
        <button class="btn-primary" onclick={runInstall} disabled={server.install_status === "installing"}
          title="Download and build this server (runs the rune's install script). Needed once before it can start.">
          {server.install_status === "installing" ? "Installing…" : server.install_status === "error" ? "Retry install" : "Install"}
        </button>
      {/if}
    {:else if (server.status === "running" || server.status === "starting")}
      {#if can("server.control")}
        <button class="btn-ghost" onclick={() => action("restart")}
          title="Restart now, immediately — no player warning. Recreates the container so rune/env/mod changes apply. Does not update the game version.">Restart</button>
        <button class="btn-ghost" onclick={() => { showSafe = true; if (!backupTargets.length) loadBackups(); }}
          title={server.restart_warn
            ? "Restart with an in-game countdown for players first (and an optional backup). Opens a dialog — nothing happens until you confirm."
            : "Restart, with an optional backup first. This rune has no player warnings, so it restarts promptly. Opens a dialog first."}>
          Safe restart{server.restart_warn ? " ⏱" : ""}
        </button>
        <button class="btn-ghost" onclick={() => { showAuto = true; loadQuietHours(); if (!backupTargets.length) loadBackups(); }}
          title="Schedule automatic restarts every N hours (a managed schedule). Opens a dialog to configure hours, player warning and backup.">
          🔁 Auto-restart{autoRestart.enabled ? ` · ${autoRestart.every_hours}h` : ""}
        </button>
        <button class="btn-ghost" onclick={() => action("stop")}
          title="Stop the server now (graceful shutdown). Players are disconnected.">Stop</button>
      {/if}
    {:else if can("server.control")}
      <button class="btn-primary" onclick={() => action("start")}
        title="Start this server now.">Start</button>
      <button class="btn-ghost" onclick={() => runInstall(true)}
        title="Re-run the install script: updates the game to the latest version and re-downloads mods (SteamCMD). Back up your world first — config files may be regenerated.">Update / Reinstall</button>
    {/if}
    {#if server.installed && server.watchdog_supported && can("server.control")}
      <button
        class="btn-ghost {server.watchdog ? 'text-accent' : 'text-muted'}"
        disabled={watchdogBusy}
        title="Auto-restart this server if the game stops responding while the container is up"
        onclick={toggleWatchdog}>
        🩺 Watchdog: {server.watchdog ? "on" : "off"}
      </button>
    {/if}
    {#if $user?.can_create}
      <button class="btn-ghost" disabled={cloning} onclick={cloneServer}
        title="Create a new server with this one's setup — same rune, variables, resource limits and mods — with fresh ports and an empty data dir. Clones the configuration, not the world/data; the copy installs fresh.">
        {cloning ? "Cloning…" : "⧉ Clone"}</button>
    {/if}
    {#if server.wipe_supported && can("server.control")}
      <button class="btn-ghost text-warn {can('server.delete') ? '' : 'ml-auto'}" onclick={() => { showWipe = true; if (!backupTargets.length) loadBackups(); }}
        title="Reset the world / persistence (loot, bases, progress) as defined by the rune. Opens a confirmation dialog with a backup-first option — it does not wipe until you confirm. Runs immediately once confirmed (it is not a schedule). Config and mods are kept.">🧹 Wipe</button>
    {/if}
    {#if can("server.delete")}
      <button class="btn-danger ml-auto" onclick={del}
        title="Permanently delete this server and all its data (world, backups list, ports). Cannot be undone.">Delete</button>
    {/if}
  </div>

  {#if showWipe}
    <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
      <div class="card p-5 w-full max-w-md space-y-4">
        <h2 class="text-lg font-semibold text-warn">🧹 Wipe {server.name}?</h2>
        <p class="text-sm text-muted">
          This <b>permanently deletes</b> this server's world / persistence (loot, bases, progress) as
          defined by its rune, then restarts it fresh. Config, whitelist and mods are kept. This cannot
          be undone{wipeBackupFirst ? " — but a backup is taken first" : ""}.
        </p>
        <label class="inline-flex items-center gap-2 text-sm">
          <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={wipeBackupFirst} />
          <span>Back up first (recommended)</span>
        </label>
        {#if wipeBackupFirst}
          <div>
            <label class="label" for="wipe-target">Backup target</label>
            <select id="wipe-target" class="input" bind:value={selectedTarget}>
              {#if backupTargets.length === 0}<option value="">No targets — create one in Settings</option>{/if}
              {#each backupTargets as t}<option value={t.id}>{t.name}</option>{/each}
            </select>
          </div>
        {/if}
        <div class="flex gap-2 pt-1">
          <button class="btn-ghost flex-1" disabled={wiping} onclick={() => (showWipe = false)}>Cancel</button>
          <button class="btn-danger flex-1" disabled={wiping} onclick={doWipe}>
            {wiping ? "Wiping…" : "Wipe now"}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if showSafe}
    <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
      <div class="card p-5 w-full max-w-md space-y-4">
        <h2 class="text-lg font-semibold">Safe restart {server.name}</h2>
        <p class="text-sm text-muted">
          {#if server.restart_warn}
            Players get an in-game countdown, then the server restarts. Good for a graceful reboot.
          {:else}
            Restarts the server{safeBackupFirst ? " after a backup" : ""}. (This rune has no player
            warnings, so it restarts promptly.)
          {/if}
        </p>
        <label class="inline-flex items-center gap-2 text-sm">
          <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={safeBackupFirst} />
          <span>Back up first</span>
        </label>
        {#if safeBackupFirst}
          <div>
            <label class="label" for="safe-target">Backup target</label>
            <select id="safe-target" class="input" bind:value={selectedTarget}>
              {#if backupTargets.length === 0}<option value="">No targets — create one in Settings</option>{/if}
              {#each backupTargets as t}<option value={t.id}>{t.name}</option>{/each}
            </select>
          </div>
        {/if}
        <div class="flex gap-2 pt-1">
          <button class="btn-ghost flex-1" disabled={safeBusy} onclick={() => (showSafe = false)}>Cancel</button>
          <button class="btn-primary flex-1" disabled={safeBusy} onclick={doSafeRestart}>
            {safeBusy ? "Starting…" : "Safe restart"}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if showAuto}
    <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
      <div class="card p-5 w-full max-w-md space-y-4">
        <h2 class="text-lg font-semibold">🔁 Auto-restart {server.name}</h2>
        <p class="text-sm text-muted">
          Restarts the server on a schedule to keep it fresh (clears memory leaks, applies pending
          changes). Runs through the same safe-restart path{server.restart_warn ? ", so players get the rune's in-game countdown first" : ""}.
        </p>
        <div>
          <label class="label" for="auto-hours">Restart every</label>
          <select id="auto-hours" class="input" bind:value={autoRestart.every_hours}>
            {#each autoHourOptions as h}<option value={h}>{h === 24 ? "24 hours (daily)" : `${h} hours`}</option>{/each}
          </select>
          <p class="text-xs text-muted mt-1">Fires at the top of the hour, every {autoRestart.every_hours}h (server local time).</p>
          {#if quietHours?.has_data}
            <p class="text-xs text-accent mt-1">
              💡 Quietest around {hh(quietHours.recommended_hour)} (avg {quietHours.recommended_avg} players, last 14 days) — a good time to restart.
            </p>
          {/if}
        </div>
        {#if server.restart_warn}
          <label class="inline-flex items-center gap-2 text-sm">
            <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={autoRestart.warn} />
            <span>Warn players with the in-game countdown first</span>
          </label>
        {/if}
        <label class="inline-flex items-center gap-2 text-sm">
          <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={autoRestart.backup_first} />
          <span>Back up before each restart</span>
        </label>
        {#if autoRestart.backup_first}
          <div>
            <label class="label" for="auto-target">Backup target</label>
            <select id="auto-target" class="input" bind:value={autoRestart.target_id}>
              {#if backupTargets.length === 0}<option value="">No targets — create one in Settings</option>{/if}
              {#each backupTargets as t}<option value={t.id}>{t.name}</option>{/each}
            </select>
          </div>
        {/if}
        <div class="flex gap-2 pt-1">
          <button class="btn-ghost flex-1" disabled={autoBusy} onclick={() => (showAuto = false)}>Cancel</button>
          {#if autoRestart.enabled}
            <button class="btn-ghost flex-1 text-warn" disabled={autoBusy} onclick={() => saveAutoRestart(false)}>
              {autoBusy ? "…" : "Turn off"}
            </button>
          {/if}
          <button class="btn-primary flex-1" disabled={autoBusy} onclick={() => saveAutoRestart(true)}>
            {autoBusy ? "Saving…" : autoRestart.enabled ? "Update" : "Turn on"}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if !server.installed}
    <div class="card border-warn/40 bg-warn/10 text-warn text-sm px-4 py-2 mb-4">
      This server isn't installed yet. Click <b>Install</b> to download/build it; progress shows below.
    </div>
  {/if}

  {#if server.ports && Object.keys(server.ports).length}
    <div class="card p-3 mb-4">
      <div class="text-xs text-muted uppercase tracking-wide mb-1">Connect address</div>
      <div class="flex flex-wrap gap-2">
        {#each Object.entries(server.ports) as [name, port]}
          <span class="badge bg-panel2 border border-border font-mono text-xs">
            {name}: {connectHost || "your-host"}:{port}
          </span>
        {/each}
      </div>
      {#if !connectHost}
        <div class="text-muted text-xs mt-1">Set a public hostname in <a href="#/settings" class="underline">Settings → Network</a> to replace “your-host”.</div>
      {/if}
    </div>
  {/if}

  {#if stats && (server.status === "running" || server.status === "starting")}
    <div class="grid grid-cols-2 sm:grid-cols-3 gap-3 mb-4">
      <div class="card p-3">
        <div class="text-xs text-muted">CPU</div>
        <div class="text-lg font-semibold">{stats.cpu_percent?.toFixed(1)}%</div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-muted">Memory</div>
        <div class="text-lg font-semibold">{stats.mem_usage_mb?.toFixed(0)} MB</div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-muted">Players</div>
        <div class="text-lg font-semibold">
          {#if status && status.online}
            {status.players}{status.max_players ? ` / ${status.max_players}` : ""}
          {:else}
            —
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Resource history -->
  <div class="mb-4">
    <div class="flex items-center gap-2">
      <button class="text-sm text-muted hover:text-text" onclick={() => (showHistory = !showHistory)}>
        {showHistory ? "▾" : "▸"} 📈 History
      </button>
      {#if showHistory}
        <div class="inline-flex rounded-md border border-border overflow-hidden text-xs ml-2">
          {#each [[24, "24h"], [72, "3d"], [168, "7d"]] as [h, lbl]}
            <button class="px-2 py-1 {metricsHours === h ? 'bg-panel2 text-text' : 'text-muted hover:bg-panel2/50'}"
              onclick={() => (metricsHours = h)}>{lbl}</button>
          {/each}
        </div>
      {/if}
    </div>
    {#if showHistory}
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mt-3">
        <Sparkline values={cpuSeries} label="CPU" unit="%" color="rgb(var(--c-accent2))" format={(v) => v.toFixed(0)} />
        <Sparkline values={memSeries} label="Memory" unit=" MB" color="rgb(var(--c-accent))" format={(v) => v.toFixed(0)} />
        <Sparkline values={playerSeries} label="Players" unit="" color="rgb(var(--c-warn))" format={(v) => v.toFixed(0)} />
      </div>
    {/if}
  </div>

  <!-- Tabs — scroll horizontally on narrow screens instead of clipping -->
  <div class="flex gap-1 border-b border-border mb-4 overflow-x-auto -mx-4 px-4 sm:mx-0 sm:px-0">
    {#each tabs as [key, label]}
      <button
        title={tabHelp[key] || ""}
        class="px-4 py-2 text-sm border-b-2 -mb-px shrink-0 whitespace-nowrap {tab === key
          ? 'border-accent text-text'
          : 'border-transparent text-muted hover:text-text'}"
        onclick={() => {
          tab = key;
          if (key === "install" && !installWs) connectInstallLog();
          if (key === "backups") loadBackups();
          if (key === "norn") loadEconomy();
          if (key === "mods") loadMods();
          if (key === "settings") {
            openEdit();
            loadDelegation();
          }
        }}>{label}</button
      >
    {/each}
  </div>

  {#snippet explainBlock(context, text)}
    {#if server.ai_enabled}
      <div class="mt-3">
        <button class="btn-ghost text-xs" disabled={explainBusy} onclick={() => explainError(context, text)}
          title="Send the log above to your configured AI assistant for a plain-language cause + fix (advisory).">
          {explainBusy ? "Analyzing…" : "🤖 Explain this"}
        </button>
      </div>
      {#if explain}
        <div class="card border-accent2/40 bg-accent2/5 p-3 text-sm space-y-1 mt-2">
          <div class="text-xs uppercase tracking-wide text-accent flex items-center gap-2">
            <span>🤖 Kvasir explains</span>
            <button class="text-muted hover:text-text ml-auto" title="Dismiss" onclick={() => (explain = "")}>✕</button>
          </div>
          <div class="whitespace-pre-wrap break-words">{explain}</div>
          <div class="text-[10px] text-muted pt-1">Advisory only — generated by your configured LLM; may contain mistakes.</div>
        </div>
      {/if}
    {/if}
  {/snippet}

  {#if tab === "install"}
    <div bind:this={installEl} class="term h-[50vh]">
      {#if installLines.length === 0}
        <div class="text-muted">No install output yet. Click Install to begin.</div>
      {/if}
      {#each installLines as l}<div>{l}</div>{/each}
    </div>
    {@render explainBlock("install", installLines.join("\n"))}
  {:else if tab === "console"}
    <div bind:this={termEl} class="term h-[50vh]">
      {#each lines as l}<div>{l}</div>{/each}
    </div>
    <form onsubmit={sendCmd} class="flex gap-2 mt-3">
      <input
        class="input font-mono"
        bind:value={cmd}
        placeholder={(server.status === "running" || server.status === "starting") ? "Type a console command…" : "Server is stopped"}
        disabled={server.status !== "running"}
      />
      <button class="btn-primary" disabled={server.status !== "running"}>Send</button>
    </form>
    {#if (server.status === "running" || server.status === "starting") && (!ws || ws.readyState !== 1)}
      <button class="btn-ghost mt-2" onclick={connectConsole}>Reconnect console</button>
    {/if}
    {@render explainBlock("console", lines.join("\n"))}
  {:else if tab === "players"}
    <div class="space-y-4">
      <div class="flex items-center gap-2 flex-wrap">
        <span class="text-sm text-muted">
          {#if !playersData.online}
            {playersData.reason || "Server offline or still starting."}
          {:else}
            {playersData.players.length} online
          {/if}
        </span>
        <button class="btn-ghost text-xs ml-auto" disabled={playersBusy} onclick={loadPlayers}
          title="Reload the player list now (it also auto-refreshes every 10 seconds).">Refresh</button>
        {#if playersData.can_lock}
          <button class="btn-ghost text-xs {serverLocked ? 'text-warn' : ''}" disabled={playersBusy} onclick={toggleLock}
            title={serverLocked
              ? "Allow new players to join again."
              : "Stop new players from joining (current players stay). Useful before a restart or during maintenance."}>
            {serverLocked ? "🔒 Unlock joins" : "🔓 Lock joins"}
          </button>
        {/if}
      </div>

      {#if playersData.can_broadcast}
        <form onsubmit={(e) => { e.preventDefault(); sendBroadcast(); }} class="flex gap-2">
          <input class="input" bind:value={broadcastMsg} placeholder="Broadcast a message to all players…" disabled={playersBusy || !playersData.online}
            title="Send an in-game message shown to everyone currently connected." />
          <button class="btn-primary" disabled={playersBusy || !playersData.online || !broadcastMsg.trim()}
            title="Send this message to all online players.">Broadcast</button>
        </form>
      {/if}

      {#if playersData.online && playersData.players.length === 0}
        <div class="card p-4 text-sm text-muted text-center">No players connected.</div>
      {:else if playersData.players.length}
        <div class="card overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-xs text-muted uppercase tracking-wide">
              <tr class="border-b border-border">
                <th class="text-left px-3 py-2">#</th>
                <th class="text-left px-3 py-2">Name</th>
                <th class="text-left px-3 py-2">Ping</th>
                <th class="text-left px-3 py-2 hidden sm:table-cell">GUID</th>
                {#if playersData.can_kick}<th class="px-3 py-2"></th>{/if}
              </tr>
            </thead>
            <tbody>
              {#each playersData.players as p}
                <tr class="border-b border-border/50">
                  <td class="px-3 py-2 font-mono text-muted">{p.id || "—"}</td>
                  <td class="px-3 py-2 font-medium">{p.name}</td>
                  <td class="px-3 py-2 text-muted">{p.ping || "—"}</td>
                  <td class="px-3 py-2 font-mono text-xs text-muted hidden sm:table-cell truncate max-w-[12rem]">{p.guid || "—"}</td>
                  {#if playersData.can_kick}
                    <td class="px-3 py-2 text-right">
                      <button class="btn-ghost text-xs text-warn" disabled={playersBusy} onclick={() => kickPlayer(p)}
                        title="Disconnect this player now (they can rejoin). You'll be asked for an optional reason. To block them permanently, use Bans.">Kick</button>
                    </td>
                  {/if}
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  {:else if tab === "activity"}
    <div class="space-y-3">
      <div class="flex items-center gap-2">
        <span class="text-sm text-muted hidden sm:inline" title="Parsed from the game's admin log — joins, disconnects, deaths and kills, newest first.">
          Activity{activity.file ? ` · ${activity.file}` : ""}
        </span>
        <div class="inline-flex rounded-md border border-border overflow-hidden text-xs">
          <button class="px-2 py-1 {activityView === 'feed' ? 'bg-panel2 text-text' : 'text-muted hover:bg-panel2/50'}" onclick={() => (activityView = 'feed')}>Feed</button>
          <button class="px-2 py-1 border-l border-border {activityView === 'board' ? 'bg-panel2 text-text' : 'text-muted hover:bg-panel2/50'}" onclick={() => (activityView = 'board')} title="Per-player kills / deaths / K·D tallied from the recent log">🏆 Leaderboard</button>
        </div>
        {#if server.ai_enabled}
          <button class="btn-primary text-xs ml-auto" disabled={digestBusy}
            title="Ask the configured AI assistant for a plain-language summary of the recent activity (advisory — sends the log events to the LLM your admin set up)."
            onclick={loadDigest}>{digestBusy ? "Summarizing…" : "🤖 Summarize"}</button>
          <button class="btn-ghost text-xs {server.ai_enabled ? '' : 'ml-auto'}" disabled={activityBusy} onclick={loadActivity}
            title="Re-read the admin log and refresh the feed.">Refresh</button>
        {:else}
          <button class="btn-ghost text-xs ml-auto" disabled={activityBusy} onclick={loadActivity}
            title="Re-read the admin log and refresh the feed.">Refresh</button>
        {/if}
      </div>
      {#if digest}
        <div class="card border-accent2/40 bg-accent2/5 p-3 text-sm space-y-1">
          <div class="text-xs uppercase tracking-wide text-accent flex items-center gap-2">
            <span>🤖 Kvasir digest</span>
            <button class="text-muted hover:text-text ml-auto" title="Dismiss" onclick={() => (digest = "")}>✕</button>
          </div>
          <div class="whitespace-pre-wrap break-words">{digest}</div>
          <div class="text-[10px] text-muted pt-1">Advisory only — generated by your configured LLM from the activity log; may contain mistakes.</div>
        </div>
      {/if}
      {#if !activity.events.length}
        <div class="card p-4 text-sm text-muted text-center">
          {activityBusy ? "Loading…" : "No activity logged yet (the server writes its admin log while running)."}
        </div>
      {:else if activityView === "board"}
        {#if !leaderboard.length}
          <div class="card p-4 text-sm text-muted text-center">No player kills/deaths in the recent log yet.</div>
        {:else}
          <div class="card overflow-x-auto">
            <table class="w-full text-sm">
              <thead class="text-xs text-muted uppercase tracking-wide">
                <tr class="border-b border-border">
                  <th class="text-left px-3 py-2">#</th>
                  <th class="text-left px-3 py-2">Player</th>
                  <th class="text-right px-3 py-2">🔫 Kills</th>
                  <th class="text-right px-3 py-2">💀 Deaths</th>
                  <th class="text-right px-3 py-2">K·D</th>
                  <th class="text-right px-3 py-2 hidden sm:table-cell">🟢 Joins</th>
                </tr>
              </thead>
              <tbody>
                {#each leaderboard as p, i}
                  <tr class="border-b border-border/50">
                    <td class="px-3 py-2 text-muted">{i === 0 ? "🥇" : i === 1 ? "🥈" : i === 2 ? "🥉" : i + 1}</td>
                    <td class="px-3 py-2 font-medium">{p.name}</td>
                    <td class="px-3 py-2 text-right">{p.kill}</td>
                    <td class="px-3 py-2 text-right text-muted">{p.death}</td>
                    <td class="px-3 py-2 text-right font-mono">{p.kd.toFixed(2)}</td>
                    <td class="px-3 py-2 text-right text-muted hidden sm:table-cell">{p.join}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
          <div class="text-[10px] text-muted">Tallied from the {activity.events.length} most recent log events.</div>
        {/if}
      {:else}
        <div class="card divide-y divide-border/50">
          {#each activity.events as ev}
            <div class="flex items-start gap-3 px-3 py-2 text-sm">
              <span class="shrink-0" title={ev.type}>{activityIcons[ev.type] || "•"}</span>
              <span class="shrink-0 font-mono text-xs text-muted w-16">{ev.time || "—"}</span>
              <span class="flex-1 break-words">
                {#if ev.player}<span class="font-medium">{ev.player}</span> {/if}<span class="text-muted">{ev.line}</span>
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {:else if tab === "files"}
    <FileManager serverId={id} />
  {:else if tab === "settings"}
    {#if edit}
      <div class="max-w-lg space-y-4">
        {#if server.ai_enabled && can("server.control")}
          <div class="card p-3 space-y-2">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium">🤖 Config review</span>
              <button class="btn-ghost text-xs ml-auto" disabled={configAdviceBusy} onclick={reviewConfig}
                title="Ask your configured AI assistant to review these settings for footguns (weak passwords, low memory, risky options). Advisory.">
                {configAdviceBusy ? "Reviewing…" : "Review config"}
              </button>
            </div>
            {#if configAdvice}
              <div class="whitespace-pre-wrap break-words text-sm">{configAdvice}</div>
              <div class="text-[10px] text-muted">Advisory only — generated by your configured LLM; secret values are never sent. May contain mistakes.</div>
            {/if}
          </div>
        {/if}
        <div>
          <label class="label" for="e-name">Server name</label>
          <input id="e-name" class="input" bind:value={edit.name} />
        </div>
        {#if skill?.variables}
          <VarForm variables={skill.variables} bind:values={edit.env} />
        {/if}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="e-cpu">CPU limit (%, 0 = unlimited)</label>
            <input id="e-cpu" class="input" type="number" bind:value={edit.cpu_percent} />
          </div>
          <div>
            <label class="label" for="e-mem">RAM limit (MB, 0 = unlimited)</label>
            <input id="e-mem" class="input" type="number" bind:value={edit.memory_mb} />
          </div>
        </div>
        <div>
          <label class="label" for="e-bm">BattleMetrics server ID (optional)</label>
          <input id="e-bm" class="input" placeholder="e.g. 12345678" bind:value={edit.bm_server_id} />
          <p class="text-xs text-muted mt-1">
            Find your server on battlemetrics.com — the number in its URL. Shows a live
            online/players badge at the top of this page.
          </p>
        </div>
        <div>
          <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
            <input type="checkbox" bind:checked={edit.auto_forward} />
            Open firewall ports automatically (UPnP / UniFi)
          </label>
          <p class="text-xs text-muted mt-1">
            On by default. Turn off to keep this server LAN-only — its ports won't be forwarded
            on the router when it starts. Takes effect on the next start.
          </p>
        </div>
        <div>
          <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
            <input type="checkbox" bind:checked={edit.autostart} />
            Start automatically after a reboot
          </label>
          <p class="text-xs text-muted mt-1">
            On by default. If this server was running when the host/panel restarts, it's brought
            back up automatically. Turn off to leave it stopped after a reboot.
          </p>
        </div>
        <div>
          <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
            <input type="checkbox" bind:checked={edit.status_public} />
            Show on the public status page
          </label>
          <p class="text-xs text-muted mt-1">
            Off by default. When on, this server's name, game and online/players state appear on the
            public <code>/status</code> page (no login). Enable the page under Settings → Status page.
          </p>
        </div>
        {#if server.ports?.web}
          <div>
            <label class="label" for="e-sub">Subdomain (optional)</label>
            <input id="e-sub" class="input" placeholder="e.g. notes" bind:value={edit.subdomain} />
            <p class="text-xs text-muted mt-1">
              Routes <code>{edit.subdomain || "sub"}.&lt;your base domain&gt;</code> to this app via
              Nginx Proxy Manager or Cloudflare Tunnel (configure under Settings → Network). The route is
              created on start and removed on stop. You can also enter a full custom domain. Leave blank to disable.
            </p>
          </div>
        {/if}
        {#if $user?.role === "admin"}
          <div>
            <span class="label">Host mounts (admin)</span>
            <p class="text-xs text-muted mt-1 mb-2">
              Mount a folder from the panel host into this container — e.g. a media library
              <code>/mnt/mediaserver → /media</code> for Jellyfin. Read-only by default (tick
              <b>Write</b> only if the app must write there). Applies on the next start.
            </p>
            <div class="space-y-2">
              {#each hostMounts as m, i}
                <div class="flex flex-wrap items-center gap-2">
                  <input class="input flex-1 min-w-[10rem] font-mono text-xs" placeholder="/mnt/mediaserver" bind:value={m.host} />
                  <span class="text-muted">→</span>
                  <input class="input flex-1 min-w-[8rem] font-mono text-xs" placeholder="/media" bind:value={m.container} />
                  <label class="inline-flex items-center gap-1 text-xs text-muted" title="Allow the container to write to this path">
                    <input type="checkbox" bind:checked={m.rw} /> Write
                  </label>
                  <button class="btn-ghost px-2 py-1 text-danger" aria-label="Remove mount" onclick={() => removeMount(i)}>✕</button>
                </div>
              {/each}
              <button class="btn-ghost text-sm" onclick={addMount}>+ Add host mount</button>
            </div>
          </div>
        {/if}
        <div class="card bg-warn/10 border-warn/40 text-warn text-xs px-3 py-2">
          Changes apply on the next <b>restart</b>. Values written into config files at install time
          (e.g. RCON password, world seed) need a <b>Reinstall</b> to fully apply — back up your
          world first, as reinstall can regenerate config.
        </div>
        <div class="flex gap-2">
          <button class="btn-primary" onclick={saveEdit} disabled={savingEdit}>
            {savingEdit ? "Saving…" : "Save changes"}
          </button>
          <button class="btn-ghost" onclick={() => runInstall(true)}>Update / Reinstall</button>
        </div>
      </div>
    {/if}

    {#if $user?.role === "admin"}
      <div class="max-w-2xl mt-10 pt-6 border-t border-border">
        <h3 class="text-lg font-semibold mb-1">Delegated users</h3>
        <p class="text-muted text-sm mb-4">
          Give specific non-admin users access to <b>this server only</b>. Permissions here apply
          just to {server?.name}; the user's access to other servers is unaffected.
        </p>

        {#if delegates.length === 0}
          <div class="text-muted text-sm mb-3">No users are delegated to this server yet.</div>
        {/if}

        <div class="space-y-3">
          {#each delegates as d (d.user_id)}
            <div class="card p-3">
              <div class="flex items-center justify-between mb-2">
                <div class="font-medium">{d.username}</div>
                <button class="btn-ghost px-2 py-1 text-danger" onclick={() => removeDelegate(d.user_id)}>
                  Remove
                </button>
              </div>
              <div class="flex flex-wrap gap-x-4 gap-y-1.5">
                {#each DELEGATE_PERMS as [perm, label]}
                  <label class="inline-flex items-center gap-1.5 text-sm cursor-pointer">
                    <input
                      type="checkbox"
                      checked={d.perms.includes(perm)}
                      onchange={() => toggleDelegatePerm(d.user_id, perm)}
                    />
                    {label}
                  </label>
                {/each}
              </div>
            </div>
          {/each}
        </div>

        <div class="flex items-center gap-2 mt-4">
          <select
            class="input max-w-xs"
            disabled={undelegatedUsers.length === 0}
            onchange={(e) => {
              addDelegate(e.target.value);
              e.target.value = "";
            }}
          >
            <option value="">
              {undelegatedUsers.length ? "+ Add user…" : "No more users to add"}
            </option>
            {#each undelegatedUsers as u}
              <option value={u.id}>{u.username}</option>
            {/each}
          </select>
          <button class="btn-primary" onclick={saveDelegates} disabled={savingDelegates}>
            {savingDelegates ? "Saving…" : "Save delegated access"}
          </button>
        </div>
        {#if allUsers.length === 0}
          <p class="text-muted text-xs mt-2">
            Create non-admin users on the <a href="#/users" class="underline">Users</a> page to delegate access.
          </p>
        {/if}
      </div>
    {/if}
  {:else if tab === "anticheat"}
    {#if skill?.anticheat}
      <div class="space-y-3">
        {#if skill.anticheat.antixray}
          <div class="card p-4">
            <div class="font-medium flex items-center gap-2">
              🛡️ Anti-xray
              <span class="badge {skill.anticheat.antixray.supported ? 'bg-accent2/20 text-accent' : 'bg-border text-muted'}">
                {skill.anticheat.antixray.supported ? "supported" : "n/a"}
              </span>
            </div>
            <p class="text-sm text-muted mt-1">{skill.anticheat.antixray.config_hint}</p>
            <p class="text-xs text-muted mt-2">
              Server-side anti-xray hides ore data, so xray clients see nothing — no detection or
              bans needed. Configure it in the file editor.
            </p>
            <button class="btn-ghost mt-2" onclick={() => (tab = "files")}>Open file editor →</button>
          </div>
        {/if}
        {#if skill.anticheat.battleye}
          <div class="card p-4">
            <div class="font-medium flex items-center gap-2">
              🛡️ BattlEye
              <span class="badge bg-accent2/20 text-accent">supported</span>
            </div>
            <p class="text-sm text-muted mt-1">{skill.anticheat.battleye.config_hint}</p>
          </div>
        {/if}
        {#if skill.anticheat.plugins_recommended?.length}
          <div class="card p-4">
            <div class="font-medium">Recommended anti-cheat</div>
            <div class="flex flex-wrap gap-2 mt-2">
              {#each skill.anticheat.plugins_recommended as p}
                <span class="badge bg-panel2 text-text border border-border">{p}</span>
              {/each}
            </div>
          </div>
        {/if}
        <div class="card p-4 text-sm text-muted">
          Caught a cheater? Use centralized <a href="#/bans" class="text-accent hover:underline">Bans</a>
          to ban them here or across every server at once.
        </div>
      </div>
    {:else}
      <div class="card p-6 text-center text-muted">
        This game defines no server-side anti-cheat hooks. Client-side anti-cheat (EAC/VAC/BattlEye)
        is shipped by the game itself.
      </div>
    {/if}
  {:else if tab === "backups"}
    <div class="flex flex-wrap items-end gap-2 mb-4">
      <div>
        <label class="label" for="bt">Target</label>
        <select id="bt" class="input" bind:value={selectedTarget}
          title="Where the backup archive is stored (local disk or a remote target configured in Settings).">
          {#if backupTargets.length === 0}
            <option value="">No targets — add one in Settings</option>
          {/if}
          {#each backupTargets as t}
            <option value={t.id}>{t.name} ({t.type})</option>
          {/each}
        </select>
      </div>
      <button class="btn-primary" onclick={runBackup} disabled={backupBusy || !selectedTarget}
        title="Create a backup archive of this server's data now, to the selected target.">
        {backupBusy ? "Starting…" : "Back up now"}
      </button>
      <button class="btn-ghost" onclick={loadBackups} title="Reload the backup list and target list.">Refresh</button>
    </div>

    <div class="card divide-y divide-border">
      {#if backups.length === 0}
        <div class="p-4 text-muted text-sm">No backups yet.</div>
      {/if}
      {#each backups as b}
        <div class="flex items-center gap-3 px-4 py-3">
          <div class="flex-1 min-w-0">
            <div class="text-sm truncate">{fmtDate(b.created_at)}</div>
            <div class="text-xs text-muted truncate">
              <span class="font-mono">{backupName(b)}</span> ·
              {fmtSize(b.size_bytes)} ·
              <span
                class={b.status === "done"
                  ? "text-accent"
                  : b.status === "error"
                    ? "text-danger"
                    : "text-warn"}>{b.status}</span
              >
              {#if b.error}— {b.error}{/if}
              {#if b.verify_ok === 1}<span class="text-accent"> · ✓ verified</span>
              {:else if b.verify_ok === 0}<span class="text-danger"> · ✗ corrupt</span>{/if}
            </div>
          </div>
          {#if b.status === "done"}
            <button class="btn-ghost" disabled={verifyingId === b.id} onclick={() => verifyBackup(b)}
              title="Download and check this backup decompresses cleanly — confirm it's restorable before you ever need it. Reads the archive but changes nothing.">
              {verifyingId === b.id ? "Verifying…" : "Verify"}</button>
            <button class="btn-ghost" onclick={() => restoreBackup(b)}
              title="Stop the server and overwrite its files with this backup. Current data is replaced — you'll be asked to confirm.">Restore</button>
          {/if}
          <button class="btn-danger" onclick={() => deleteBackup(b)}
            title="Delete this backup archive. Does not affect the running server.">Delete</button>
        </div>
      {/each}
    </div>
  {:else if tab === "mods"}
    <div class="flex items-center justify-between gap-2 mb-1">
      <h3 class="text-lg font-semibold">🧩 Workshop mods</h3>
      <button class="btn-ghost text-xs" onclick={loadMods} disabled={modsBusy}>Refresh</button>
    </div>
    <p class="text-muted text-sm mb-4">
      The mods this server loads, in order. Yggdrasil checks each one against the Steam Workshop and
      against what actually downloaded to disk — so a mod that was removed upstream (or failed to
      download) shows up here instead of silently dropping out and blocking players from joining.
      <b>Editing the list takes effect on the next Update/Reinstall.</b>
    </p>

    {#if !modStatus}
      <div class="text-muted text-sm">Loading…</div>
    {:else if !modStatus.mods.length && !modStatus.orphans.length}
      <div class="card text-sm p-3 text-muted">
        No Workshop mods configured. Add IDs (semicolon-separated, in load order) under
        <button class="text-accent hover:underline" onclick={() => { tab = "settings"; openEdit(); loadDelegation(); }}>Settings → MODS</button>,
        then Update/Reinstall.
      </div>
    {:else}
      {#if modStatus.issues > 0}
        <div class="card border-warn/40 bg-warn/10 p-3 mb-4 flex items-start justify-between gap-3">
          <div class="text-sm text-warn">
            <b>{modStatus.issues} of {modStatus.mods.length} mod(s) are missing or were removed from the Workshop.</b>
            DayZ starts without them, which often stops players from joining (a missing dependency
            breaks the mission). Remove the dead ones, then press <b>Update/Reinstall</b>.
          </div>
          <button class="btn-ghost text-xs shrink-0 text-warn" onclick={pruneBroken} disabled={modsBusy}>
            Remove broken
          </button>
        </div>
      {:else if modStatus.mods.length}
        <div class="card border-accent2/40 bg-accent2/5 p-3 mb-4 text-sm text-accent">
          ✓ All {modStatus.mods.length} mod(s) are installed and still present on the Workshop.
        </div>
      {/if}

      {#if modStatus.mods.length}
        <div class="card divide-y divide-border mb-5">
          {#each modStatus.mods as m, i}
            <div class="flex items-center gap-3 p-2.5">
              <span class="text-muted text-xs w-5 text-right shrink-0">{i + 1}</span>
              <div class="min-w-0 flex-1">
                <a class="text-accent hover:underline font-medium truncate block" href={m.url} target="_blank" rel="noopener">{m.name}</a>
                <span class="text-muted text-xs font-mono">{m.id}</span>
              </div>
              {#if m.installed}
                <span class="text-xs px-2 py-0.5 rounded bg-accent2/15 text-accent shrink-0">on disk</span>
              {:else}
                <span class="text-xs px-2 py-0.5 rounded bg-warn/15 text-warn shrink-0">not downloaded</span>
              {/if}
              {#if m.workshop === "removed"}
                <span class="text-xs px-2 py-0.5 rounded bg-danger/15 text-danger shrink-0">removed from Workshop</span>
              {:else if m.workshop === "ok"}
                <span class="text-xs px-2 py-0.5 rounded bg-panel2 text-muted shrink-0">Workshop ✓</span>
              {:else}
                <span class="text-xs px-2 py-0.5 rounded bg-panel2 text-muted shrink-0">Workshop ?</span>
              {/if}
              <button class="btn-ghost text-xs shrink-0" onclick={() => removeMod(m.id)} disabled={modsBusy}>Remove</button>
            </div>
          {/each}
        </div>
      {/if}

      {#if modStatus.orphans.length}
        <h4 class="text-sm font-semibold mb-1">Downloaded but not in the load order</h4>
        <p class="text-muted text-xs mb-2">
          These <span class="font-mono">@mod</span> folders are on disk but not in MODS, so the server
          doesn't load them. Add one to the list (and Update/Reinstall) or ignore it.
        </p>
        <div class="card divide-y divide-border">
          {#each modStatus.orphans as m}
            <div class="flex items-center gap-3 p-2.5">
              <div class="min-w-0 flex-1">
                <a class="text-accent hover:underline font-medium truncate block" href={m.url} target="_blank" rel="noopener">{m.name}</a>
                <span class="text-muted text-xs font-mono">{m.id}</span>
              </div>
              {#if m.workshop === "removed"}
                <span class="text-xs px-2 py-0.5 rounded bg-danger/15 text-danger shrink-0">removed from Workshop</span>
              {/if}
              <button class="btn-ghost text-xs shrink-0" onclick={() => addOrphan(m.id)} disabled={modsBusy}>Add to list</button>
            </div>
          {/each}
        </div>
      {/if}
    {/if}
  {:else if tab === "norn"}
    <div class="flex items-center gap-2 mb-1">
      <h3 class="text-lg font-semibold">🧵 Norn — loot economy</h3>
    </div>
    <p class="text-muted text-sm mb-4">
      Controls how long dropped items stay in the world before they despawn. Modded loot often
      vanishes too fast — set a floor below and nothing will despawn quicker than that.
      <b>Changes apply on the next restart.</b>
    </p>

    {#if !economy}
      <div class="text-muted text-sm">Loading…</div>
    {:else if !economy.found}
      <div class="card border-warn/40 bg-warn/10 text-warn text-sm p-3">
        No mission economy files found for <span class="font-mono">{economy.mission}</span>. Install
        and start the server once so DayZ writes its <span class="font-mono">mpmissions</span> files.
      </div>
    {:else}
      <!-- Saved / persistence -->
      {#if economy.saved && (economy.saved.min_lifetime_hours || (economy.saved.registered && economy.saved.registered.length) || (economy.saved.globals && Object.keys(economy.saved.globals).length))}
        <div class="card border-accent2/40 bg-accent2/5 p-3 mb-5 flex items-center justify-between gap-3">
          <div class="text-sm">
            <span class="text-accent font-medium">✓ Saved & auto-re-applied after updates.</span>
            <span class="text-muted">
              {#if economy.saved.min_lifetime_hours}floor {economy.saved.min_lifetime_hours}h{/if}{#if economy.saved.registered && economy.saved.registered.length} · {economy.saved.registered.length} registration(s){/if}{#if economy.saved.globals && Object.keys(economy.saved.globals).length} · {Object.keys(economy.saved.globals).length} globals{/if}.
              A restart keeps your settings; Update/Reinstall regenerates vanilla files and Norn re-applies these automatically.
            </span>
          </div>
          <button class="btn-ghost text-xs shrink-0" onclick={resetNorn} disabled={nornBusy}>Reset</button>
        </div>
      {/if}

      <!-- Overview -->
      <div class="grid grid-cols-2 sm:grid-cols-3 gap-3 mb-5">
        <div class="card p-3">
          <div class="text-muted text-xs uppercase tracking-wide">Mission</div>
          <div class="text-sm font-mono mt-1 truncate">{economy.mission}</div>
        </div>
        <div class="card p-3">
          <div class="text-muted text-xs uppercase tracking-wide">Item types</div>
          <div class="text-2xl font-semibold mt-1">{economy.total_items}</div>
        </div>
        <div class="card p-3 {economy.min_lifetime >= 0 && economy.min_lifetime < 3600 ? 'border-warn/50' : ''}">
          <div class="text-muted text-xs uppercase tracking-wide">Shortest lifetime</div>
          <div class="text-2xl font-semibold mt-1">{fmtDur(economy.min_lifetime)}</div>
          {#if economy.min_lifetime >= 0 && economy.min_lifetime < 3600}
            <div class="text-warn text-xs mt-0.5">some items despawn fast</div>
          {/if}
        </div>
      </div>

      <!-- Unregistered modded types -->
      {#if economy.unregistered && economy.unregistered.length}
        <div class="card border-warn/40 bg-warn/10 p-4 mb-5">
          <h4 class="font-semibold mb-1 text-warn">⚠ {economy.unregistered.length} modded loot file(s) not in the economy</h4>
          <p class="text-muted text-xs mb-2">
            These <span class="font-mono">types.xml</span> files exist in the mission but aren't registered in
            <span class="font-mono">cfgeconomycore.xml</span>, so their items don't spawn or get managed. Register
            them so the loot works (and the lifetime floor applies to them too).
          </p>
          <ul class="text-xs font-mono text-muted mb-3 space-y-0.5">
            {#each economy.unregistered as u}<li class="truncate">• {u}</li>{/each}
          </ul>
          <button class="btn-primary" onclick={registerTypes} disabled={nornBusy}>
            {nornBusy ? "Working…" : "Register all in economy"}
          </button>
        </div>
      {/if}

      <!-- Loot from installed mods -->
      {#if modLoot && modLoot.mods && modLoot.mods.length}
        <div class="card p-4 mb-5">
          <h4 class="font-semibold mb-1">Loot from installed mods</h4>
          <p class="text-muted text-xs mb-3">
            <span class="font-mono">types.xml</span> files shipped inside your mods. Import one to copy it
            into the mission and register it, so its loot spawns and is covered by the lifetime floor.
          </p>
          <div class="space-y-3">
            {#each modLoot.mods as m}
              <div class="border border-border rounded-md p-3">
                <div class="flex items-center gap-2 mb-1">
                  <span class="font-medium">{m.name}</span>
                  {#if m.expansion}<span class="badge bg-warn/20 text-warn">manages own economy</span>{/if}
                </div>
                {#if m.expansion}
                  <p class="text-warn text-xs mb-2">DayZ Expansion injects its own loot — you usually don't need to import these.</p>
                {/if}
                {#each m.files as f}
                  <div class="flex items-center justify-between gap-3 py-1">
                    <span class="text-xs font-mono truncate">{f.path} <span class="text-muted">· {f.items} items</span></span>
                    {#if f.imported}
                      <span class="badge bg-accent2/20 text-accent shrink-0">imported</span>
                    {:else}
                      <button class="btn-ghost px-2 py-1 text-xs shrink-0" onclick={() => importModTypes(f.path)} disabled={nornBusy}>Import + register</button>
                    {/if}
                  </div>
                {/each}
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Minimum lifetime floor -->
      <div class="card p-4 mb-5">
        <h4 class="font-semibold mb-1">Minimum lifetime floor</h4>
        <p class="text-muted text-xs mb-3">
          Raise every item whose lifetime is below this up to it — across vanilla <span class="font-mono">types.xml</span>
          and any modded types files registered in the economy. The fastest, most reliable fix for
          "modded items despawn too quickly".
        </p>
        <div class="flex flex-wrap items-end gap-2">
          <div>
            <label class="label" for="norn-h">No item despawns faster than (hours)</label>
            <input id="norn-h" class="input w-40" type="number" min="0.1" step="0.5" bind:value={minHours} />
          </div>
          <button class="btn-primary" onclick={applyMinLifetime} disabled={nornBusy}>
            {nornBusy ? "Working…" : "Apply floor"}
          </button>
        </div>
      </div>

      <!-- Globals cleanup timers -->
      <div class="card p-4 mb-5">
        <h4 class="font-semibold mb-1">Cleanup timers (globals.xml)</h4>
        <p class="text-muted text-xs mb-3">
          Fallback despawn timers in seconds. <span class="font-mono">CleanupLifetimeDefault</span> is
          used for items with no explicit lifetime — bump it if loot still disappears.
        </p>
        <div class="grid grid-cols-2 sm:grid-cols-3 gap-3">
          {#each Object.keys(globalsEdit) as k}
            <div>
              <label class="label text-xs" for={`g-${k}`}>{k.replace("CleanupLifetime", "")}</label>
              <input id={`g-${k}`} class="input" type="number" min="0" bind:value={globalsEdit[k]} />
            </div>
          {/each}
        </div>
        <button class="btn-ghost mt-3" onclick={saveGlobals} disabled={nornBusy}>Save timers</button>
      </div>

      <!-- Per-file breakdown -->
      <div class="card divide-y divide-border">
        {#each economy.files as f}
          <div class="flex items-center justify-between px-4 py-2 text-sm">
            <span class="font-mono truncate">{f.path}{#if f.modded}<span class="badge bg-accent2/20 text-accent ml-2">modded</span>{/if}</span>
            <span class="text-muted shrink-0">{f.items} items · min {fmtDur(f.min_lifetime)}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
{/if}
