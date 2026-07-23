<script>
  import { onMount, onDestroy } from "svelte";
  import { api, wsURL, getToken } from "../lib/api.js";
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

  // Copy takes what's on screen; Download goes to the server for the real thing.
  //
  // They're different on purpose. The console tab only holds what arrived since
  // you opened it, so copying it is honest about being a snapshot of the view.
  // The container's actual log lives in Docker and reaches further back, which is
  // what you want when something crashed before you looked.
  async function copyLog(buf, what) {
    const text = buf.join("\n");
    if (!text) return toast("Nothing to copy yet", "warn");
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
      } else {
        // The panel is commonly reached over plain http on a LAN, where the async
        // clipboard API isn't available.
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      toast(`${what} copied — ${buf.length} line${buf.length === 1 ? "" : "s"}`, "success");
    } catch (e) {
      toast("Could not copy: " + e.message, "error");
    }
  }

  let showLogExport = $state(false);
  // Ranges are relative to now, and there's no date picker, because the log
  // doesn't go back that far: Yggdrasil recreates the container on every restart,
  // so Docker's log for it starts at the current container's creation. Offering
  // "last Tuesday" would reliably return an empty file.
  const logRanges = [
    { id: "tail-200", label: "Last 200 lines", q: "tail=200" },
    { id: "tail-2000", label: "Last 2000 lines", q: "tail=2000" },
    { id: "15m", label: "Last 15 minutes", q: "since=15m" },
    { id: "1h", label: "Last hour", q: "since=1h" },
    { id: "24h", label: "Last 24 hours", q: "since=24h" },
    { id: "all", label: "Everything this container has", q: "tail=all" },
  ];
  let logRange = $state("tail-2000");
  let logTimestamps = $state(true);

  let downloadingLog = $state(false);

  async function downloadLog(kind) {
    // A download needs the token on the URL: it's a browser navigation, not a
    // fetch, so it can't carry the Authorization header. The access log redacts
    // the token query parameter.
    const tok = encodeURIComponent(getToken());
    const base =
      kind === "install"
        ? `/api/servers/${id}/install/log/export?`
        : `/api/servers/${id}/logs/export?${(logRanges.find((x) => x.id === logRange) ?? logRanges[1]).q}` +
          `&timestamps=${logTimestamps}&`;
    const url = `${base}token=${tok}`;

    downloadingLog = true;
    try {
      // Ask for a token line first. Navigating straight to the real URL would be
      // fine on success, but on failure the browser leaves the panel and lands on
      // a page of raw JSON — losing your place to tell you the server has no
      // container. Fetching the whole log instead and turning it into a blob
      // would handle the error, but would also pull a large log through memory,
      // which is exactly what streaming it avoids. So: check cheaply, then hand
      // the real URL to the browser.
      const probe = await fetch(kind === "install" ? url : `${base}token=${tok}&tail=1`);
      if (!probe.ok) {
        let msg = probe.statusText;
        try {
          msg = (await probe.json()).error || msg;
        } catch {
          /* not JSON; the status text will do */
        }
        toast(msg, "warn");
        return;
      }
      // Content-Disposition names the file, and the body streams to disk.
      window.location.assign(url);
      showLogExport = false;
    } catch (e) {
      toast("Could not reach the panel: " + e.message, "error");
    } finally {
      downloadingLog = false;
    }
  }
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

  async function banPlayer(p) {
    if (!confirm(`Ban ${p.name}?\n\nThey're added to ban.txt and refused on their next join. A player already in-game can't be kicked without RCon (which DayZ-Linux lacks), so they stay until they disconnect.`)) return;
    const reason = prompt(`Ban ${p.name}? Optional reason (for the audit log):`, "");
    if (reason === null) return;
    playersBusy = true;
    try {
      const r = await api.post(`/servers/${id}/players/ban`, { id: p.id || p.guid, name: p.name, reason });
      toast(r.message || `Banned ${p.name}`, "success");
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

  // Mods tab (Modrinth mod/plugin manager for Minecraft) — search filtered to the
  // server's loader + version, one-click install with dependencies, update, remove.
  let modsInstalled = $state([]);
  let modsFolder = $state("");
  let modsLoading = $state(false);
  // How many installed mods have a newer compatible build on Modrinth — drives the
  // update badge on the Mods tab (the backend already flags each mod's update_available).
  const modUpdateCount = $derived(modsInstalled.filter((m) => m.update_available).length);
  let modQuery = $state("");
  let modResults = $state([]);
  let modLoader = $state("");
  let modGameVersion = $state("");
  let modSearching = $state(false);
  let modBusy = $state(""); // project id / filename currently acting on
  let modSearchTimer = null;

  async function loadInstalledMods() {
    modsLoading = true;
    try {
      const r = await api.get(`/servers/${id}/mods`);
      modsInstalled = r.mods || [];
      modsFolder = r.folder || "";
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modsLoading = false;
    }
  }
  async function searchMods() {
    if (!modQuery.trim()) {
      modResults = [];
      return;
    }
    modSearching = true;
    try {
      const r = await api.get(`/servers/${id}/mods/search?q=${encodeURIComponent(modQuery)}`);
      modResults = r.results || [];
      modLoader = r.loader || "";
      modGameVersion = r.game_version || "";
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modSearching = false;
    }
  }
  function onModQuery() {
    clearTimeout(modSearchTimer);
    modSearchTimer = setTimeout(searchMods, 350); // debounce
  }
  const installedSlugs = $derived(new Set(modsInstalled.map((m) => m.slug).filter(Boolean)));
  // Mod icons live on Modrinth's CDN, which the strict CSP (img-src 'self') blocks
  // — route them through the panel so they load without leaking the viewer's IP.
  const modIcon = (url) => (url ? `/api/mods/icon?url=${encodeURIComponent(url)}` : "");
  async function installMod(project) {
    modBusy = project;
    try {
      const r = await api.post(`/servers/${id}/mods/install`, { project });
      toast(`Installed: ${r.installed.join(", ")} — restart the server to apply`, "success");
      await loadInstalledMods();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modBusy = "";
    }
  }
  async function updateMod(file) {
    modBusy = file;
    try {
      await api.post(`/servers/${id}/mods/update?file=${encodeURIComponent(file)}`);
      toast("Updated — restart the server to apply", "success");
      await loadInstalledMods();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modBusy = "";
    }
  }
  async function removeMcMod(file) {
    if (!confirm(`Remove ${file}?`)) return;
    modBusy = file;
    try {
      await api.del(`/servers/${id}/mods?file=${encodeURIComponent(file)}`);
      toast("Removed — restart the server to apply", "info");
      await loadInstalledMods();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modBusy = "";
    }
  }
  $effect(() => {
    if (tab === "mcmods" && server?.mods_supported) loadInstalledMods();
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

  // Stability: recent unexpected exits (crashes / external stops). Shown only when
  // there's something to show, so a healthy server stays uncluttered.
  let crashList = $state([]);
  async function loadCrashes() {
    try {
      crashList = await api.get(`/servers/${id}/crashes`);
    } catch {
      crashList = [];
    }
  }
  // 0 = clean stop, 143 = SIGTERM, 130 = SIGINT — graceful terminations, not crashes.
  const isFault = (code) => code !== 0 && code !== 143 && code !== 130;
  const faultCount = $derived(crashList.filter((c) => isFault(c.exit_code)).length);
  async function clearCrashes() {
    try {
      await api.del(`/servers/${id}/crashes`);
      crashList = [];
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // Minecraft server-jar update check (Paper/Purpur builds).
  let jarStatus = $state(null);
  async function loadJarUpdate() {
    jarStatus = await api.get(`/servers/${id}/jar-update`).catch(() => null);
  }

  // Kvasir: recent proactive-AI reactions (explanations + proposed/applied fixes).
  // Shown only when Kvasir has actually reacted, so it stays hidden until in use.
  let kvasirEvents = $state([]);
  async function loadKvasirEvents() {
    try {
      kvasirEvents = await api.get(`/servers/${id}/kvasir-events`);
    } catch {
      kvasirEvents = [];
    }
  }
  async function clearKvasirEvents() {
    try {
      await api.del(`/servers/${id}/kvasir-events`);
      kvasirEvents = [];
    } catch (e) {
      toast(e.message, "error");
    }
  }
  // Compact "X ago" for a crash timestamp (SQLite UTC "YYYY-MM-DD HH:MM:SS").
  function crashAgo(iso) {
    if (!iso) return "";
    const t = new Date(iso.replace(" ", "T") + (iso.endsWith("Z") ? "" : "Z")).getTime();
    if (isNaN(t)) return "";
    const s = (Date.now() - t) / 1000;
    if (s < 60) return "just now";
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
  }

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
  let autoRestart = $state({ enabled: false, every_hours: 6, anchor_hour: 0, warn: true, backup_first: false, target_id: "" });
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

  // Spell out the hours the restart will actually land on. The cron behind this
  // is `0 <anchor>-23/<N>`, which stops at 23 rather than wrapping past midnight
  // — so when N doesn't divide 24 the last gap of the day is short. Better to
  // show the real times than to let someone infer an even cycle that isn't.
  const autoRestartTimes = $derived.by(() => {
    const n = autoRestart.every_hours,
      a = autoRestart.anchor_hour ?? 0;
    if (n >= 24) return hh(a) + " daily";
    const times = [];
    for (let h = a; h <= 23; h += n) times.push(hh(h));
    return times.join(", ");
  });

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
      toast(
        enabled
          ? `Auto-restart on — ${autoRestart.every_hours >= 24 ? `daily at ${hh(autoRestart.anchor_hour)}` : `every ${autoRestart.every_hours}h from ${hh(autoRestart.anchor_hour)}`}`
          : "Auto-restart off",
        "success",
      );
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

  // Restore is destructive (overwrites the live world/data), so it goes through a
  // deliberate modal that makes you type the server name — not a one-tap confirm.
  let restoreTarget = $state(null); // the backup being restored
  let restoreConfirm = $state(""); // must match the server name to enable Restore
  let restoring = $state(false);
  function restoreBackup(b) {
    restoreTarget = b;
    restoreConfirm = "";
  }
  async function doRestore() {
    if (!restoreTarget || restoreConfirm !== server.name) return;
    restoring = true;
    try {
      await api.post(`/backups/${restoreTarget.id}/restore`);
      toast("Restored — the server was stopped; start it when ready.", "success");
      restoreTarget = null;
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      restoring = false;
    }
  }
  function downloadBackup(b) {
    // Cookie-auth same-origin download; open in a hidden anchor.
    const a = document.createElement("a");
    a.href = `/api/backups/${b.id}/download`;
    a.rel = "noopener";
    a.click();
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
      ...(server?.mods_supported && can("server.files") ? [["mcmods", "Mods"]] : []),
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

  // --- DayZ mod manager: paste-by-id, Workshop search, drag-reorder, Kvasir ---
  let modPasteId = $state("");
  async function addModById(mid) {
    const wsid = String(mid || "").trim();
    if (!wsid) return;
    modsBusy = true;
    try {
      const r = await api.post(`/servers/${id}/dayz/mods`, { id: wsid });
      toast(`Added ${r.name || wsid} — press Update/Reinstall to download it`, "success");
      modPasteId = "";
      await Promise.all([loadServer(), loadMods()]);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modsBusy = false;
    }
  }
  async function removeModById(mid) {
    modsBusy = true;
    try {
      await api.del(`/servers/${id}/dayz/mods?id=${encodeURIComponent(mid)}`);
      await Promise.all([loadServer(), loadMods()]);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modsBusy = false;
    }
  }
  const configuredIds = $derived(new Set((modStatus?.mods || []).map((m) => m.id)));

  // Workshop search (needs a Steam Web API key configured in Settings).
  let dzQuery = $state("");
  let dzResults = $state([]);
  let dzSearching = $state(false);
  let modNeedsKey = $state(false);
  async function dzSearchMods() {
    const q = dzQuery.trim();
    if (!q) {
      dzResults = [];
      return;
    }
    dzSearching = true;
    try {
      const r = await api.get(`/servers/${id}/dayz/mods/search?q=${encodeURIComponent(q)}`);
      dzResults = r.results || [];
      modNeedsKey = !!r.needs_key;
    } catch (e) {
      toast(e.message, "error");
    } finally {
      dzSearching = false;
    }
  }

  // Drag to reorder the load order (order matters in DayZ).
  let modDrag = $state(-1);
  function modDragOver(e, i) {
    e.preventDefault();
    if (modDrag < 0 || modDrag === i) return;
    const arr = modStatus.mods.slice();
    const [moved] = arr.splice(modDrag, 1);
    arr.splice(i, 0, moved);
    modStatus.mods = arr;
    modDrag = i;
  }
  async function saveOrder() {
    modDrag = -1;
    modsBusy = true;
    try {
      await api.put(`/servers/${id}/dayz/mods/order`, { ids: (modStatus?.mods || []).map((m) => m.id) });
      toast("Load order saved — applies on the next restart", "success");
      await loadServer();
    } catch (e) {
      toast(e.message, "error");
      await loadMods();
    } finally {
      modsBusy = false;
    }
  }

  // Kvasir: suggest missing dependencies + a sane load order.
  let modSuggest = $state(null);
  let modSuggesting = $state(false);
  async function suggestMods() {
    modSuggesting = true;
    try {
      modSuggest = await api.post(`/servers/${id}/dayz/mods/suggest`, {});
    } catch (e) {
      toast(e.message, "error");
      modSuggest = null;
    } finally {
      modSuggesting = false;
    }
  }
  async function applySuggestedOrder() {
    if (!modSuggest?.recommended_order?.length) return;
    modsBusy = true;
    try {
      await api.put(`/servers/${id}/dayz/mods/order`, { ids: modSuggest.recommended_order });
      toast("Applied Kvasir's load order — applies on the next restart", "success");
      modSuggest = null;
      await Promise.all([loadServer(), loadMods()]);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      modsBusy = false;
    }
  }

  let skill = $state(null); // parsed gameskill (for anti-cheat surface + edit form)

  // Edit settings
  let edit = $state(null); // { name, env, cpu_percent, memory_mb }
  let savingEdit = $state(false);
  let realms = $state([]); // groups, for the admin-only Group picker in the editor
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
      cpu_alarm_pct: server.cpu_alarm_pct || 0,
      mem_alarm_mb: server.mem_alarm_mb || 0,
      disk_alarm_mb: server.disk_alarm_mb || 0,
      tags: (server.tags || []).join(", "),
      subdomain: server.subdomain || "",
      realm_id: server.realm_id || "",
    };
    hostMounts = (server.host_mounts || []).map((m) => ({ host: m.host, container: m.container, rw: !!m.rw }));
    // Groups are admin-only to change; load them lazily when the editor opens.
    if ($user?.role === "admin" && !realms.length) api.get("/realms").then((r) => (realms = r)).catch(() => {});
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
        cpu_alarm_pct: Number(edit.cpu_alarm_pct) || 0,
        mem_alarm_mb: Number(edit.mem_alarm_mb) || 0,
        disk_alarm_mb: Number(edit.disk_alarm_mb) || 0,
        tags: (edit.tags || "").split(","),
        subdomain: edit.subdomain || "",
      };
      // Host mounts are admin-only; only send the field when the caller is an admin
      // (the backend rejects it otherwise), and drop blank rows.
      if ($user?.role === "admin") {
        payload.host_mounts = hostMounts
          .filter((m) => m.host.trim() && m.container.trim())
          .map((m) => ({ host: m.host.trim(), container: m.container.trim(), rw: !!m.rw }));
        payload.realm_id = edit.realm_id || ""; // reassign group (admin-only in the backend)
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

  // Shared free-text notes for the admin team (per server).
  let editingNotes = $state(false);
  let notesDraft = $state("");
  // Whether this note is markdown is stored per server rather than being a view
  // toggle: it's a property of how the note was written. Off by default, so an
  // existing note keeps reading exactly as it does now and nobody's asterisks
  // silently turn into bullets.
  let notesMdDraft = $state(false);
  let savingNotes = $state(false);
  function startEditNotes() {
    notesDraft = server.notes || "";
    notesMdDraft = !!server.notes_markdown;
    editingNotes = true;
  }
  async function saveNotes() {
    savingNotes = true;
    try {
      await api.put(`/servers/${id}`, { notes: notesDraft, notes_markdown: notesMdDraft });
      // Re-fetch rather than patch locally: the rendered HTML is built by the
      // server, so anything assembled here would be showing something it didn't
      // produce — and it's the escaping that makes it safe to inject.
      await loadServer();
      editingNotes = false;
      toast("Notes saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingNotes = false;
    }
  }

  let cloning = $state(false);
  // Download this server as a portable bundle to import on another panel. The
  // bundle holds decrypted secrets, so it's admin-only and worth a heads-up.
  let exporting = $state(false);
  let exportedBytes = $state(0);
  async function exportServer() {
    if (exporting) return;
    exporting = true;
    exportedBytes = 0;
    try {
      const resp = await fetch(`/api/servers/${id}/export`, { credentials: "include" });
      if (!resp.ok) return toast("Export failed", "error");
      // Stream the bundle so a large server shows live progress instead of a
      // dead button. The tar is gzipped on the fly, so the final size is
      // unknown up front — we report bytes received as they arrive.
      const reader = resp.body.getReader();
      const chunks = [];
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        chunks.push(value);
        exportedBytes += value.length;
      }
      const blob = new Blob(chunks, { type: "application/gzip" });
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `${(server.name || "server").replace(/[^a-z0-9_-]/gi, "-")}.yggserver.tar.gz`;
      a.click();
      URL.revokeObjectURL(a.href);
      toast("Exported. The bundle holds this server's secrets — handle it like a credential.", "info");
    } catch (e) {
      toast(e.message || "Export failed", "error");
    } finally {
      exporting = false;
    }
  }
  // Human-readable byte count for the live export progress label.
  const fmtBytes = (n) => (n < 1e6 ? `${(n / 1e3).toFixed(0)} KB` : `${(n / 1e6).toFixed(1)} MB`);

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

  // Delete is irreversible (world, backups list, ports all go), so it uses the same
  // deliberate type-the-name modal as restore — not a one-tap confirm().
  let showDelete = $state(false);
  let deleteConfirm = $state(""); // must match the server name to enable Delete
  let deleting = $state(false);
  function del() {
    deleteConfirm = "";
    showDelete = true;
  }
  async function doDelete() {
    if (deleteConfirm !== server.name) return;
    deleting = true;
    try {
      await api.del(`/servers/${id}`);
      toast("Server deleted", "success");
      navigate("/servers");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      deleting = false;
    }
  }

  onMount(async () => {
    loadNetwork();
    loadCrashes();
    loadKvasirEvents();
    await loadServer();
    // Pre-load installed mods (if this rune supports them) so the "N updates" badge
    // on the Mods tab is visible without having to open the tab first.
    if (server?.mods_supported && can("server.files")) loadInstalledMods();
    if (server?.gameskill_id === "minecraft-java" && server?.installed) loadJarUpdate();
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
  <div class="action-bar flex flex-wrap items-center gap-1.5 sm:gap-2 mb-4">
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
          title="Schedule automatic restarts (a managed schedule). Opens a dialog to configure the interval, the hour they start from, player warning and backup.">
          🔁 Auto-restart{autoRestart.enabled
            ? autoRestart.every_hours >= 24
              ? ` · ${hh(autoRestart.anchor_hour)}`
              : ` · ${autoRestart.every_hours}h`
            : ""}
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
    {#if $user?.role === "admin"}
      <button class="btn-ghost" disabled={exporting} onclick={exportServer}
        title="Download this server as a portable bundle (its config, rune and data) to import on another Yggdrasil panel. The bundle contains decrypted secrets — treat it like a credential.">
        {exporting ? `Exporting… ${fmtBytes(exportedBytes)}` : "⤓ Export"}</button>
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

  {#if jarStatus?.update_available}
    <div class="card border-l-4 border-accent2 bg-accent2/5 p-3 mb-4 flex items-center gap-3 flex-wrap">
      <span class="text-sm">
        🧩 A newer <b>{jarStatus.type}</b> build is available for {jarStatus.version} —
        <span class="font-mono">{jarStatus.current_build}</span> → <span class="font-mono text-accent">{jarStatus.latest_build}</span>.
      </span>
      {#if can("server.control")}
        <button class="btn-primary text-xs ml-auto" onclick={() => runInstall(true)} disabled={server.install_status === 'installing'}
          title="Download the latest server jar (re-runs Install: your world and configs are kept, the jar and any regenerated defaults refresh). Back up first if unsure.">
          {server.install_status === "installing" ? "Updating…" : "Update jar"}
        </button>
      {/if}
    </div>
  {/if}

  {#if showDelete}
    <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
      <div class="card w-full max-w-md p-5 space-y-3 border-l-4 border-danger">
        <h2 class="text-lg font-semibold text-danger">⚠️ Delete server</h2>
        <p class="text-sm text-muted">
          This <b class="text-text">permanently deletes</b> <b class="text-text">{server.name}</b> — its container,
          all its data (world, persistence), its backups list and its ports. <b class="text-text">This cannot be undone.</b>
        </p>
        <p class="text-xs text-muted">Tip: Export or back it up first if you might want it again.</p>
        <div>
          <label class="label" for="delete-confirm">Type <span class="font-mono text-text">{server.name}</span> to confirm</label>
          <input id="delete-confirm" class="input" bind:value={deleteConfirm} placeholder={server.name} autocomplete="off" />
        </div>
        <div class="flex gap-2">
          <button class="btn-ghost flex-1" onclick={() => (showDelete = false)}>Cancel</button>
          <button class="btn-danger flex-1" disabled={deleting || deleteConfirm !== server.name} onclick={doDelete}>
            {deleting ? "Deleting…" : "Delete server"}
          </button>
        </div>
      </div>
    </div>
  {/if}

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
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="auto-hours">Restart every</label>
            <select id="auto-hours" class="input" bind:value={autoRestart.every_hours}>
              {#each autoHourOptions as h}<option value={h}>{h === 24 ? "24 hours (daily)" : `${h} hours`}</option>{/each}
            </select>
          </div>
          <div>
            <label class="label" for="auto-anchor">{autoRestart.every_hours === 24 ? "At" : "Starting at"}</label>
            <select id="auto-anchor" class="input" bind:value={autoRestart.anchor_hour}>
              {#each Array.from({ length: 24 }, (_, i) => i) as h}<option value={h}>{hh(h)}</option>{/each}
            </select>
          </div>
        </div>
        <div>
          <p class="text-xs text-muted">Fires at {autoRestartTimes} (server local time).</p>
          {#if quietHours?.has_data}
            <p class="text-xs text-accent mt-1">
              💡 Quietest around {hh(quietHours.recommended_hour)} (avg {quietHours.recommended_avg} players, last 14 days).
              {#if autoRestart.anchor_hour !== quietHours.recommended_hour}
                <button type="button" class="underline hover:no-underline"
                        onclick={() => (autoRestart.anchor_hour = quietHours.recommended_hour)}>
                  Start there
                </button>
              {:else}
                Restarts start there.
              {/if}
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

  {#if crashList.length}
    <div class="card p-4 mb-4 border-l-4 {faultCount ? 'border-warn' : 'border-border'}">
      <h3 class="text-base font-semibold flex items-center gap-2">
        {faultCount ? "⚠️" : "📉"} Stability
        {#if faultCount}
          <span class="badge bg-warn/20 text-warn">{faultCount} recent fault{faultCount === 1 ? "" : "s"}</span>
        {:else}
          <span class="badge bg-border text-muted">{crashList.length} recent stop{crashList.length === 1 ? "" : "s"}</span>
        {/if}
        {#if can("server.control")}
          <button class="btn-ghost text-xs ml-auto" onclick={clearCrashes} title="Clear this stability history.">Clear</button>
        {/if}
      </h3>
      <p class="text-xs text-muted mt-0.5 mb-3">Exits the panel caught while this server was running. A graceful stop/restart (exit 0, 143, 130) is shown in grey; a real fault — a crash or OOM kill — is flagged.</p>
      <div class="space-y-2">
        {#each crashList.slice(0, 8) as c}
          <details class="rounded-md border border-border bg-panel2/40">
            <summary class="flex items-center gap-2 px-3 py-2 cursor-pointer text-sm select-none">
              <span class="badge {isFault(c.exit_code) ? 'bg-danger/20 text-danger' : 'bg-border text-muted'}">
                {isFault(c.exit_code) ? `exit ${c.exit_code}` : "stopped"}
              </span>
              <span class="text-muted">{crashAgo(c.ts)}</span>
              {#if c.reason}<span class="text-muted/70 ml-auto text-xs">log ▾</span>{/if}
            </summary>
            {#if c.reason}
              <pre class="text-[11px] font-mono whitespace-pre-wrap break-words px-3 py-2 border-t border-border text-muted overflow-x-auto">{c.reason}</pre>
            {/if}
          </details>
        {/each}
      </div>
    </div>
  {/if}

  <!-- Kvasir: proactive-AI reactions (surfaced in-panel, not only in Discord) -->
  {#if kvasirEvents.length}
    <div class="card p-4 mb-4 border-l-4 border-accent/60">
      <h3 class="text-base font-semibold flex items-center gap-2">
        🧠 Kvasir
        <span class="badge bg-accent/20 text-accent">{kvasirEvents.length} recent</span>
        {#if can("server.control")}
          <button class="btn-ghost text-xs ml-auto" onclick={clearKvasirEvents} title="Clear Kvasir's reaction history.">Clear</button>
        {/if}
      </h3>
      <p class="text-xs text-muted mt-0.5 mb-3">What the proactive AI saw and how it responded. Suggested fixes it didn't auto-apply (config changes) are left for you to act on.</p>
      <div class="space-y-2">
        {#each kvasirEvents.slice(0, 8) as k}
          <div class="rounded-md border border-border bg-panel2/40 px-3 py-2">
            <div class="flex items-center gap-2 text-sm flex-wrap">
              <span class="badge bg-border text-muted">{k.event}{k.detail ? ` · ${k.detail}` : ""}</span>
              {#if k.applied}
                <span class="badge bg-ok/20 text-ok">applied {k.action}{k.apply_status ? ` (${k.apply_status})` : ""}</span>
              {:else if k.action && k.action !== "none"}
                <span class="badge bg-warn/20 text-warn">proposed {k.action}{k.args ? ` ${k.args}` : ""}</span>
              {/if}
              <span class="text-muted ml-auto text-xs">{crashAgo(k.ts)}</span>
            </div>
            <p class="text-sm mt-1">{k.explanation}</p>
            {#if k.action && k.action !== "none" && !k.applied}
              <p class="text-xs text-muted mt-1">Suggested fix: <code>{k.action}{k.args ? ` ${k.args}` : ""}</code>{k.reason ? ` — ${k.reason}` : ""}</p>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <!-- Shared admin notes -->
  {#if server.notes || editingNotes || can("server.control")}
    <div class="card p-3 mb-4">
      {#if editingNotes}
        <textarea
          class="input w-full min-h-24 font-mono text-sm"
          bind:value={notesDraft}
          placeholder="Shared notes for the admin team — e.g. what this server is for, event schedule, gotchas…"
        ></textarea>
        <div class="flex gap-2 mt-2 items-center flex-wrap">
          <button class="btn-primary" disabled={savingNotes} onclick={saveNotes}>{savingNotes ? "Saving…" : "Save"}</button>
          <button class="btn-ghost" onclick={() => (editingNotes = false)}>Cancel</button>
          <label class="inline-flex items-center gap-2 text-sm ml-auto"
            title="Render this note as Markdown — headings, lists, bold, links, tables. Off means it shows exactly as typed.">
            <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={notesMdDraft} />
            <span>Markdown</span>
          </label>
        </div>
      {:else}
        <div class="flex items-start gap-3">
          <div class="flex-1 min-w-0">
            <div class="text-xs text-muted mb-1">
              📝 Notes{server.notes_markdown ? " · Markdown" : ""}
            </div>
            {#if server.notes}
              {#if server.notes_markdown && server.notes_html}
                <!-- The one place this panel injects HTML. server.notes_html is
                     rendered by internal/api/notes_render.go, which drops raw HTML
                     and empties javascript:/data: URLs — a note is written with
                     server.control and read by an admin, so it crosses a privilege
                     boundary. Never put anything else in here. -->
                <div class="text-sm notes-md break-words">{@html server.notes_html}</div>
              {:else}
                <div class="text-sm whitespace-pre-wrap break-words">{server.notes}</div>
              {/if}
            {:else}
              <div class="text-sm text-muted italic">No notes yet.</div>
            {/if}
          </div>
          {#if can("server.control")}
            <button class="btn-ghost shrink-0" onclick={startEditNotes}>Edit</button>
          {/if}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Tabs — scroll horizontally on narrow screens instead of clipping -->
  <div class="flex gap-1 border-b border-border mb-4 overflow-x-auto -mx-4 px-4 sm:mx-0 sm:px-0">
    {#each tabs as [key, label]}
      <button
        title={tabHelp[key] || ""}
        class="px-3 sm:px-4 py-2 text-sm border-b-2 -mb-px shrink-0 whitespace-nowrap {tab === key
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
        }}
        >{label}{#if key === "mcmods" && modUpdateCount > 0}<span
            class="ml-1.5 badge bg-warn/20 text-warn text-[10px] min-w-4 text-center"
            title="{modUpdateCount} installed mod{modUpdateCount === 1 ? '' : 's'} {modUpdateCount === 1 ? 'has' : 'have'} a newer build available">{modUpdateCount}</span
          >{/if}</button
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
    <div class="flex items-center gap-2 mb-2">
      <span class="text-xs text-muted">
        The most recent install, up to 500 lines. Held in memory — a panel restart clears it.
      </span>
      <div class="flex gap-2 ml-auto">
        <button class="btn-ghost text-xs" disabled={!installLines.length}
          onclick={() => copyLog(installLines, "Install log")}
          title="Copy what's on screen to the clipboard.">Copy</button>
        <button class="btn-ghost text-xs" disabled={!installLines.length || downloadingLog}
          onclick={() => downloadLog("install")}
          title="Download the install log as a text file.">{downloadingLog ? "…" : "Download"}</button>
      </div>
    </div>
    <div bind:this={installEl} class="term h-[50vh]">
      {#if installLines.length === 0}
        <div class="text-muted">No install output yet. Click Install to begin.</div>
      {/if}
      {#each installLines as l}<div>{l}</div>{/each}
    </div>
    {@render explainBlock("install", installLines.join("\n"))}
  {:else if tab === "console"}
    <div class="flex items-center gap-2 mb-2">
      <span class="text-xs text-muted">Live output since you opened this tab.</span>
      <div class="flex gap-2 ml-auto">
        <button class="btn-ghost text-xs" disabled={!lines.length}
          onclick={() => copyLog(lines, "Console")}
          title="Copy what's on screen to the clipboard.">Copy</button>
        <button class="btn-ghost text-xs" onclick={() => (showLogExport = true)}
          title="Download the container's log as a text file — including output from before you opened this tab.">
          Download…
        </button>
      </div>
    </div>
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
            {playersData.count ?? playersData.players.length} online
            {#if playersData.reason}<span class="text-muted/70"> · {playersData.reason}</span>{/if}
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

      {#if playersData.online && !(playersData.count ?? playersData.players.length)}
        <div class="card p-4 text-sm text-muted text-center">No players connected.</div>
      {:else if playersData.online && playersData.players.length === 0}
        <div class="card p-4 text-sm text-muted text-center">
          {playersData.count} player{playersData.count === 1 ? "" : "s"} connected — this server doesn't report their names.
        </div>
      {:else if playersData.players.length}
        <div class="card overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-xs text-muted uppercase tracking-wide">
              <tr class="border-b border-border">
                <th class="text-left px-3 py-2">#</th>
                <th class="text-left px-3 py-2">Name</th>
                <th class="text-left px-3 py-2">Ping</th>
                <th class="text-left px-3 py-2 hidden sm:table-cell">GUID</th>
                {#if playersData.can_kick || playersData.can_ban}<th class="px-3 py-2"></th>{/if}
              </tr>
            </thead>
            <tbody>
              {#each playersData.players as p, i}
                <tr class="border-b border-border/50">
                  <td class="px-3 py-2 font-mono text-muted">{p.id || i + 1}</td>
                  <td class="px-3 py-2 font-medium">{p.name}</td>
                  <td class="px-3 py-2 text-muted">{p.ping || "—"}</td>
                  <td class="px-3 py-2 font-mono text-xs text-muted hidden sm:table-cell truncate max-w-[12rem]">{p.guid || "—"}</td>
                  {#if playersData.can_kick || playersData.can_ban}
                    <td class="px-3 py-2 text-right whitespace-nowrap">
                      {#if playersData.can_kick}
                        <button class="btn-ghost text-xs text-warn" disabled={playersBusy} onclick={() => kickPlayer(p)}
                          title="Disconnect this player now (they can rejoin). You'll be asked for an optional reason.">Kick</button>
                      {/if}
                      {#if playersData.can_ban}
                        <button class="btn-ghost text-xs text-danger" disabled={playersBusy} onclick={() => banPlayer(p)}
                          title="Ban this player (adds them to ban.txt). Takes effect on their next join — DayZ-Linux can't remove a player who's already in-game without RCon.">Ban</button>
                      {/if}
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
  {:else if tab === "mcmods"}
    <div class="max-w-3xl space-y-6">
      <!-- Search -->
      <div>
        <label class="label" for="mod-search">Add a mod or plugin</label>
        <input
          id="mod-search"
          class="input"
          placeholder="Search Modrinth…"
          bind:value={modQuery}
          oninput={onModQuery} />
        <p class="text-xs text-muted mt-1">
          {#if modLoader}
            Showing {modLoader} results{modGameVersion ? ` for ${modGameVersion}` : " (all versions)"} — only what this server can load. Installs to <code>{modsFolder}/</code>; restart the server to apply.
          {:else}
            Only mods/plugins compatible with this server's loader and version are shown.
          {/if}
        </p>
      </div>

      {#if modSearching}
        <p class="text-sm text-muted">Searching…</p>
      {:else if modResults.length}
        <div class="space-y-2">
          {#each modResults as m (m.project_id)}
            <div class="card p-3 flex items-center gap-3">
              {#if m.icon_url}<img src={modIcon(m.icon_url)} alt="" class="w-9 h-9 rounded flex-shrink-0" />{/if}
              <div class="min-w-0 flex-1">
                <div class="font-medium truncate">{m.title}</div>
                <div class="text-xs text-muted truncate">{m.description}</div>
              </div>
              {#if installedSlugs.has(m.slug)}
                <span class="badge bg-border text-muted">installed</span>
              {:else}
                <button
                  class="btn-primary text-sm"
                  disabled={modBusy === m.project_id}
                  onclick={() => installMod(m.slug || m.project_id)}>
                  {modBusy === m.project_id ? "Installing…" : "Install"}
                </button>
              {/if}
            </div>
          {/each}
        </div>
      {:else if modQuery.trim()}
        <p class="text-sm text-muted">No compatible results for “{modQuery}”.</p>
      {/if}

      <!-- Installed -->
      <div>
        <h3 class="text-sm font-semibold mb-2">Installed</h3>
        {#if modsLoading}
          <p class="text-sm text-muted">Loading…</p>
        {:else if modsInstalled.length === 0}
          <p class="text-sm text-muted">No mods installed yet.</p>
        {:else}
          <div class="space-y-2">
            {#each modsInstalled as m (m.filename)}
              <div class="card p-3 flex items-center gap-3">
                {#if m.icon_url}<img src={modIcon(m.icon_url)} alt="" class="w-8 h-8 rounded flex-shrink-0" />{/if}
                <div class="min-w-0 flex-1">
                  <div class="font-medium truncate">
                    {m.title || m.filename}
                    {#if !m.managed}<span class="badge bg-border text-muted ml-1">manual</span>{/if}
                    {#if m.update_available}<span class="badge bg-warn/20 text-warn ml-1">update</span>{/if}
                  </div>
                  <div class="text-xs text-muted truncate">
                    {m.installed_version || m.filename}{#if m.update_available} → {m.latest_version}{/if}
                  </div>
                </div>
                {#if m.update_available}
                  <button class="btn-primary text-sm" disabled={modBusy === m.filename} onclick={() => updateMod(m.filename)}>
                    {modBusy === m.filename ? "…" : "Update"}
                  </button>
                {/if}
                <button class="btn-ghost text-sm text-warn" disabled={modBusy === m.filename} onclick={() => removeMcMod(m.filename)}>Remove</button>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {:else if tab === "files"}
    <FileManager serverId={id} configFiles={server.config_files ?? []} />
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
        {#if $user?.role === "admin"}
          <div>
            <label class="label" for="e-realm">Group</label>
            <select id="e-realm" class="input" bind:value={edit.realm_id}>
              <option value="">— no group —</option>
              {#each realms as rl (rl.id)}<option value={rl.id}>{rl.name}</option>{/each}
            </select>
            <p class="text-xs text-muted mt-1">The realm this server belongs to. Moving it also changes which delegates can reach it.</p>
          </div>
        {/if}
        <div>
          <label class="label" for="e-tags">Tags</label>
          <input id="e-tags" class="input" placeholder="e.g. survival, event, staging" bind:value={edit.tags} />
          <p class="text-xs text-muted mt-1">Comma-separated labels for grouping and filtering on the Servers page.</p>
        </div>
        <div>
          <div class="label">Resource alarms</div>
          <div class="grid grid-cols-3 gap-3">
            <div>
              <label class="text-xs text-muted" for="cpu-alarm">CPU (%)</label>
              <input id="cpu-alarm" class="input" type="number" min="0" max="100" bind:value={edit.cpu_alarm_pct} placeholder="0 = off" />
            </div>
            <div>
              <label class="text-xs text-muted" for="mem-alarm">Memory (MB)</label>
              <input id="mem-alarm" class="input" type="number" min="0" bind:value={edit.mem_alarm_mb} placeholder="0 = off" />
            </div>
            <div>
              <label class="text-xs text-muted" for="disk-alarm">Disk (MB)</label>
              <input id="disk-alarm" class="input" type="number" min="0" bind:value={edit.disk_alarm_mb} placeholder="0 = off" />
            </div>
          </div>
          <p class="text-xs text-muted mt-1">
            Get a notification when CPU% or memory stays at/above the threshold for ~10 min, or when the data
            directory grows past the disk threshold (checked hourly) — plus an all-clear when it recovers. 0 = off.
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
            <button class="btn-ghost" onclick={() => downloadBackup(b)}
              title="Download this backup archive to keep a copy off-panel.">Download</button>
            <button class="btn-ghost" onclick={() => restoreBackup(b)}
              title="Stop the server and overwrite its files with this backup. Current data is replaced — you'll be asked to confirm by typing the server name.">Restore</button>
          {/if}
          <button class="btn-danger" onclick={() => deleteBackup(b)}
            title="Delete this backup archive. Does not affect the running server.">Delete</button>
        </div>
      {/each}
    </div>

    {#if restoreTarget}
      <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
        <div class="card w-full max-w-md p-5 space-y-3 border-l-4 border-danger">
          <h2 class="text-lg font-semibold text-danger">⚠️ Restore backup</h2>
          <p class="text-sm text-muted">
            This overwrites <b class="text-text">{server.name}</b>'s current world and data with the backup from
            <b class="text-text">{fmtDate(restoreTarget.created_at)}</b>. The server is stopped first, and
            <b class="text-text">this cannot be undone</b> — the current data is replaced.
          </p>
          <p class="text-xs text-muted">Tip: take a fresh backup first if you might want today's state back.</p>
          <div>
            <label class="label" for="restore-confirm">Type <span class="font-mono text-text">{server.name}</span> to confirm</label>
            <input id="restore-confirm" class="input" bind:value={restoreConfirm} placeholder={server.name} autocomplete="off" />
          </div>
          <div class="flex gap-2">
            <button class="btn-ghost flex-1" onclick={() => (restoreTarget = null)}>Cancel</button>
            <button class="btn-danger flex-1" disabled={restoring || restoreConfirm !== server.name} onclick={doRestore}>
              {restoring ? "Restoring…" : "Restore"}
            </button>
          </div>
        </div>
      </div>
    {/if}
  {:else if tab === "mods"}
    <div class="flex items-center justify-between gap-2 mb-1">
      <h3 class="text-lg font-semibold">🧩 Workshop mods</h3>
      <div class="flex gap-2">
        <button class="btn-ghost text-xs" onclick={suggestMods} disabled={modSuggesting || !(modStatus?.mods || []).length}
          title="Ask Kvasir to flag missing dependencies (frameworks like CF / Dabs) and a sane load order.">
          {modSuggesting ? "Kvasir…" : "🧠 Kvasir: check deps + order"}
        </button>
        <button class="btn-ghost text-xs" onclick={loadMods} disabled={modsBusy}>Refresh</button>
      </div>
    </div>
    <p class="text-muted text-sm mb-4">
      The mods this server loads, <b>in order</b>. Search or paste a Workshop id to add one, drag to
      reorder (frameworks/dependencies should load first), and remove the ones you don't want.
      Adds/removes download on the next <b>Update/Reinstall</b>; a reorder applies on the next restart.
    </p>

    {#if can("server.control")}
      <div class="card p-3 mb-4 space-y-3">
        <!-- Search the Workshop (needs a Steam Web API key) -->
        <form class="flex gap-2" onsubmit={(e) => { e.preventDefault(); dzSearchMods(); }}>
          <input class="input flex-1" placeholder="Search the DayZ Workshop…" bind:value={dzQuery} />
          <button class="btn-ghost shrink-0" type="submit" disabled={dzSearching}>{dzSearching ? "…" : "Search"}</button>
        </form>
        {#if modNeedsKey}
          <p class="text-xs text-muted">
            Search needs a Steam Web API key —
            <button class="text-accent hover:underline" onclick={() => (tab = "settings")}>add one in Settings → Integrations</button>.
            You can still paste an id below.
          </p>
        {/if}
        {#if dzResults.length}
          <div class="card divide-y divide-border max-h-72 overflow-y-auto">
            {#each dzResults as r}
              <div class="flex items-center gap-3 p-2">
                <div class="min-w-0 flex-1">
                  <a class="text-accent hover:underline text-sm truncate block" href={r.url} target="_blank" rel="noopener">{r.title}</a>
                  <span class="text-muted text-xs font-mono">{r.id}</span>
                </div>
                {#if configuredIds.has(r.id)}
                  <span class="text-xs text-muted shrink-0">added</span>
                {:else}
                  <button class="btn-ghost text-xs shrink-0" onclick={() => addModById(r.id)} disabled={modsBusy}>Add</button>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        <!-- Paste a Workshop id directly -->
        <form class="flex gap-2" onsubmit={(e) => { e.preventDefault(); addModById(modPasteId); }}>
          <input class="input flex-1 font-mono" placeholder="…or paste a Workshop id, e.g. 1559212036" bind:value={modPasteId} inputmode="numeric" />
          <button class="btn-ghost shrink-0" type="submit" disabled={modsBusy || !modPasteId.trim()}>Add id</button>
        </form>
      </div>
    {/if}

    {#if modSuggest}
      <div class="card border-l-4 border-accent/60 p-3 mb-4 space-y-2">
        <div class="flex items-center gap-2">
          <h4 class="text-sm font-semibold">🧠 Kvasir's review</h4>
          <button class="btn-ghost text-xs ml-auto" onclick={() => (modSuggest = null)}>Dismiss</button>
        </div>
        {#if modSuggest.dependencies?.length}
          <div>
            <p class="text-xs text-muted mb-1">Possibly missing dependencies:</p>
            <ul class="text-sm space-y-1">
              {#each modSuggest.dependencies as d}
                <li>• <b>{d.name}</b>{d.reason ? ` — ${d.reason}` : ""}</li>
              {/each}
            </ul>
          </div>
        {:else}
          <p class="text-sm text-muted">No missing dependencies spotted.</p>
        {/if}
        {#if modSuggest.order_note}
          <p class="text-sm">{modSuggest.order_note}</p>
        {/if}
        {#if modSuggest.recommended_order?.length}
          <button class="btn-ghost text-xs text-accent" onclick={applySuggestedOrder} disabled={modsBusy}>
            Apply Kvasir's load order
          </button>
        {/if}
        <p class="text-[11px] text-muted">Advisory — Kvasir never edits the list itself. Verify ids on the Workshop before adding.</p>
      </div>
    {/if}

    {#if !modStatus}
      <div class="text-muted text-sm">Loading…</div>
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
          {#each modStatus.mods as m, i (m.id)}
            <div class="flex items-center gap-2 sm:gap-3 p-2.5 {modDrag === i ? 'bg-panel2/60' : ''}"
              role="listitem"
              draggable={can("server.control") && modStatus.mods.length > 1}
              ondragstart={() => (modDrag = i)}
              ondragover={(e) => modDragOver(e, i)}
              ondragend={saveOrder}
              ondrop={saveOrder}>
              {#if can("server.control") && modStatus.mods.length > 1}
                <span class="text-muted cursor-grab select-none shrink-0" title="Drag to reorder">⠿</span>
              {/if}
              <span class="text-muted text-xs w-5 text-right shrink-0">{i + 1}</span>
              <div class="min-w-0 flex-1">
                <a class="text-accent hover:underline font-medium truncate block" href={m.url} target="_blank" rel="noopener">{m.name}</a>
                <span class="text-muted text-xs font-mono">{m.id}</span>
              </div>
              {#if m.installed}
                <span class="text-xs px-2 py-0.5 rounded bg-accent2/15 text-accent shrink-0 hidden sm:inline">on disk</span>
              {:else}
                <span class="text-xs px-2 py-0.5 rounded bg-warn/15 text-warn shrink-0">not downloaded</span>
              {/if}
              {#if m.workshop === "removed"}
                <span class="text-xs px-2 py-0.5 rounded bg-danger/15 text-danger shrink-0">removed</span>
              {:else if m.workshop === "ok"}
                <span class="text-xs px-2 py-0.5 rounded bg-panel2 text-muted shrink-0 hidden sm:inline">Workshop ✓</span>
              {/if}
              {#if can("server.control")}
                <button class="btn-ghost text-xs shrink-0" onclick={() => removeModById(m.id)} disabled={modsBusy}>Remove</button>
              {/if}
            </div>
          {/each}
        </div>
      {:else}
        <div class="card text-sm p-3 text-muted mb-4">No mods in the load order yet — search or paste an id above to add one.</div>
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
              {#if can("server.control")}
                <button class="btn-ghost text-xs shrink-0" onclick={() => addModById(m.id)} disabled={modsBusy}>Add to list</button>
              {/if}
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

{#if showLogExport}
  <div class="fixed inset-0 bg-black/60 grid place-items-center z-40 p-4" role="presentation"
    onclick={(e) => { if (e.target === e.currentTarget) showLogExport = false; }}>
    <div class="card p-5 w-full max-w-md space-y-3">
      <h2 class="text-lg font-semibold">Download console log</h2>
      <p class="text-sm text-muted">
        Straight from the container, so it includes output from before you opened the tab.
      </p>
      <div>
        <label class="label" for="log-range">Range</label>
        <select id="log-range" class="input" bind:value={logRange}>
          {#each logRanges as r}<option value={r.id}>{r.label}</option>{/each}
        </select>
      </div>
      <label class="inline-flex items-center gap-2 text-sm">
        <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={logTimestamps} />
        <span>Include timestamps</span>
      </label>
      <p class="text-xs text-muted">
        The log starts when the container was last created — starting or restarting the server
        makes a new one, so there's nothing from before that to fetch.
      </p>
      <p class="text-xs text-muted">
        Server logs can contain passwords and tokens. Read it before you share it.
      </p>
      <div class="flex gap-2 justify-end pt-1">
        <button class="btn-ghost" onclick={() => (showLogExport = false)}>Cancel</button>
        <button class="btn-primary" disabled={downloadingLog} onclick={() => downloadLog("console")}>
          {downloadingLog ? "Checking…" : "Download"}
        </button>
      </div>
    </div>
  </div>
{/if}
