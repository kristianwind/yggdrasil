<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { registerPasskey, passkeysSupported } from "../lib/webauthn.js";
  import RealmManager from "../components/RealmManager.svelte";

  let targets = $state([]);
  let showCreate = $state(false);
  let form = $state(blank());

  // Settings are grouped into tabs so the page doesn't grow into one long scroll.
  const settingsTabs = [
    { id: "system", label: "System" },
    { id: "network", label: "Network" },
    { id: "domains", label: "Domains" },
    { id: "security", label: "Security" },
    { id: "realms", label: "Realms" },
    { id: "integrations", label: "Integrations" },
  ];
  let tab = $state(localStorage.getItem("ygg_settings_tab") || "system");
  function selectTab(id) {
    tab = id;
    try {
      localStorage.setItem("ygg_settings_tab", id);
    } catch {
      /* ignore */
    }
  }

  // Two-factor auth
  let twofa = $state({ enabled: false });
  let twofaSetup = $state(null); // { secret, uri }
  let twofaCode = $state("");

  // Host OS updates — read-only. The panel says what's pending; applying is left
  // to the operator, because `apt upgrade` bounces Docker (and every server with
  // it) and that's not a call to make from a web button.
  let osUpd = $state(null);
  const staleAptCache = $derived(osUpd?.cache_age_hours != null && osUpd.cache_age_hours > 48);
  async function loadOSUpdates() {
    try {
      osUpd = await api.get("/system/os-updates");
    } catch {
      osUpd = { supported: false, note: "Could not read the host's update status." };
    }
  }
  async function copyAptCmd() {
    const cmd = "sudo apt update && sudo apt upgrade";
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(cmd);
      } else {
        // The panel is often reached over plain http on a LAN.
        const ta = document.createElement("textarea");
        ta.value = cmd;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      toast("Command copied", "success");
    } catch (e) {
      toast("Could not copy: " + e.message, "error");
    }
  }

  // Panel updates (self-update of the Yggdrasil panel binary)
  let build = $state(null);
  let updating = $state(false);
  let checking = $state(false);
  async function loadBuild() {
    try {
      build = await api.get("/version");
    } catch {
      build = null;
    }
  }
  async function checkUpdates() {
    checking = true;
    try {
      build = await api.post("/system/check-update", {});
      toast(build.update_available ? `Update available: ${build.latest}` : "You're on the latest version", "info");
    } catch (err) {
      toast(err.message, "error");
    } finally {
      checking = false;
    }
  }
  async function doUpdate() {
    if (!build?.latest) return;
    if (!confirm(`Update Yggdrasil to ${build.latest}? The panel will restart briefly.`)) return;
    updating = true;
    try {
      await api.post("/system/update", {});
      toast(`Updating to ${build.latest}… the panel will restart`, "info");
      await waitForRestart(build.latest);
    } catch (err) {
      toast(err.message || "Update failed", "error");
      updating = false;
    }
  }
  async function waitForRestart(target) {
    const deadline = Date.now() + 90000;
    await new Promise((r) => setTimeout(r, 2000));
    while (Date.now() < deadline) {
      try {
        const v = await api.get("/version", { allow401: true });
        if (v && v.version === target) {
          toast(`Updated to ${target} — reloading`, "success");
          setTimeout(() => location.reload(), 800);
          return;
        }
      } catch {
        /* server mid-restart — keep polling */
      }
      // The download/verify runs in a background unit; surface a recorded failure.
      try {
        const st = await api.get("/system/update-status", { allow401: true });
        if (st?.state === "error") {
          toast(`Update failed: ${st.message || "see server logs"}`, "error");
          updating = false;
          return;
        }
      } catch {
        /* ignore while restarting */
      }
      await new Promise((r) => setTimeout(r, 2000));
    }
    toast("Update is taking longer than expected — reload the page in a moment", "info");
    updating = false;
  }

  // Scheduled auto-update (opt-in)
  let autoUpdate = $state({ enabled: false, hour: 4 });
  async function loadAutoUpdate() {
    try {
      autoUpdate = await api.get("/system/auto-update");
    } catch {
      /* keep defaults */
    }
  }
  async function saveAutoUpdate() {
    try {
      autoUpdate = await api.post("/system/auto-update", {
        enabled: autoUpdate.enabled,
        hour: Number(autoUpdate.hour),
      });
      toast("Auto-update settings saved", "success");
    } catch (err) {
      toast(err.message, "error");
    }
  }

  // Passkeys (WebAuthn)
  let passkeys = $state([]);
  let pkBusy = $state(false);
  const canPasskey = passkeysSupported();

  async function loadPasskeys() {
    try {
      passkeys = await api.get("/auth/passkey/credentials");
    } catch {
      passkeys = [];
    }
  }
  async function addPasskey() {
    // Run the WebAuthn ceremony immediately on this click (no blocking dialog
    // first — that would break the transient user activation create() needs).
    // Name it afterwards.
    pkBusy = true;
    try {
      const res = await registerPasskey();
      const name = prompt("Name this passkey (e.g. 'MacBook Touch ID'):", "passkey");
      if (name && name.trim() && name.trim() !== "passkey" && res?.id) {
        try {
          await api.put(`/auth/passkey/credentials/${res.id}`, { name: name.trim() });
        } catch {
          /* naming is best-effort; the passkey is already registered */
        }
      }
      toast("Passkey added", "success");
      await loadPasskeys();
    } catch (err) {
      if (err.name === "NotAllowedError" || err.name === "AbortError") {
        toast("Passkey creation was cancelled or timed out", "info");
      } else {
        toast(`Could not add passkey: ${err.name ? err.name + " — " : ""}${err.message || err}`, "error");
      }
    } finally {
      pkBusy = false;
    }
  }

  async function renamePasskey(pk) {
    const name = prompt("Rename passkey:", pk.name);
    if (name === null) return;
    try {
      await api.put(`/auth/passkey/credentials/${pk.id}`, { name: name.trim() || "passkey" });
      await loadPasskeys();
    } catch (err) {
      toast(err.message, "error");
    }
  }
  async function delPasskey(pk) {
    if (!confirm(`Remove passkey "${pk.name}"?`)) return;
    try {
      await api.del(`/auth/passkey/credentials/${pk.id}`);
      toast("Passkey removed", "success");
      await loadPasskeys();
    } catch (err) {
      toast(err.message, "error");
    }
  }

  async function load2fa() {
    try {
      twofa = await api.get("/auth/2fa");
    } catch (e) {
      /* ignore */
    }
  }
  async function start2fa() {
    try {
      twofaSetup = await api.post("/auth/2fa/setup");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function enable2fa() {
    try {
      await api.post("/auth/2fa/enable", { code: twofaCode });
      toast("2FA enabled", "success");
      twofaSetup = null;
      twofaCode = "";
      await load2fa();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function disable2fa() {
    const c = prompt("Enter a current 2FA code to disable:");
    if (c === null) return;
    try {
      await api.post("/auth/2fa/disable", { code: c });
      toast("2FA disabled", "success");
      await load2fa();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // Message templates
  let templates = $state([]);
  let editing = $state(null); // { id?, name, body }

  // Steam authorization
  let steam = $state(null);
  let steamForm = $state({ username: "", password: "", guard_code: "" });
  let steamStep = $state(1); // 1 = credentials, 2 = guard code
  let steamBusy = $state(false);

  async function loadSteam() {
    try {
      steam = await api.get("/steam/account");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  // Step 1: trigger Steam to email a Guard code (login without a code).
  async function sendSteamCode() {
    if (!steamForm.username || !steamForm.password) return toast("Username and password required", "warn");
    steamBusy = true;
    try {
      const res = await api.post("/steam/send-code", {
        username: steamForm.username,
        password: steamForm.password,
      });
      if (res.status === "no_guard_needed") {
        await authorizeSteam(); // no Guard — finish immediately
      } else {
        steamStep = 2;
        toast("Steam Guard code sent to your account's email", "info");
      }
    } catch (e) {
      toast(e.message, "error");
    } finally {
      steamBusy = false;
    }
  }
  // Step 2: complete authorization with the emailed code.
  async function authorizeSteam() {
    steamBusy = true;
    try {
      await api.post("/steam/authorize", steamForm);
      toast("Steam account authorized", "success");
      steamForm = { username: "", password: "", guard_code: "" };
      steamStep = 1;
      await loadSteam();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      steamBusy = false;
    }
  }
  async function forgetSteam() {
    if (!confirm("Forget the Steam account? (The cached login is kept on disk so re-adding won't re-trigger Steam Guard.)")) return;
    try {
      await api.del("/steam/account");
      await loadSteam();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // Steam Web API key — for DayZ Workshop mod search (write-only; separate from the
  // SteamCMD login above, which downloads mods).
  let steamKey = $state({ configured: false });
  let steamKeyInput = $state("");
  let steamKeyBusy = $state(false);
  async function loadSteamKey() {
    steamKey = await api.get("/settings/steam-web-api-key").catch(() => ({ configured: false }));
  }
  async function saveSteamKey() {
    steamKeyBusy = true;
    try {
      steamKey = await api.put("/settings/steam-web-api-key", { key: steamKeyInput.trim() });
      steamKeyInput = "";
      toast(steamKey.configured ? "Steam Web API key saved" : "Steam Web API key cleared", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      steamKeyBusy = false;
    }
  }

  // API tokens
  let tokens = $state([]);
  let newTokenName = $state("");
  let createdToken = $state(null); // plaintext shown once

  // Notifications
  let channels = $state([]);
  let notifyServers = $state([]); // for the per-channel "scope to a server" picker
  let showNotify = $state(false);
  let notifyForm = $state({
    type: "telegram",
    token: "",
    chat_id: "",
    url: "",
    host: "",
    port: 587,
    username: "",
    password: "",
    from: "",
    to: "",
    server_id: "",
  });

  async function loadTokens() {
    try {
      tokens = await api.get("/tokens");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function createToken() {
    if (!newTokenName) return toast("Name required", "warn");
    try {
      const res = await api.post("/tokens", { name: newTokenName });
      createdToken = res.token;
      newTokenName = "";
      await loadTokens();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function deleteToken(t) {
    if (!confirm(`Delete token "${t.name}"?`)) return;
    try {
      await api.del(`/tokens/${t.id}`);
      await loadTokens();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function loadChannels() {
    try {
      channels = await api.get("/notifications");
      notifyServers = await api.get("/servers").catch(() => []);
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function createChannel() {
    try {
      await api.post("/notifications", notifyForm);
      toast("Channel added", "success");
      showNotify = false;
      notifyForm = { type: "telegram", token: "", chat_id: "", url: "", host: "", port: 587, username: "", password: "", from: "", to: "", server_id: "" };
      await loadChannels();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function testChannel(c) {
    try {
      await api.post(`/notifications/${c.id}/test`);
      toast("Test sent", "success");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function deleteChannel(c) {
    if (!confirm("Delete this channel?")) return;
    try {
      await api.del(`/notifications/${c.id}`);
      await loadChannels();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function loadTemplates() {
    try {
      templates = await api.get("/templates");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function saveTemplate() {
    if (!editing.name || !editing.body) return toast("Name and body required", "warn");
    try {
      await api.post("/templates", editing);
      toast("Template saved", "success");
      editing = null;
      await loadTemplates();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function deleteTemplate(t) {
    if (!confirm(`Delete template "${t.name}"?`)) return;
    try {
      await api.del(`/templates/${t.id}`);
      await loadTemplates();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  function blank() {
    return {
      name: "",
      type: "local",
      path: "",
      host: "",
      port: 0,
      username: "",
      password: "",
      share: "",
      keep_n: 0,
      keep_days: 0,
    };
  }

  async function load() {
    try {
      targets = await api.get("/backup/targets");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  // Network (public hostname / connect address)
  let network = $state({ public_hostname: "", detected: "", internal: "", effective: "" });
  let savingNetwork = $state(false);
  async function loadNetwork() {
    try {
      network = await api.get("/settings/network");
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveNetwork() {
    savingNetwork = true;
    try {
      const res = await api.put("/settings/network", {
        public_hostname: network.public_hostname,
        upnp_enabled: !!network.upnp_enabled,
        battlemetrics_token: network.battlemetrics_token ?? "",
      });
      network = { ...network, ...res };
      toast("Network settings saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingNetwork = false;
    }
  }
  // Public status page (opt-in up/down board at /status)
  let statusPage = $state({ enabled: false, title: "Server Status" });
  let savingStatusPage = $state(false);
  async function loadStatusPage() {
    try {
      statusPage = await api.get("/settings/status-page");
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveStatusPage() {
    savingStatusPage = true;
    try {
      statusPage = await api.put("/settings/status-page", {
        enabled: !!statusPage.enabled,
        title: statusPage.title || "",
      });
      toast("Status page settings saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingStatusPage = false;
    }
  }

  // Beacon (voluntary, opt-in install ping)
  let beacon = $state({ enabled: false, url: "", instance_id: "", version: "", receiver_enabled: false });
  let savingBeacon = $state(false);
  let testingBeacon = $state(false);
  let beaconStats = $state(null);
  async function testBeacon() {
    testingBeacon = true;
    try {
      const r = await api.post("/settings/beacon/test", { url: beacon.url || "" });
      if (r.ok) toast(`Collector reachable (HTTP ${r.status})`, "success");
      else toast(r.error || "Collector not reachable", "error");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      testingBeacon = false;
    }
  }
  async function loadBeacon() {
    try {
      beacon = await api.get("/settings/beacon");
      if (beacon.receiver_enabled) loadBeaconStats();
    } catch (e) {
      /* non-fatal */
    }
  }
  async function loadBeaconStats() {
    try {
      beaconStats = await api.get("/beacon/stats");
    } catch (e) {
      beaconStats = null;
    }
  }
  async function saveBeacon() {
    savingBeacon = true;
    try {
      // Don't send receiver_enabled — the collector role is managed out-of-band, so
      // saving client settings here must never flip it. The backend leaves any field
      // we omit unchanged.
      const body = {
        enabled: !!beacon.enabled,
        url: beacon.url || "",
      };
      // The publish settings only exist on a collector, and only send when they're
      // on screen — on every other instance those fields aren't rendered and would
      // post undefined.
      if (beacon.receiver_enabled) {
        body.public_count_enabled = !!beacon.public_count_enabled;
        const min = parseInt(beacon.public_count_min, 10);
        if (!isNaN(min)) body.public_count_min = min;
      }
      beacon = await api.put("/settings/beacon", body);
      toast("Beacon settings saved", "success");
      if (beacon.receiver_enabled) loadBeaconStats();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingBeacon = false;
    }
  }

  // Nightly backup verification
  let backupVerify = $state({ enabled: false, last_run: "" });
  let savingBackupVerify = $state(false);
  async function loadBackupVerify() {
    try {
      backupVerify = await api.get("/settings/backup-verify");
    } catch {
      /* non-fatal */
    }
  }
  async function saveBackupVerify() {
    savingBackupVerify = true;
    try {
      backupVerify = await api.put("/settings/backup-verify", { enabled: !!backupVerify.enabled });
      toast("Backup verification settings saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingBackupVerify = false;
    }
  }

  // Discord status board (auto-updating embed via webhook)
  let discord = $state({ enabled: false, configured: false });
  let discordWebhook = $state("");
  let savingDiscord = $state(false);
  let postingDiscord = $state(false);
  async function loadDiscord() {
    try {
      discord = await api.get("/settings/discord-status");
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveDiscord() {
    savingDiscord = true;
    try {
      const body = { enabled: !!discord.enabled };
      if (discordWebhook.trim()) body.webhook = discordWebhook.trim();
      discord = await api.put("/settings/discord-status", body);
      discordWebhook = "";
      toast("Discord status board saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingDiscord = false;
    }
  }
  async function postDiscord() {
    postingDiscord = true;
    try {
      await api.post("/settings/discord-status/post", {});
      toast("Posted to Discord", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      postingDiscord = false;
    }
  }

  // Discord control bot (two-way slash commands). Token is write-only; the API
  // only reports whether one is configured.
  let bot = $state({ configured: false, control_channel: "" });
  let botToken = $state("");
  let savingBot = $state(false);
  async function loadBot() {
    try {
      bot = await api.get("/settings/discord-bot");
    } catch {
      /* admin-only; leave defaults */
    }
  }
  async function saveBot() {
    savingBot = true;
    try {
      const body = { control_channel: bot.control_channel || "" };
      if (botToken.trim()) body.token = botToken.trim();
      bot = await api.put("/settings/discord-bot", body);
      botToken = "";
      toast("Discord bot saved — reconnecting", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingBot = false;
    }
  }

  let upnpStatus = $state(null);
  let checkingUpnp = $state(false);
  async function checkUpnp() {
    checkingUpnp = true;
    try {
      upnpStatus = await api.get("/upnp/status");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      checkingUpnp = false;
    }
  }

  // UniFi port forwarding
  let unifi = $state({ url: "", username: "", password: "", site: "default", enabled: false, configured: false });
  let savingUnifi = $state(false);
  let testingUnifi = $state(false);
  async function loadUnifi() {
    try {
      unifi = { ...unifi, ...(await api.get("/settings/unifi")), password: "" };
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveUnifi() {
    savingUnifi = true;
    try {
      const res = await api.put("/settings/unifi", unifi);
      unifi = { ...unifi, ...res, password: "" };
      toast("UniFi settings saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingUnifi = false;
    }
  }
  async function testUnifi() {
    testingUnifi = true;
    try {
      const res = await api.post("/settings/unifi/test", unifi);
      toast(`UniFi connected — ${res.rules} port-forward rules found`, "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      testingUnifi = false;
    }
  }

  // AI assistant (advisory features — admin brings their own LLM)
  let ai = $state({ provider: "openai", model: "", base_url: "", api_key: "", enabled: false, configured: false, digest_enabled: false, digest_hour: 8, actions_enabled: false, proactive_level: 0, proactive_triggers: "crash,slowstart,resource,host" });
  // Kvasir proactive trigger checkboxes ↔ the csv string on `ai`.
  const PROACTIVE_TRIGGERS = [
    ["crash", "Crashes / faults"],
    ["slowstart", "Slow / failed starts"],
    ["resource", "Resource alarms"],
    ["host", "Panel / host problems"],
    ["anomaly", "Player / traffic anomalies"],
  ];
  function toggleTrigger(key) {
    const set = new Set((ai.proactive_triggers || "").split(",").map((t) => t.trim()).filter(Boolean));
    set.has(key) ? set.delete(key) : set.add(key);
    ai.proactive_triggers = [...set].join(",");
  }
  const hasTrigger = (key) => (ai.proactive_triggers || "").split(",").map((t) => t.trim()).includes(key);
  let savingAi = $state(false);
  let testingAi = $state(false);
  const aiProviders = ["openai", "anthropic", "openrouter", "deepseek", "mistral", "ollama", "custom"];
  async function loadAi() {
    try {
      ai = { ...ai, ...(await api.get("/ai/config")), api_key: "" };
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveAi() {
    savingAi = true;
    try {
      await api.put("/ai/config", ai);
      toast("AI settings saved", "success");
      await loadAi();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingAi = false;
    }
  }
  async function testAi() {
    testingAi = true;
    try {
      const r = await api.post("/ai/config/test", {});
      toast(`AI connected — replied: ${r.reply}`, "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      testingAi = false;
    }
  }

  // NPM (Nginx Proxy Manager) subdomain routing
  let npm = $state({ url: "", email: "", password: "", base_domain: "", internal_host: "", le_email: "", enabled: false, configured: false });
  let savingNpm = $state(false);
  let testingNpm = $state(false);
  async function loadNpm() {
    try {
      npm = { ...npm, ...(await api.get("/settings/npm")), password: "" };
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveNpm() {
    savingNpm = true;
    try {
      const res = await api.put("/settings/npm", npm);
      npm = { ...npm, ...res, password: "" };
      toast("NPM settings saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingNpm = false;
    }
  }
  async function testNpm() {
    testingNpm = true;
    try {
      const res = await api.post("/settings/npm/test", npm);
      toast(`NPM connected — ${res.hosts} proxy hosts found`, "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      testingNpm = false;
    }
  }

  // Cloudflare Tunnel subdomain routing
  let cf = $state({ token: "", account_id: "", zone_id: "", tunnel_id: "", base_domain: "", internal_host: "", enabled: false, configured: false });
  let savingCf = $state(false);
  let testingCf = $state(false);
  async function loadCf() {
    try {
      cf = { ...cf, ...(await api.get("/settings/cloudflare")), token: "" };
    } catch (e) {
      /* non-fatal */
    }
  }
  async function saveCf() {
    savingCf = true;
    try {
      const res = await api.put("/settings/cloudflare", cf);
      cf = { ...cf, ...res, token: "" };
      toast("Cloudflare settings saved", "success");
    } catch (e) {
      toast(e.message, "error");
    } finally {
      savingCf = false;
    }
  }
  async function testCf() {
    testingCf = true;
    try {
      const res = await api.post("/settings/cloudflare/test", cf);
      toast(`Cloudflare token OK${res.zone_id ? " — zone resolved" : ""}`, "success");
      if (res.zone_id) cf.zone_id = res.zone_id;
    } catch (e) {
      toast(e.message, "error");
    } finally {
      testingCf = false;
    }
  }

  // Kvasir Watchers — log-pattern rules.
  let watchers = $state([]);
  let watcherServers = $state([]); // for the per-server scope dropdown
  const WATCHER_PRESETS = [
    { name: "Failed logins", pattern: "(?i)failed (login|password|authentication)|authentication failure|invalid user", threshold: 5, window_secs: 120, action: "kvasir" },
    { name: "HTTP 5xx spike", pattern: "\" 5\\d\\d ", threshold: 10, window_secs: 60, action: "kvasir" },
    { name: "Database errors", pattern: "(?i)(deadlock|too many connections|out of memory|corrupt|SQLSTATE|slow query)", threshold: 3, window_secs: 120, action: "kvasir" },
    { name: "DayZ: suspicious admin log", pattern: "(?i)(hit by Player|killed by Player).*", threshold: 8, window_secs: 60, action: "notify" },
    { name: "Out of memory", pattern: "(?i)(out of memory|OOM|java.lang.OutOfMemoryError|Killed process)", threshold: 1, window_secs: 300, action: "kvasir" },
  ];
  let watcherForm = $state(null); // null = closed; else the editing/creating object
  let watcherBusy = $state(false);
  async function loadWatchers() {
    watchers = await api.get("/watchers").catch(() => []);
  }
  function newWatcher(preset) {
    watcherForm = {
      id: "", server_id: "", name: "", pattern: "", threshold: 3, window_secs: 60, action: "kvasir", enabled: true,
      ...(preset || {}),
    };
  }
  function editWatcher(w) {
    watcherForm = { ...w };
  }
  async function saveWatcher() {
    if (!watcherForm.name.trim() || !watcherForm.pattern.trim()) {
      toast("Name and pattern are required", "error");
      return;
    }
    watcherBusy = true;
    try {
      if (watcherForm.id) await api.put(`/watchers/${watcherForm.id}`, watcherForm);
      else await api.post("/watchers", watcherForm);
      toast("Watcher saved", "success");
      watcherForm = null;
      await loadWatchers();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      watcherBusy = false;
    }
  }
  async function deleteWatcher(w) {
    if (!confirm(`Delete watcher "${w.name}"?`)) return;
    try {
      await api.del(`/watchers/${w.id}`);
      await loadWatchers();
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function loadWatcherServers() {
    watcherServers = await api.get("/servers").then((r) => r.map((s) => ({ id: s.id, name: s.name }))).catch(() => []);
  }
  // Kvasir watcher suggestions — the AI reads one server's rune type + recent log
  // and proposes rules; each is added explicitly, nothing is created on its own.
  let suggestServerId = $state("");
  let suggestBusy = $state(false);
  let watcherSuggestions = $state(null); // null = nothing requested yet, [] = none found
  // The server the current suggestions belong to — captured at request time, so a
  // later dropdown change can't silently re-target where "+ Add" lands.
  let suggestedFor = $state(null); // { id, name }
  async function suggestWatchers() {
    if (!suggestServerId) return;
    suggestBusy = true;
    watcherSuggestions = null;
    suggestedFor = { id: suggestServerId, name: watcherServers.find((s) => s.id === suggestServerId)?.name || "server" };
    try {
      const r = await api.post(`/servers/${suggestServerId}/watchers/suggest`);
      watcherSuggestions = r.suggestions || [];
    } catch (e) {
      toast(e.message, "error");
      suggestedFor = null;
    } finally {
      suggestBusy = false;
    }
  }
  async function addSuggestion(sg) {
    try {
      await api.post("/watchers", {
        server_id: suggestedFor.id, name: sg.name, pattern: sg.pattern,
        threshold: sg.threshold, window_secs: sg.window_secs, action: sg.action, enabled: true,
      });
      watcherSuggestions = watcherSuggestions.filter((x) => x !== sg);
      toast(`Watcher added to ${suggestedFor.name}`, "success");
      await loadWatchers();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  // Host migration — export/import a panel-settings bundle (Settings → System).
  const MIGRATE_GROUPS = [
    ["channels", "Notification channels"],
    ["ai", "Kvasir / AI config"],
    ["integrations", "Integrations (Steam, Discord, BattleMetrics)"],
    ["network", "Network (NPM, Cloudflare, UniFi, domains)"],
    ["rune_repos", "Rune repositories"],
    ["watchers", "Global watchers"],
    ["users", "Users & permissions"],
  ];
  let migrateSel = $state({ channels: true, ai: true, integrations: true, network: false, rune_repos: true, watchers: true, users: false });
  let migrateResult = $state(null);
  async function exportPanelSettings() {
    const include = Object.entries(migrateSel).filter(([, v]) => v).map(([k]) => k).join(",");
    try {
      const res = await fetch(`/api/panel/export?include=${include}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
      const blob = await res.blob();
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = "panel-settings.yggpanel.json";
      a.click();
      URL.revokeObjectURL(a.href);
      toast("Bundle downloaded — it contains secrets, treat it like a password", "info");
    } catch (e) {
      toast(e.message, "error");
    }
  }
  async function importPanelSettings(ev) {
    const file = ev.target.files?.[0];
    ev.target.value = "";
    if (!file) return;
    try {
      const bundle = JSON.parse(await file.text());
      migrateResult = await api.post("/panel/import", bundle);
      toast("Settings bundle merged", "success");
    } catch (e) {
      toast(e.message, "error");
    }
  }

  onMount(() => {
    load();
    loadTemplates();
    loadTokens();
    loadChannels();
    loadSteam();
    loadSteamKey();
    loadWatchers();
    loadWatcherServers();
    load2fa();
    loadPasskeys();
    loadBuild();
    loadAutoUpdate();
    loadOSUpdates();
    loadNetwork();
    loadStatusPage();
    loadBeacon();
    loadDiscord();
    loadBot();
    loadBackupVerify();
    loadUnifi();
    loadNpm();
    loadCf();
    loadAi();
  });

  async function create() {
    if (!form.name) return toast("Name required", "warn");
    try {
      await api.post("/backup/targets", { ...form, port: Number(form.port) || 0 });
      toast("Target created", "success");
      showCreate = false;
      form = blank();
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }

  async function test(t) {
    try {
      await api.post(`/backup/targets/${t.id}/test`);
      toast(`${t.name}: reachable`, "success");
    } catch (e) {
      toast(`${t.name}: ${e.message}`, "error");
    }
  }

  async function del(t) {
    if (!confirm(`Delete target "${t.name}"?`)) return;
    try {
      await api.del(`/backup/targets/${t.id}`);
      await load();
    } catch (e) {
      toast(e.message, "error");
    }
  }
</script>

<h1 class="text-2xl font-semibold mb-4">Settings</h1>

<div class="flex flex-wrap gap-1 border-b border-border mb-6">
  {#each settingsTabs as t}
    <button
      class="px-3 py-2 text-sm border-b-2 -mb-px {tab === t.id
        ? 'border-accent text-text font-medium'
        : 'border-transparent text-muted hover:text-text'}"
      onclick={() => selectTab(t.id)}
    >
      {t.label}
    </button>
  {/each}
</div>

{#if tab === "system"}
<!-- Panel updates -->
<h2 class="text-xl font-semibold mb-2">Panel updates</h2>
<div class="card p-4 mb-10">
  {#if build}
    <div class="flex items-center gap-3 flex-wrap">
      <span class="text-sm">Version <span class="font-mono">{build.version}</span></span>
      {#if build.update_available}
        <span class="badge bg-warn/20 text-warn">{build.latest} available</span>
      {:else}
        <span class="badge bg-accent2/20 text-accent">up to date</span>
      {/if}
      <div class="flex-1"></div>
      <button class="btn-ghost" disabled={checking} onclick={checkUpdates}
        title="Check GitHub for a newer Yggdrasil panel release.">
        {checking ? "Checking…" : "Check now"}
      </button>
      {#if build.update_available && build.can_self_update}
        <button class="btn-primary" disabled={updating} onclick={doUpdate}
          title="Download the new panel binary and restart into it. Game servers keep running; the panel is briefly unavailable during the swap.">
          {updating ? "Updating…" : `Update to ${build.latest}`}
        </button>
      {:else if build.update_available}
        <a class="btn-ghost" href={`${build.repo}/releases/latest`} target="_blank" rel="noopener">View release ↗</a>
      {/if}
    </div>
    {#if build.update_available && !build.can_self_update}
      <p class="text-xs text-muted mt-2">
        In-panel update isn't available here (dev build, or the update helper isn't installed). Re-run
        <code>install.sh</code> on the host once to enable one-click updates.
      </p>
    {/if}
    {#if updating}
      <p class="text-xs text-muted mt-2">Downloading &amp; installing, then the panel restarts — this page reconnects automatically.</p>
    {/if}

    <div class="border-t border-border mt-4 pt-4">
      <label class="inline-flex items-center gap-2 text-sm">
        <input type="checkbox" class="accent-accent2 w-4 h-4" bind:checked={autoUpdate.enabled} onchange={saveAutoUpdate} />
        <span>Automatically install updates</span>
      </label>
      <div class="flex items-center gap-2 mt-2 text-sm {autoUpdate.enabled ? '' : 'opacity-50'}">
        <span class="text-muted">Daily at</span>
        <select class="input w-auto py-1" bind:value={autoUpdate.hour} onchange={saveAutoUpdate} disabled={!autoUpdate.enabled}>
          {#each Array(24) as _, h}
            <option value={h}>{String(h).padStart(2, "0")}:00</option>
          {/each}
        </select>
        <span class="text-muted">server time</span>
      </div>
      <p class="text-xs text-muted mt-2">
        Off by default. When on, the panel checks daily and installs a newer release automatically — only the
        panel restarts (a few seconds); your game &amp; app servers keep running.
      </p>
    </div>
  {:else}
    <p class="text-sm text-muted">Checking for updates…</p>
  {/if}
</div>


<!-- Host OS updates (read-only: the panel reports, it doesn't apply) -->
<h2 class="text-xl font-semibold mb-2 mt-10">Operating system</h2>
<div class="card p-4 mb-10 max-w-xl">
  {#if !osUpd}
    <p class="text-sm text-muted">Checking…</p>
  {:else if !osUpd.supported}
    <p class="text-sm text-muted">{osUpd.note || "The panel can't read this host's update status."}</p>
  {:else}
    <div class="flex items-center gap-3 flex-wrap">
      <span class="text-sm">
        {#if osUpd.total === 0}
          <span class="badge bg-accent2/20 text-accent">up to date</span>
        {:else}
          <b>{osUpd.total}</b> update{osUpd.total === 1 ? "" : "s"} available
        {/if}
      </span>
      {#if osUpd.security != null && osUpd.security > 0}
        <span class="badge bg-warn/20 text-warn">{osUpd.security} security</span>
      {/if}
      {#if osUpd.reboot_required}
        <span class="badge bg-warn/20 text-warn">reboot needed</span>
      {/if}
    </div>

    {#if osUpd.reboot_required}
      <p class="text-xs text-muted mt-2">
        Something installed needs a reboot to take effect{osUpd.reboot_pkgs?.length
          ? ` — ${osUpd.reboot_pkgs.join(", ")}`
          : ""}. Rebooting stops every server, so pick your moment; autostart brings the running
        ones back.
      </p>
    {/if}

    {#if osUpd.security == null && osUpd.total > 0}
      <p class="text-xs text-muted mt-2">
        No security breakdown on this host — {osUpd.note || "install update-notifier-common for one"}.
      </p>
    {/if}

    {#if staleAptCache}
      <p class="text-xs text-warn mt-2">
        The package list was last refreshed {Math.round(osUpd.cache_age_hours)} hours ago, so this
        count may be out of date. <span class="font-mono">sudo apt update</span> refreshes it.
      </p>
    {/if}

    <div class="border-t border-border mt-4 pt-4 space-y-2">
      <p class="text-xs text-muted">
        Yggdrasil reports these but doesn't install them — that's a decision with your players on
        it. Apply them over SSH:
      </p>
      <div class="install-cmd flex items-center gap-2">
        <code class="text-xs font-mono text-accent2 break-all">sudo apt update &amp;&amp; sudo apt upgrade</code>
        <button class="btn-ghost text-xs ml-auto shrink-0" onclick={copyAptCmd}>Copy</button>
      </div>
      <p class="text-xs text-muted">
        Upgrading Docker restarts it, which stops every running server. Do it when it's quiet — a
        server's <b>Auto-restart</b> dialog will tell you its calmest hour.
      </p>
    </div>
  {/if}
</div>

<!-- Beacon (voluntary install ping) -->
<h2 class="text-xl font-semibold mb-2 mt-10">Beacon</h2>
<p class="text-muted mb-4 text-sm">
  Off by default. When on, this panel sends a small daily ping so the project can gauge how many
  installs are out there. It sends <b>only</b> the two values shown below — a random anonymous ID and the
  version — and <b>nothing else</b>: no IP, no server names, no addresses, no usage data.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
    <input type="checkbox" bind:checked={beacon.enabled} />
    Send an anonymous daily beacon
  </label>
  {#if beacon.enabled && beacon.last_sent}
    <div class="text-xs text-muted">Last ping: {beacon.last_sent}</div>
  {/if}
  {#if beacon.enabled && beacon.last_error}
    <!-- A beacon that can't reach its collector used to fail in total silence and
         retry forever. Say so where the switch is. -->
    <div class="rounded-lg bg-warn/10 border border-warn/35 p-3 text-xs">
      <div class="text-warn font-medium mb-1">The last ping didn't land</div>
      <div class="text-muted font-mono break-all">{beacon.last_error}</div>
      <div class="text-muted mt-1">
        {beacon.last_sent
          ? `Nothing has been counted since ${beacon.last_sent}.`
          : "This panel has never been counted."}
        It keeps retrying every 30 minutes. <b>Test</b> checks a URL before you commit to it.
      </div>
    </div>
  {/if}
  <div class="rounded-lg bg-panel2/50 border border-border p-3 text-xs font-mono break-all">
    <div class="text-muted mb-1">Exactly what is sent — nothing more:</div>
    {'{'} "instance_id": "{beacon.instance_id}", "version": "{beacon.version}" {'}'}
  </div>
  <div>
    <label class="label" for="beacon-url">Collector URL</label>
    <div class="flex gap-2">
      <input id="beacon-url" class="input" bind:value={beacon.url} placeholder="https://beacon.yggdrasilpanel.com/api/beacon" />
      <button class="btn-ghost shrink-0" onclick={testBeacon} disabled={testingBeacon}>
        {testingBeacon ? "Testing…" : "Test"}
      </button>
    </div>
    <p class="text-xs text-muted mt-1">
      Where the ping goes. Leave as the default unless you run your own collector. Use <b>Test</b> to check
      it's reachable.
    </p>
  </div>

  <!-- The collector role is a project-maintainer concern, not something every
       self-hoster needs — so it's enabled out-of-band (a DB/config setting on the
       one instance that gathers counts), not a toggle here. When this instance IS
       the collector, we surface the tallies. -->
  {#if beacon.receiver_enabled}
    <div class="border-t border-border pt-3">
      <div class="text-sm font-medium">📡 This instance is the beacon collector</div>
      <p class="text-xs text-muted mt-1">Receiving anonymous pings (ID + version only — never any IP).</p>
      {#if beaconStats}
        <div class="grid grid-cols-3 gap-2 mt-3 text-center">
          <div class="rounded-lg bg-panel2/50 border border-border p-2">
            <div class="text-lg font-semibold">{beaconStats.total}</div>
            <div class="text-[10px] text-muted uppercase tracking-wide">Total</div>
          </div>
          <div class="rounded-lg bg-panel2/50 border border-border p-2">
            <div class="text-lg font-semibold">{beaconStats.active_7d}</div>
            <div class="text-[10px] text-muted uppercase tracking-wide">Active 7d</div>
          </div>
          <div class="rounded-lg bg-panel2/50 border border-border p-2">
            <div class="text-lg font-semibold">{beaconStats.active_30d}</div>
            <div class="text-[10px] text-muted uppercase tracking-wide">Active 30d</div>
          </div>
        </div>
        {#if beaconStats.versions?.length}
          <div class="mt-2 text-xs text-muted">
            By version (30d): {beaconStats.versions.map((v) => `${v.version} ×${v.count}`).join(", ")}
          </div>
        {/if}
      {/if}

      <!-- Publishing the count is a second, separate decision: receiving pings
           privately and putting a number on a public page are not the same thing,
           so opting into one must not opt you into the other. -->
      <div class="mt-4 pt-3 border-t border-border">
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={beacon.public_count_enabled} />
          Publish the count at <code class="text-xs">/api/beacon/count</code>
        </label>
        <p class="text-xs text-muted mt-1">
          Unauthenticated, cached 60s. Reports installs seen in the last 30 days — not a running total,
          which would only ever climb.
        </p>
        {#if beacon.public_count_enabled}
          <div class="mt-3">
            <label class="label" for="beacon-count-min">Withhold below</label>
            <input
              id="beacon-count-min"
              class="input w-32"
              type="number"
              min="1"
              bind:value={beacon.public_count_min}
            />
            <p class="text-xs text-muted mt-1">
              Under this many, the endpoint answers <code>null</code> instead of a number. A count can
              fall for reasons that aren't real — a collector outage, pings that quietly stop arriving —
              and a small number published as fact is worse than no number.
              {#if beaconStats && beaconStats.active_30d < (beacon.public_count_min || 0)}
                <span class="block mt-1 text-warn">
                  Currently {beaconStats.active_30d} active — below the floor, so nothing is published yet.
                </span>
              {/if}
            </p>
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <button class="btn-primary" onclick={saveBeacon} disabled={savingBeacon}>
    {savingBeacon ? "Saving…" : "Save"}
  </button>
</div>

<!-- Host migration: move this panel's configuration to another Yggdrasil host -->
<h2 class="text-xl font-semibold mb-2">Host migration <span class="text-muted font-normal text-base">· move settings to another panel</span></h2>
<p class="text-muted mb-4 text-sm">
  Export the groups you pick as a settings bundle and import it on another running Yggdrasil host — it
  <b>merges</b> (nothing on the target is deleted; existing users/channels/repos are skipped, integrations
  and Kvasir config are applied). Secrets travel decrypted inside the file and are re-encrypted by the
  target, so <b>treat the file like a password</b>. Servers move individually with each server's
  Export/Import buttons — they carry their schedules, watchers and notification routing along. API tokens
  can't move (each panel signs its own); recreate them on the target. For a byte-for-byte whole-panel move
  incl. all server data, use <code>yggdrasil migrate</code> from the terminal.
</p>
<div class="card p-4 mb-10 space-y-4">
  <div class="flex flex-wrap gap-x-5 gap-y-2">
    {#each MIGRATE_GROUPS as g}
      <label class="inline-flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={migrateSel[g[0]]} /> {g[1]}
      </label>
    {/each}
  </div>
  <div class="flex items-center gap-3 flex-wrap">
    <button class="btn-primary text-sm" onclick={exportPanelSettings} disabled={!Object.values(migrateSel).some(Boolean)}>⬇ Export selected</button>
    <span class="text-xs text-muted">— or on the receiving panel —</span>
    <label class="btn-ghost text-sm cursor-pointer">
      ⬆ Import bundle…
      <input type="file" accept=".json,application/json" class="hidden" onchange={importPanelSettings} />
    </label>
  </div>
  {#if migrateResult}
    <div class="text-sm text-muted">
      Imported: {Object.entries(migrateResult).map(([k, v]) => `${k.replaceAll("_", " ")}: ${v}`).join(" · ") || "nothing new"}
    </div>
  {/if}
</div>
{/if}

{#if tab === "network"}
<!-- Network -->
<h2 class="text-xl font-semibold mb-2">Network</h2>
<p class="text-muted mb-4 text-sm">The public hostname players use to connect. It's shown as the connect address on each server's page.</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div>
    <label class="label" for="pubhost">Public hostname</label>
    <input id="pubhost" class="input" bind:value={network.public_hostname} placeholder="games.example.com" />
    <p class="text-muted text-xs mt-1">
      Leave empty to auto-detect your external IP{#if network.detected}{" "}(currently <span class="font-mono">{network.detected}</span>){/if}. Servers show
      <span class="font-mono">{network.effective || "your-host"}:&lt;port&gt;</span> as the connect address.
    </p>
    {#if network.internal}
      <p class="text-muted text-xs mt-1">
        This panel's internal (LAN) address: <span class="font-mono">{network.internal}</span> — the address to point NPM / port-forwards at.
      </p>
    {/if}
  </div>
  <div class="border-t border-border pt-3">
    <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
      <input type="checkbox" bind:checked={network.upnp_enabled} />
      Automatic UPnP port forwarding
    </label>
    <p class="text-muted text-xs mt-1">
      When a server starts, ask the router (via UPnP-IGD) to forward its ports, and release them on stop.
      Off by default — many routers (incl. UniFi) ship with UPnP disabled. If unavailable, forward ports manually.
    </p>
    <div class="flex items-center gap-2 mt-2">
      <button class="btn-ghost px-2 py-1 text-xs" onclick={checkUpnp} disabled={checkingUpnp}>
        {checkingUpnp ? "Checking…" : "Check gateway"}
      </button>
      {#if upnpStatus}
        {#if upnpStatus.gateway}
          <span class="text-xs text-accent">✓ Gateway found{#if upnpStatus.external_ip} · WAN {upnpStatus.external_ip}{/if}</span>
        {:else}
          <span class="text-xs text-muted">No UPnP gateway ({upnpStatus.message || "router UPnP off"})</span>
        {/if}
      {/if}
    </div>
  </div>
  <div class="border-t border-border pt-3">
    <label class="label" for="bmtoken">BattleMetrics API token (optional)</label>
    <input
      id="bmtoken"
      class="input"
      type="password"
      bind:value={network.battlemetrics_token}
      placeholder={network.battlemetrics_enabled ? "•••••••• (saved — type to replace)" : "paste a token to raise rate limits"}
    />
    <p class="text-muted text-xs mt-1">
      Optional. Public server status works without a token; a token (battlemetrics.com → Account → API)
      just raises the rate limit. Set a server's BattleMetrics ID on its Settings tab to show a live status badge.
    </p>
  </div>
  <button class="btn-primary" onclick={saveNetwork} disabled={savingNetwork}>
    {savingNetwork ? "Saving…" : "Save"}
  </button>
</div>

<!-- Public status page -->
<h2 class="text-xl font-semibold mb-2">Status page</h2>
<p class="text-muted mb-4 text-sm">
  A public, read-only up/down board at
  <a class="text-accent" href="/status" target="_blank" rel="noopener">/status</a>
  so players can check whether a server is online — no login. Nothing is shown until you enable it here
  <em>and</em> turn on “Show on the public status page” for each server (its Settings tab). Only name, game
  and online/players state are exposed — never addresses, ports or config.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
    <input type="checkbox" bind:checked={statusPage.enabled} />
    Enable the public status page
  </label>
  <div>
    <label class="label" for="sptitle">Page title</label>
    <input id="sptitle" class="input" bind:value={statusPage.title} placeholder="Server Status" />
  </div>
  <div class="flex items-center gap-3">
    <button class="btn-primary" onclick={saveStatusPage} disabled={savingStatusPage}>
      {savingStatusPage ? "Saving…" : "Save"}
    </button>
    {#if statusPage.enabled}
      <a class="text-sm text-accent" href="/status" target="_blank" rel="noopener">Open status page ↗</a>
    {/if}
  </div>
</div>

<!-- UniFi port forwarding -->
<h2 class="text-xl font-semibold mb-2">UniFi port forwarding</h2>
<p class="text-muted mb-4 text-sm">
  Automatically create/remove WAN port-forward rules on your UniFi gateway when servers start/stop.
  Use a dedicated local admin account. Credentials are encrypted at rest.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div class="grid sm:grid-cols-2 gap-3">
    <div>
      <label class="label" for="uurl">Controller URL</label>
      <input id="uurl" class="input" bind:value={unifi.url} placeholder="https://192.168.1.1" />
    </div>
    <div>
      <label class="label" for="usite">Site</label>
      <input id="usite" class="input" bind:value={unifi.site} placeholder="default" />
    </div>
    <div>
      <label class="label" for="uuser">Username</label>
      <input id="uuser" class="input" bind:value={unifi.username} placeholder="admin" autocomplete="off" />
    </div>
    <div>
      <label class="label" for="upass">Password</label>
      <input id="upass" class="input" type="password" bind:value={unifi.password}
        placeholder={unifi.configured ? "•••••••• (unchanged)" : "password"} autocomplete="new-password" />
    </div>
  </div>
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
    <input type="checkbox" bind:checked={unifi.enabled} />
    Enable automatic UniFi port forwarding
  </label>
  <div class="flex gap-2">
    <button class="btn-primary" onclick={saveUnifi} disabled={savingUnifi}>{savingUnifi ? "Saving…" : "Save"}</button>
    <button class="btn-ghost" onclick={testUnifi} disabled={testingUnifi}>{testingUnifi ? "Testing…" : "Test connection"}</button>
  </div>
</div>

{/if}

{#if tab === "domains"}
<!-- NPM subdomain routing -->
<h2 class="text-xl font-semibold mb-2">Nginx Proxy Manager (subdomains)</h2>
<p class="text-muted mb-4 text-sm">
  Give HTTP app servers their own subdomain. Yggdrasil creates/removes a proxy host in your
  Nginx Proxy Manager when those servers start/stop, routing <code>sub.your-domain</code> →
  the server's web port. Requires a wildcard DNS record (<code>*.domain → your public IP</code>)
  and ports 80/443 forwarded to NPM. Games (raw UDP) are unaffected. Credentials encrypted at rest.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div class="grid sm:grid-cols-2 gap-3">
    <div>
      <label class="label" for="npmurl">NPM URL</label>
      <input id="npmurl" class="input" bind:value={npm.url} placeholder="http://192.168.1.158:81" />
    </div>
    <div>
      <label class="label" for="npmbase">Base domain</label>
      <input id="npmbase" class="input" bind:value={npm.base_domain} placeholder="yggdrasilpanel.com" />
    </div>
    <div>
      <label class="label" for="npmemail">Admin email</label>
      <input id="npmemail" class="input" bind:value={npm.email} placeholder="admin@example.com" autocomplete="off" />
    </div>
    <div>
      <label class="label" for="npmpass">Admin password</label>
      <input id="npmpass" class="input" type="password" bind:value={npm.password}
        placeholder={npm.configured ? "•••••••• (unchanged)" : "password"} autocomplete="new-password" />
    </div>
    <div>
      <label class="label" for="npminternal">Internal host</label>
      <input id="npminternal" class="input" bind:value={npm.internal_host} placeholder="(auto: this VM's LAN IP)" />
    </div>
    <div>
      <label class="label" for="npmle">Let's Encrypt email</label>
      <input id="npmle" class="input" bind:value={npm.le_email} placeholder="certs@example.com" autocomplete="off" />
    </div>
  </div>
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
    <input type="checkbox" bind:checked={npm.enabled} />
    Enable subdomain routing via NPM
  </label>
  <div class="flex gap-2">
    <button class="btn-primary" onclick={saveNpm} disabled={savingNpm}>{savingNpm ? "Saving…" : "Save"}</button>
    <button class="btn-ghost" onclick={testNpm} disabled={testingNpm}>{testingNpm ? "Testing…" : "Test connection"}</button>
  </div>
</div>

<!-- Cloudflare Tunnel subdomain routing -->
<h2 class="text-xl font-semibold mb-2">Cloudflare Tunnel (subdomains)</h2>
<p class="text-muted mb-4 text-sm">
  Expose HTTP app servers on a subdomain through a Cloudflare Tunnel — no port forwarding,
  no public IP needed (the tunnel is outbound). Yggdrasil adds/removes a tunnel ingress rule
  and a proxied <code>CNAME</code> when those servers start/stop. Prereqs: a tunnel created in
  the Cloudflare dashboard with the <code>cloudflared</code> connector running (you can launch it
  from the <b>cloudflared</b> rune), and an API token with <b>Account → Cloudflare Tunnel: Edit</b>
  and <b>Zone → DNS: Edit</b>. Token encrypted at rest. Uses the same per-server Subdomain field as NPM.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div class="grid sm:grid-cols-2 gap-3">
    <div>
      <label class="label" for="cftoken">API token</label>
      <input id="cftoken" class="input" type="password" bind:value={cf.token}
        placeholder={cf.configured ? "•••••••• (unchanged)" : "token"} autocomplete="new-password" />
    </div>
    <div>
      <label class="label" for="cfbase">Base domain</label>
      <input id="cfbase" class="input" bind:value={cf.base_domain} placeholder="example.com" />
    </div>
    <div>
      <label class="label" for="cfacct">Account ID</label>
      <input id="cfacct" class="input" bind:value={cf.account_id} placeholder="32-char account id" autocomplete="off" />
    </div>
    <div>
      <label class="label" for="cftunnel">Tunnel ID</label>
      <input id="cftunnel" class="input" bind:value={cf.tunnel_id} placeholder="tunnel uuid" autocomplete="off" />
    </div>
    <div>
      <label class="label" for="cfzone">Zone ID (optional)</label>
      <input id="cfzone" class="input" bind:value={cf.zone_id} placeholder="(auto-resolved from base domain)" autocomplete="off" />
    </div>
    <div>
      <label class="label" for="cfinternal">Internal host</label>
      <input id="cfinternal" class="input" bind:value={cf.internal_host} placeholder="(auto: this VM's LAN IP)" />
    </div>
  </div>
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
    <input type="checkbox" bind:checked={cf.enabled} />
    Enable subdomain routing via Cloudflare Tunnel
  </label>
  <div class="flex gap-2">
    <button class="btn-primary" onclick={saveCf} disabled={savingCf}>{savingCf ? "Saving…" : "Save"}</button>
    <button class="btn-ghost" onclick={testCf} disabled={testingCf}>{testingCf ? "Testing…" : "Test connection"}</button>
  </div>
</div>

{/if}

{#if tab === "security"}
<!-- Two-factor auth -->
<h2 class="text-xl font-semibold mb-2">Two-factor authentication</h2>
<p class="text-muted mb-4 text-sm">Protect your account with a TOTP authenticator app.</p>
<div class="card p-4 mb-10">
  {#if twofa.enabled}
    <div class="flex items-center gap-3">
      <span class="badge bg-accent2/20 text-accent">enabled</span>
      <span class="flex-1 text-sm text-muted">A code from your authenticator is required at login.</span>
      <button class="btn-danger" onclick={disable2fa}>Disable</button>
    </div>
  {:else if twofaSetup}
    <p class="text-sm mb-2">
      Add this secret to your authenticator app (or scan the otpauth URI), then enter a code to
      confirm:
    </p>
    <code class="block break-all text-xs bg-black/40 p-2 rounded mb-1">{twofaSetup.secret}</code>
    <code class="block break-all text-[10px] text-muted bg-black/30 p-2 rounded mb-3">{twofaSetup.uri}</code>
    <div class="flex gap-2">
      <input class="input font-mono tracking-widest" bind:value={twofaCode} placeholder="123456" inputmode="numeric" />
      <button class="btn-primary" onclick={enable2fa}>Confirm & enable</button>
      <button class="btn-ghost" onclick={() => (twofaSetup = null)}>Cancel</button>
    </div>
  {:else}
    <div class="flex items-center gap-3">
      <span class="badge bg-border text-muted">disabled</span>
      <span class="flex-1 text-sm text-muted">Add a second factor to your login.</span>
      <button class="btn-primary" onclick={start2fa}
        title="Set up time-based one-time codes (TOTP). You'll scan a secret into an authenticator app and confirm a code.">Enable 2FA</button>
    </div>
  {/if}
</div>

<!-- Passkeys (WebAuthn) -->
<h2 class="text-xl font-semibold mb-2">Passkeys</h2>
<p class="text-muted mb-4 text-sm">
  Sign in without a password using Touch ID, Windows Hello, a phone, or a security key — a passkey is
  itself two-factor (your device + biometrics/PIN).
  {#if !canPasskey}<span class="text-danger">Only available when the panel is opened over HTTPS on a domain (not plain HTTP/LAN).</span>{/if}
</p>
<div class="card p-4 mb-10">
  {#if passkeys.length}
    <div class="divide-y divide-border mb-3">
      {#each passkeys as pk}
        <div class="flex items-center gap-3 py-2">
          <span>🔑</span>
          <div class="flex-1">
            <div class="text-sm">{pk.name}</div>
            <div class="text-xs text-muted">
              added {pk.created_at?.slice(0, 10)}{pk.last_used ? ` · last used ${pk.last_used.slice(0, 10)}` : ""}
            </div>
          </div>
          <button class="btn-ghost px-2 py-1" onclick={() => renamePasskey(pk)}>Rename</button>
          <button class="btn-danger px-2 py-1" onclick={() => delPasskey(pk)}>Remove</button>
        </div>
      {/each}
    </div>
  {:else}
    <p class="text-sm text-muted mb-3">No passkeys registered yet.</p>
  {/if}
  <button class="btn-primary" disabled={!canPasskey || pkBusy} onclick={addPasskey}
    title="Register a passkey (fingerprint, face, security key or device PIN) so you can sign in without a password.">
    {pkBusy ? "Waiting for passkey…" : "Add a passkey"}
  </button>
</div>

{/if}

{#if tab === "realms"}
<RealmManager />
{/if}

{#if tab === "integrations"}
<!-- Discord status board -->
<h2 class="text-xl font-semibold mb-2">Discord status board</h2>
<p class="text-muted mb-4 text-sm">
  Keep a live, auto-updating status embed in a Discord channel — up/down and player counts for the same
  servers you share on the <a class="text-accent" href="/status" target="_blank" rel="noopener">public status page</a>.
  No bot to host: it uses an <b>incoming webhook</b> and edits the same message in place (never spams the channel).
  In Discord: Server Settings → Integrations → Webhooks → New Webhook → pick a channel → Copy URL.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div>
    <label class="label" for="discord-wh">Webhook URL</label>
    <input
      id="discord-wh"
      class="input"
      type="password"
      bind:value={discordWebhook}
      placeholder={discord.configured ? "•••••••• (saved — paste to replace)" : "https://discord.com/api/webhooks/…"}
      autocomplete="off"
    />
  </div>
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
    <input type="checkbox" bind:checked={discord.enabled} />
    Keep the status board updated (every few minutes)
  </label>
  <div class="flex items-center gap-3">
    <button class="btn-primary" onclick={saveDiscord} disabled={savingDiscord}>
      {savingDiscord ? "Saving…" : "Save"}
    </button>
    {#if discord.configured}
      <button class="btn-ghost" onclick={postDiscord} disabled={postingDiscord}>
        {postingDiscord ? "Posting…" : "Post now"}
      </button>
    {/if}
  </div>
</div>

<!-- Discord control bot -->
<h2 class="text-xl font-semibold mb-2">Discord control bot</h2>
<p class="text-muted mb-4 text-sm">
  A two-way bot with slash commands: <code>/status</code>, <code>/servers</code> and <code>/players</code>
  (read-only, anywhere the bot can see) plus <code>/start</code>, <code>/stop</code>, <code>/restart</code>
  — which only work in the <b>control channel</b> below. Create a bot at
  <a class="text-accent" href="https://discord.com/developers/applications" target="_blank" rel="noopener">discord.com/developers/applications</a>
  (Bot → Reset Token), invite it with the <code>bot</code> + <code>applications.commands</code> scopes, then paste its token here.
  It connects outbound — no port-forward needed.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div>
    <label class="label" for="bot-token">Bot token</label>
    <input
      id="bot-token"
      class="input"
      type="password"
      bind:value={botToken}
      placeholder={bot.configured ? "•••••••• (saved — paste to replace)" : "paste the bot token"}
      autocomplete="off"
    />
  </div>
  <div>
    <label class="label" for="bot-channel">Control channel ID</label>
    <input id="bot-channel" class="input font-mono" bind:value={bot.control_channel} placeholder="e.g. 1527678395423391957" />
    <p class="text-muted text-xs mt-1">
      In Discord, enable Developer Mode (Settings → Advanced), then right-click your control channel → Copy Channel ID.
      Leave empty to allow <b>read-only commands only</b> — start/stop/restart stay disabled until a channel is set.
    </p>
  </div>
  <div class="flex items-center gap-3">
    <button class="btn-primary" onclick={saveBot} disabled={savingBot}>
      {savingBot ? "Saving…" : "Save"}
    </button>
    {#if bot.configured}
      <span class="text-xs text-accent">● Bot configured</span>
    {/if}
  </div>
</div>

<!-- Kvasir — AI assistant (advisory) -->
<h2 class="text-xl font-semibold mb-2">Kvasir <span class="text-muted font-normal text-base">· AI assistant</span></h2>
<p class="text-muted mb-4 text-sm">
  Kvasir is the panel's optional AI helper (named after the wisest being in Norse myth) — it powers the
  “what happened while I was away” digest, the log explainer, the ops health briefing, and (opt-in)
  proactive monitoring that can explain, propose, or safely fix problems as they happen. Bring your own
  provider and key — nothing is sent anywhere you don't configure here. By default Kvasir is advisory and
  <b>never acts on a server by itself</b>; it only takes safe, reversible actions if you turn on Active
  help below, and never deletes, wipes or reconfigures autonomously. Log data (player names, events) is
  sent to the endpoint below only when Kvasir needs it.
</p>
<div class="card p-4 mb-10 space-y-3">
  <div class="grid sm:grid-cols-2 gap-3">
    <div>
      <label class="label" for="ai-prov">Provider</label>
      <select id="ai-prov" class="input" bind:value={ai.provider}
        title="Which API dialect to speak. Anthropic uses the Messages API; everything else uses the OpenAI /chat/completions shape (OpenRouter, DeepSeek, Mistral, Ollama, or any compatible gateway via 'custom').">
        {#each aiProviders as p}<option value={p}>{p}</option>{/each}
      </select>
    </div>
    <div>
      <label class="label" for="ai-model">Model</label>
      <input id="ai-model" class="input" bind:value={ai.model} autocomplete="off"
        placeholder="e.g. gpt-4o-mini, claude-…, deepseek-chat"
        title="The exact model id for your provider." />
    </div>
  </div>
  <div>
    <label class="label" for="ai-base">Base URL <span class="text-muted">(optional — required for “custom”)</span></label>
    <input id="ai-base" class="input" bind:value={ai.base_url} autocomplete="off"
      placeholder="(uses the provider default; e.g. http://192.168.1.x:11434/v1 for Ollama)"
      title="Override the API base. Leave blank to use the provider's default endpoint. For a LAN Ollama, use the host's LAN IP, not localhost." />
  </div>
  <div>
    <label class="label" for="ai-key">API key</label>
    <input id="ai-key" class="input" type="password" bind:value={ai.api_key} autocomplete="new-password"
      placeholder={ai.configured ? "•••••••• (unchanged)" : "your provider API key (not needed for local Ollama)"}
      title="Stored encrypted; never shown again. Leave blank to keep the existing key." />
  </div>
  <div class="text-xs text-muted uppercase tracking-wide pt-1">AI levels (turn off in tiers)</div>
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer"
    title="Master switch. When off, all AI features are hidden and no data is sent anywhere.">
    <input type="checkbox" bind:checked={ai.enabled} />
    <span><b>Advisory</b> — digests, error explainer, config review (read-only)</span>
  </label>
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer"
    title="Higher tier: let the AI PROPOSE server actions (restart / safe-restart / stop / start) from a natural-language request. You always review and confirm before anything runs; the AI can never wipe, delete or reconfigure. Off by default.">
    <input type="checkbox" bind:checked={ai.actions_enabled} disabled={!ai.enabled} />
    <span><b>Actions</b> — let AI propose restart/stop/start (you always confirm) <span class="text-warn">·  opt-in</span></span>
  </label>
  <div class="flex flex-wrap items-center gap-2 text-sm">
    <label class="inline-flex items-center gap-2 cursor-pointer"
      title="Send a daily 'anything need attention?' ops digest to your notification channels (Telegram/Discord/webhook/email).">
      <input type="checkbox" bind:checked={ai.digest_enabled} disabled={!ai.enabled} />
      Daily ops digest to notifications
    </label>
    {#if ai.digest_enabled}
      <span class="text-muted">at</span>
      <select class="input w-auto" bind:value={ai.digest_hour}>
        {#each Array(24) as _, h}<option value={h}>{String(h).padStart(2, "0")}:00</option>{/each}
      </select>
      <span class="text-muted">server time</span>
    {/if}
  </div>

  <!-- Proactive monitoring -->
  <div class="border-t border-border pt-3 mt-1">
    <div class="flex flex-wrap items-center gap-2 text-sm">
      <span class="font-medium">Proactive monitoring</span>
      <select class="input w-auto" bind:value={ai.proactive_level} disabled={!ai.enabled}>
        <option value={0}>Off</option>
        <option value={1}>Passive — explain events</option>
        <option value={2}>Active observe — explain + propose a fix</option>
        <option value={3}>Active help — apply safe fixes itself</option>
      </select>
    </div>
    <p class="text-muted text-xs mt-1">
      Kvasir watches for problems and, at your chosen level, explains them, proposes a fix, or applies a
      <b>safe</b> one itself. At <b>Active help</b> it will restart a stuck server and — after an out-of-memory
      kill — <b>raise its memory limit and restart it</b> (bounded: never below the current limit, at most 2×,
      and never above 80% of host RAM). Hard limits at every level: it <b>never</b> deletes, wipes, changes
      environment variables or other config on its own (those are only ever proposed), auto-fixes are
      rate-limited, and everything it does is audited and announced. <span class="text-warn">Active help is opt-in.</span>
    </p>
    {#if ai.proactive_level > 0}
      <div class="flex flex-wrap gap-3 mt-2 text-sm">
        <span class="text-muted">Watch:</span>
        {#each PROACTIVE_TRIGGERS as [key, label]}
          <label class="inline-flex items-center gap-1.5 cursor-pointer">
            <input type="checkbox" checked={hasTrigger(key)} onchange={() => toggleTrigger(key)} disabled={!ai.enabled} />
            {label}
          </label>
        {/each}
      </div>
    {/if}
  </div>

  <div class="flex gap-2">
    <button class="btn-primary" onclick={saveAi} disabled={savingAi}>{savingAi ? "Saving…" : "Save"}</button>
    <button class="btn-ghost" onclick={testAi} disabled={testingAi}
      title="Send a tiny test prompt using the saved config to verify the key and endpoint. Save first.">{testingAi ? "Testing…" : "Test connection"}</button>
  </div>
</div>

<!-- Kvasir Watchers -->
<h2 class="text-xl font-semibold mb-2">Kvasir Watchers <span class="text-muted font-normal text-base">· proactive log rules</span></h2>
<p class="text-muted mb-4 text-sm">
  Watch any server's log for a pattern — a burst of failed logins, an HTTP 5xx spike, a database error, an
  out-of-memory line — and act when it appears too often in a window. A watcher <b>notifies</b>, and with
  action <b>Kvasir</b> it also hands the matched lines to the AI to explain what's happening and propose a
  fix. Reads the container's own log, so it works for apps and games alike. Scanned every ~30s.
</p>
<div class="card p-4 mb-10 space-y-4">
  <div class="flex items-center gap-2 flex-wrap">
    <button class="btn-primary text-sm" onclick={() => newWatcher()}>+ New watcher</button>
    <span class="text-xs text-muted">Presets:</span>
    {#each WATCHER_PRESETS as p}
      <button class="btn-ghost text-xs" onclick={() => newWatcher(p)}>{p.name}</button>
    {/each}
  </div>

  <!-- Kvasir suggestions: pick a server, the AI reads its rune type + recent log
       and proposes tailored rules. Nothing is added without a click. -->
  <div class="flex items-center gap-2 flex-wrap">
    <span class="text-sm">✨ Let Kvasir suggest rules for</span>
    <select class="input w-auto text-sm" bind:value={suggestServerId}>
      <option value="">choose a server…</option>
      {#each watcherServers as sv}<option value={sv.id}>{sv.name}</option>{/each}
    </select>
    <button class="btn-ghost text-sm" onclick={suggestWatchers} disabled={suggestBusy || !suggestServerId}
      title="Kvasir reads the server's app type and a sample of its recent log, and proposes watcher rules. Needs the AI configured above.">
      {suggestBusy ? "Reading the log…" : "Suggest"}</button>
  </div>
  {#if watcherSuggestions !== null && suggestedFor}
    {#if watcherSuggestions.length}
      <div class="text-sm font-medium">Kvasir's suggestions for <span class="text-accent">{suggestedFor.name}</span>:</div>
      <div class="card divide-y divide-border">
        {#each watcherSuggestions as sg}
          <div class="flex items-center gap-3 px-3 py-2 flex-wrap">
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium flex items-center gap-2">
                {sg.name}
                {#if sg.action === "kvasir"}<span class="badge bg-accent/20 text-accent">🧠 Kvasir</span>{/if}
              </div>
              <div class="text-xs text-muted font-mono truncate">{sg.pattern}</div>
              <div class="text-[11px] text-muted">{sg.threshold}× in {sg.window_secs}s · {sg.reason}</div>
            </div>
            <button class="btn-primary text-xs" onclick={() => addSuggestion(sg)} title="Add this watcher to {suggestedFor.name}">+ Add to {suggestedFor.name}</button>
          </div>
        {/each}
      </div>
      <p class="text-[11px] text-muted">Suggestions are validated (the pattern must be a working regex) but AI-written — skim before adding.</p>
    {:else}
      <div class="text-sm text-muted">Kvasir had nothing to add for <b>{suggestedFor.name}</b> — its existing rules may already cover the log (duplicates are dropped). Trying again can turn up different ideas.</div>
    {/if}
  {/if}

  {#if watcherForm}
    <div class="card p-3 border-l-4 border-accent space-y-3 bg-panel2/40">
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <label class="text-sm">Name
          <input class="input mt-1" bind:value={watcherForm.name} placeholder="Failed logins" />
        </label>
        <label class="text-sm">Applies to
          <select class="input mt-1" bind:value={watcherForm.server_id}>
            <option value="">Every server</option>
            {#each watcherServers as sv}<option value={sv.id}>{sv.name}</option>{/each}
          </select>
        </label>
      </div>
      <label class="text-sm block">Pattern <span class="text-muted">(regular expression, matched per log line)</span>
        <input class="input mt-1 font-mono text-xs" bind:value={watcherForm.pattern} placeholder="(?i)failed login|authentication failure" />
      </label>
      <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 items-end">
        <label class="text-sm">Threshold
          <input class="input mt-1" type="number" min="1" bind:value={watcherForm.threshold} />
        </label>
        <label class="text-sm">Window (s)
          <input class="input mt-1" type="number" min="1" bind:value={watcherForm.window_secs} />
        </label>
        <label class="text-sm">Action
          <select class="input mt-1" bind:value={watcherForm.action}>
            <option value="notify">Notify</option>
            <option value="kvasir">Notify + Kvasir</option>
          </select>
        </label>
        <label class="inline-flex items-center gap-2 text-sm pb-2">
          <input type="checkbox" bind:checked={watcherForm.enabled} /> Enabled
        </label>
      </div>
      <div class="flex gap-2">
        <button class="btn-primary text-sm" onclick={saveWatcher} disabled={watcherBusy}>{watcherBusy ? "Saving…" : "Save watcher"}</button>
        <button class="btn-ghost text-sm" onclick={() => (watcherForm = null)}>Cancel</button>
      </div>
      <p class="text-[11px] text-muted">Fires at most once per 10 min per watcher. "Notify + Kvasir" needs the AI configured above and proactive monitoring on.</p>
    </div>
  {/if}

  {#if watchers.length}
    <div class="divide-y divide-border">
      {#each watchers as w}
        <div class="flex items-center gap-3 py-2 flex-wrap">
          <div class="min-w-0 flex-1">
            <div class="text-sm font-medium flex items-center gap-2">
              {w.name}
              {#if !w.enabled}<span class="badge bg-border text-muted">off</span>{/if}
              {#if w.action === "kvasir"}<span class="badge bg-accent/20 text-accent">🧠 Kvasir</span>{/if}
              {#if w.source === "rune"}<span class="badge bg-panel2 border border-border text-muted" title="Shipped by this server's rune — edit or disable freely; a reinstall only restores it if deleted.">ᚱ rune</span>{/if}
            </div>
            <div class="text-xs text-muted font-mono truncate">{w.pattern}</div>
            <div class="text-[11px] text-muted">
              {w.threshold}× in {w.window_secs}s · {w.server_id ? (watcherServers.find((s) => s.id === w.server_id)?.name || "one server") : "every server"}
              {#if w.last_fired}· last fired {w.last_fired}{/if}
            </div>
          </div>
          <button class="btn-ghost text-xs" onclick={() => editWatcher(w)}>Edit</button>
          <button class="btn-ghost text-xs text-danger" onclick={() => deleteWatcher(w)}>Delete</button>
        </div>
      {/each}
    </div>
  {:else}
    <div class="text-sm text-muted">No watchers yet — add one, or start from a preset.</div>
  {/if}
</div>

<!-- Steam authorization -->
<h2 class="text-xl font-semibold mb-2">Steam account</h2>
<p class="text-muted mb-4 text-sm">
  Most dedicated servers download anonymously. A few (e.g. <b>DayZ</b>) need a Steam account that
  owns the game. Authorize one <b>once</b> here — the login is cached so Steam Guard isn't asked
  again. Tip: use a dedicated account for the host and <b>email-based</b> Steam Guard (the mobile
  authenticator asks for a new code every login and breaks unattended updates). Your password and
  code are never stored or logged.
</p>
<div class="card p-4 mb-10">
  {#if steam?.configured}
    <div class="flex items-center gap-3">
      <div class="flex-1">
        <div class="font-medium">{steam.username}
          <span class="badge {steam.authorized ? 'bg-accent2/20 text-accent' : 'bg-warn/20 text-warn'} ml-1">
            {steam.authorized ? "authorized" : "not authorized"}
          </span>
        </div>
        {#if steam.authorized_at}<div class="text-xs text-muted">since {steam.authorized_at}</div>{/if}
      </div>
      <button class="btn-danger" onclick={forgetSteam}
        title="Remove the stored Steam authorization + credential cache. Games that need a Steam login (e.g. DayZ) won't be able to install or update until you re-authorize.">Forget</button>
    </div>
  {:else if steamStep === 1}
    <div class="text-xs text-muted mb-2">
      Step 1 of 2 — enter your credentials. Steam will email a Guard code to the account.
    </div>
    <div class="grid sm:grid-cols-2 gap-3">
      <div>
        <label class="label" for="st-user">Username</label>
        <input id="st-user" class="input" bind:value={steamForm.username} autocomplete="off" />
      </div>
      <div>
        <label class="label" for="st-pass">Password</label>
        <input id="st-pass" class="input" type="password" bind:value={steamForm.password} autocomplete="off" />
      </div>
    </div>
    <button class="btn-primary mt-3" onclick={sendSteamCode} disabled={steamBusy}>
      {steamBusy ? "Contacting Steam… (can take a minute)" : "Send Steam Guard code"}
    </button>
  {:else}
    <div class="text-xs text-muted mb-2">
      Step 2 of 2 — enter the Steam Guard code from <b>{steamForm.username}</b>'s email.
    </div>
    <div class="max-w-xs">
      <label class="label" for="st-guard">Steam Guard code</label>
      <input id="st-guard" class="input font-mono tracking-widest" bind:value={steamForm.guard_code}
        autocomplete="off" placeholder="XXXXX" />
    </div>
    <div class="flex gap-2 mt-3">
      <button class="btn-primary" onclick={authorizeSteam} disabled={steamBusy}>
        {steamBusy ? "Authorizing…" : "Authorize"}
      </button>
      <button class="btn-ghost" onclick={() => { steamStep = 1; steamForm.guard_code = ''; }}>Back</button>
    </div>
  {/if}
</div>

<h2 class="text-xl font-semibold mb-2">Steam Web API key</h2>
<p class="text-muted mb-4 text-sm">
  Optional. Lets the <b>DayZ Mods</b> tab search the Steam Workshop by name. Get a free key at
  <a class="text-accent hover:underline" href="https://steamcommunity.com/dev/apikey" target="_blank" rel="noopener">steamcommunity.com/dev/apikey</a>.
  Without it you can still add mods by pasting their Workshop id. Separate from the SteamCMD login above
  (which downloads mods). Stored encrypted, write-only.
</p>
<div class="card p-4 mb-10 max-w-xl space-y-3">
  <div class="text-sm">
    Status: {#if steamKey.configured}<span class="text-accent">a key is configured</span>{:else}<span class="text-muted">no key set</span>{/if}
  </div>
  <div class="flex gap-2">
    <input class="input flex-1 font-mono" type="password" placeholder={steamKey.configured ? "Enter a new key to replace it" : "Paste your Steam Web API key"} bind:value={steamKeyInput} autocomplete="off" />
    <button class="btn-primary shrink-0" onclick={saveSteamKey} disabled={steamKeyBusy || !steamKeyInput.trim()}>Save</button>
    {#if steamKey.configured}
      <button class="btn-ghost shrink-0" onclick={() => { steamKeyInput = ''; saveSteamKey(); }} disabled={steamKeyBusy}>Clear</button>
    {/if}
  </div>
</div>

<div class="flex items-center justify-between mb-2">
  <h2 class="text-xl font-semibold">Backup targets</h2>
  <button class="btn-primary" onclick={() => (showCreate = true)}>+ New target</button>
</div>
<p class="text-muted mb-4 text-sm">
  Where backups are stored. <b>Local</b> also covers an NFS or CIFS share already mounted on the
  host — just point the path at the mountpoint. <b>SFTP</b> and <b>SMB</b> connect directly.
  Credentials are encrypted at rest.
</p>

<div class="card divide-y divide-border">
  {#if targets.length === 0}
    <div class="p-4 text-muted text-sm">No backup targets yet.</div>
  {/if}
  {#each targets as t}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1">
        <div class="font-medium">{t.name} <span class="badge bg-border text-muted ml-1">{t.type}</span></div>
        <div class="text-xs text-muted">
          {t.host ? t.host + " · " : ""}{t.path || "(default)"}
          {#if t.keep_n || t.keep_days}
            · retention: {t.keep_n ? `keep ${t.keep_n}` : ""}{t.keep_n && t.keep_days ? " / " : ""}{t.keep_days
              ? `${t.keep_days}d`
              : ""}
          {/if}
        </div>
      </div>
      <button class="btn-ghost" onclick={() => test(t)}>Test</button>
      <button class="btn-danger" onclick={() => del(t)}>Delete</button>
    </div>
  {/each}
</div>

<!-- Nightly backup verification -->
<h2 class="text-xl font-semibold mb-2 mt-10">Backup verification</h2>
<p class="text-muted mb-4 text-sm">
  Once a day, check each server's most recent backup actually decompresses — so a corrupt backup is caught
  proactively and you get a notification, instead of finding out mid-restore. Off by default: it downloads
  each latest archive from its target (bandwidth on remote SFTP/SMB stores).
</p>
<div class="card p-4 mb-4 max-w-xl">
  <label class="inline-flex items-center gap-2 text-sm cursor-pointer">
    <input type="checkbox" bind:checked={backupVerify.enabled} onchange={saveBackupVerify} disabled={savingBackupVerify} />
    Verify the latest backup of each server nightly
  </label>
  {#if backupVerify.last_run}
    <p class="text-xs text-muted mt-2">Last run: {backupVerify.last_run}</p>
  {/if}
</div>

<!-- Notifications -->
<div class="flex items-center justify-between mt-10 mb-2">
  <h2 class="text-xl font-semibold">Notifications</h2>
  <button class="btn-primary" onclick={() => (showNotify = true)}>+ Add channel</button>
</div>
<p class="text-muted mb-4 text-sm">
  Get notified on backups (done/failed) and servers starting/stopping, via Telegram, Discord, or a
  generic webhook. Tokens are encrypted at rest.
</p>
<div class="card divide-y divide-border">
  {#if channels.length === 0}
    <div class="p-4 text-muted text-sm">No notification channels.</div>
  {/if}
  {#each channels as c}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1 min-w-0">
        <span class="font-medium capitalize">{c.type}</span>
        {#if c.server_id}
          <span class="badge bg-panel2 border border-border text-muted ml-2">{c.server_name || "one server"}</span>
        {:else}
          <span class="badge bg-panel2 border border-border text-muted ml-2">Global</span>
        {/if}
      </div>
      <button class="btn-ghost" onclick={() => testChannel(c)}>Test</button>
      <button class="btn-danger" onclick={() => deleteChannel(c)}>Delete</button>
    </div>
  {/each}
</div>

{#if showNotify}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-md p-5 space-y-3">
      <h2 class="text-lg font-semibold">Add notification channel</h2>
      <div>
        <label class="label" for="n-type">Type</label>
        <select id="n-type" class="input" bind:value={notifyForm.type}>
          <option value="telegram">Telegram</option>
          <option value="discord">Discord webhook</option>
          <option value="webhook">Generic webhook</option>
          <option value="email">Email (SMTP)</option>
        </select>
      </div>
      <div>
        <label class="label" for="n-scope">Scope</label>
        <select id="n-scope" class="input" bind:value={notifyForm.server_id}>
          <option value="">Global — every notification</option>
          {#each notifyServers as srv}<option value={srv.id}>Only {srv.name}</option>{/each}
        </select>
        <p class="text-muted text-xs mt-1">A server-scoped channel only receives that server's events (start/stop, crashes, watchdog, backups, alarms). Global channels still see everything.</p>
      </div>
      {#if notifyForm.type === "telegram"}
        <div>
          <label class="label" for="n-token">Bot token</label>
          <input id="n-token" class="input" bind:value={notifyForm.token} />
        </div>
        <div>
          <label class="label" for="n-chat">Chat ID</label>
          <input id="n-chat" class="input" bind:value={notifyForm.chat_id} />
        </div>
      {:else if notifyForm.type === "email"}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="n-host">SMTP host</label>
            <input id="n-host" class="input" bind:value={notifyForm.host} />
          </div>
          <div>
            <label class="label" for="n-port">Port</label>
            <input id="n-port" class="input" type="number" bind:value={notifyForm.port} />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="n-user">Username</label>
            <input id="n-user" class="input" bind:value={notifyForm.username} autocomplete="off" />
          </div>
          <div>
            <label class="label" for="n-pass">Password</label>
            <input id="n-pass" class="input" type="password" bind:value={notifyForm.password} autocomplete="off" />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="n-from">From</label>
            <input id="n-from" class="input" bind:value={notifyForm.from} placeholder="ygg@example.com" />
          </div>
          <div>
            <label class="label" for="n-to">To</label>
            <input id="n-to" class="input" bind:value={notifyForm.to} placeholder="you@example.com" />
          </div>
        </div>
      {:else}
        <div>
          <label class="label" for="n-url">Webhook URL</label>
          <input id="n-url" class="input" bind:value={notifyForm.url} />
        </div>
      {/if}
      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showNotify = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={createChannel}>Add</button>
      </div>
    </div>
  </div>
{/if}

<!-- API tokens -->
<div class="flex items-center justify-between mt-10 mb-2">
  <h2 class="text-xl font-semibold">API tokens</h2>
</div>
<p class="text-muted mb-4 text-sm">
  Drive Yggdrasil from automation (scripts, a home AI). A token acts as you and is shown only once.
</p>
{#if createdToken}
  <div class="card border-accent2/40 bg-accent2/10 p-4 mb-3">
    <div class="text-sm text-accent mb-1">New token — copy it now, it won't be shown again:</div>
    <code class="block break-all text-xs bg-black/40 p-2 rounded">{createdToken}</code>
    <button class="btn-ghost mt-2" onclick={() => (createdToken = null)}>Dismiss</button>
  </div>
{/if}
<div class="flex gap-2 mb-3">
  <input class="input" bind:value={newTokenName} placeholder="Token name (e.g. home-ai)" />
  <button class="btn-primary" onclick={createToken}>Create</button>
</div>
<div class="card divide-y divide-border">
  {#if tokens.length === 0}
    <div class="p-4 text-muted text-sm">No API tokens.</div>
  {/if}
  {#each tokens as t}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1">
        <div class="font-medium">{t.name}</div>
        <div class="text-xs text-muted">
          created {t.created_at}{t.last_used_at ? ` · last used ${t.last_used_at}` : " · never used"}
        </div>
      </div>
      <button class="btn-danger" onclick={() => deleteToken(t)}>Delete</button>
    </div>
  {/each}
</div>

<!-- Message templates -->
<div class="flex items-center justify-between mt-10 mb-2">
  <h2 class="text-xl font-semibold">In-game message templates</h2>
  <button class="btn-primary" onclick={() => (editing = { name: "", body: "" })}>+ New template</button>
</div>
<p class="text-muted mb-4 text-sm">
  Used by scheduled "in-game message" tasks. Variables like <code>{"{{minutes}}"}</code> and
  <code>{"{{server_name}}"}</code> are substituted at send time. The body is the full console/RCON
  command (e.g. <code>say …</code>).
</p>

<div class="card divide-y divide-border">
  {#each templates as t}
    <div class="flex items-center gap-3 px-4 py-3">
      <div class="flex-1 min-w-0">
        <div class="font-medium">{t.name} {#if t.builtin}<span class="badge bg-border text-muted ml-1">built-in</span>{/if}</div>
        <div class="text-xs text-muted font-mono truncate">{t.body}</div>
      </div>
      <button class="btn-ghost" onclick={() => (editing = { id: t.id, name: t.name, body: t.body })}>Edit</button>
      {#if !t.builtin}
        <button class="btn-danger" onclick={() => deleteTemplate(t)}>Delete</button>
      {/if}
    </div>
  {/each}
</div>

{#if editing}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-lg p-5 space-y-3">
      <h2 class="text-lg font-semibold">{editing.id ? "Edit" : "New"} template</h2>
      <div>
        <label class="label" for="m-name">Name</label>
        <input id="m-name" class="input" bind:value={editing.name} />
      </div>
      <div>
        <label class="label" for="m-body">Body (console/RCON command)</label>
        <input id="m-body" class="input font-mono" bind:value={editing.body} placeholder="say Restarting in {{minutes}} min" />
      </div>
      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (editing = null)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={saveTemplate}>Save</button>
      </div>
    </div>
  </div>
{/if}

{#if showCreate}
  <div class="fixed inset-0 z-50 bg-black/60 grid place-items-center p-4">
    <div class="card w-full max-w-lg max-h-[90vh] overflow-auto p-5 space-y-3">
      <h2 class="text-lg font-semibold">New backup target</h2>
      <div>
        <label class="label" for="t-name">Name</label>
        <input id="t-name" class="input" bind:value={form.name} />
      </div>
      <div>
        <label class="label" for="t-type">Type</label>
        <select id="t-type" class="input" bind:value={form.type}>
          <option value="local">Local / NFS / CIFS mount</option>
          <option value="sftp">SFTP</option>
          <option value="smb">SMB / CIFS (direct)</option>
        </select>
      </div>

      {#if form.type !== "local"}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="t-host">Host</label>
            <input id="t-host" class="input" bind:value={form.host} />
          </div>
          <div>
            <label class="label" for="t-port">Port (optional)</label>
            <input id="t-port" class="input" type="number" bind:value={form.port} />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="t-user">Username</label>
            <input id="t-user" class="input" bind:value={form.username} />
          </div>
          <div>
            <label class="label" for="t-pass">Password</label>
            <input id="t-pass" class="input" type="password" bind:value={form.password} />
          </div>
        </div>
      {/if}

      {#if form.type === "smb"}
        <div>
          <label class="label" for="t-share">Share name</label>
          <input id="t-share" class="input" bind:value={form.share} />
        </div>
      {/if}

      <div>
        <label class="label" for="t-path">{form.type === "local" ? "Directory path" : "Remote path"}</label>
        <input id="t-path" class="input" bind:value={form.path} placeholder={form.type === "local" ? "/mnt/backups" : "backups"} />
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="t-keepn">Keep latest N (0 = ∞)</label>
          <input id="t-keepn" class="input" type="number" bind:value={form.keep_n} />
        </div>
        <div>
          <label class="label" for="t-keepd">Keep days (0 = ∞)</label>
          <input id="t-keepd" class="input" type="number" bind:value={form.keep_days} />
        </div>
      </div>

      <div class="flex gap-2 pt-2">
        <button class="btn-ghost flex-1" onclick={() => (showCreate = false)}>Cancel</button>
        <button class="btn-primary flex-1" onclick={create}>Create target</button>
      </div>
    </div>
  </div>
{/if}
{/if}
