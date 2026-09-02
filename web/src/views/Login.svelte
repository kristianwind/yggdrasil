<script>
  // The login page is the only thing a visitor to the public demo sees first, and
  // it has no way to know the credentials. Without this they meet a password box
  // with no password and leave — which is an expensive way to lose the person the
  // demo exists for. /api/version is public and already unauthenticated.
  import { onMount as onMountDemo } from "svelte";
  let isDemo = $state(false);
  let demoLogin = $state("");
  onMountDemo(async () => {
    try {
      const v = await (await fetch("/api/version")).json();
      isDemo = !!v?.demo;
      demoLogin = v?.demo_login || "";
    } catch { /* not fatal — the form still works */ }
  });
  import { login } from "../lib/auth.js";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { loginWithPasskey, passkeysSupported } from "../lib/webauthn.js";

  let username = $state("");
  let password = $state("");
  let code = $state("");
  let needCode = $state(false);
  let busy = $state(false);
  const canPasskey = passkeysSupported();

  // Forgot-password flow, shown inline instead of the sign-in form.
  let forgot = $state(false);
  let forgotIdent = $state("");
  let forgotSent = $state(false);

  async function submitForgot(e) {
    e.preventDefault();
    busy = true;
    try {
      // The server always answers the same way (no account enumeration), so we
      // show a generic confirmation regardless of whether a mail actually went out.
      await api.post("/auth/forgot", { identifier: forgotIdent }, { allow401: true });
      forgotSent = true;
    } catch (err) {
      toast(err.message || "Something went wrong", "error");
    } finally {
      busy = false;
    }
  }

  function backToLogin() {
    forgot = false;
    forgotSent = false;
    forgotIdent = "";
  }

  async function submit(e) {
    e.preventDefault();
    busy = true;
    try {
      await login(username, password, code);
      location.hash = "#/";
    } catch (err) {
      if (err.message === "2fa_required") {
        needCode = true;
        toast("Enter your 2FA code", "info");
      } else {
        toast(err.message === "unauthorized" ? "Invalid credentials" : err.message, "error");
      }
    } finally {
      busy = false;
    }
  }

  async function passkeyLogin() {
    busy = true;
    try {
      const res = await loginWithPasskey();
      if (!res || !res.token) throw new Error("passkey login failed");
      location.hash = "#/";
    } catch (err) {
      // A user cancelling the browser prompt throws NotAllowedError/AbortError.
      if (err.name === "NotAllowedError" || err.name === "AbortError") {
        toast("Passkey sign-in cancelled", "info");
      } else {
        toast(err.message || "Passkey sign-in failed", "error");
      }
    } finally {
      busy = false;
    }
  }
</script>

<div class="min-h-screen grid place-items-center p-4">
  {#if forgot}
    <form onsubmit={submitForgot} class="card p-6 w-full max-w-sm space-y-4">
      <div class="text-center">
        <div class="text-3xl">🌳</div>
        <h1 class="text-xl font-semibold mt-1">Reset your password</h1>
        <p class="text-muted text-sm">We’ll email a reset link if the account has an address on file</p>
      </div>
      {#if forgotSent}
        <div class="rounded-md bg-panel2 border border-border p-4 text-sm text-muted">
          If an account matches, a password-reset link is on its way. Check your inbox
          (and spam) — the link is valid for 1&nbsp;hour.
        </div>
        <button type="button" class="btn-primary w-full" onclick={backToLogin}>Back to sign in</button>
      {:else}
        <div>
          <label class="label" for="fi">Username or email</label>
          <input id="fi" class="input" bind:value={forgotIdent} autocomplete="username" />
        </div>
        <button class="btn-primary w-full" disabled={busy || !forgotIdent}>
          {busy ? "Sending…" : "Send reset link"}
        </button>
        <button type="button" class="btn-ghost w-full" disabled={busy} onclick={backToLogin}>Back to sign in</button>
      {/if}
    </form>
  {:else}
    <form onsubmit={submit} class="card p-6 w-full max-w-sm space-y-4">
      <div class="text-center">
        <div class="text-3xl">🌳</div>
        <h1 class="text-xl font-semibold mt-1">Yggdrasil Panel</h1>
        <p class="text-muted text-sm">Sign in to manage your game &amp; app servers</p>
      </div>
      {#if isDemo}
        <div class="rounded-lg border border-warn/40 bg-warn/10 p-3 text-sm">
          <p class="font-semibold text-warn">This is a public demo.</p>
          <p class="text-muted text-xs mt-1">
            Everything you see is real and live — and nothing can be changed, so click freely.
          </p>
          {#if demoLogin}
            <p class="text-xs mt-2 font-mono text-text">{demoLogin}</p>
          {/if}
        </div>
      {/if}
      <div>
        <label class="label" for="u">Username</label>
        <input id="u" class="input" bind:value={username} autocomplete="username" />
      </div>
      <div>
        <label class="label" for="p">Password</label>
        <input id="p" class="input" type="password" bind:value={password} autocomplete="current-password" />
      </div>
      {#if needCode}
        <div>
          <label class="label" for="c">2FA code</label>
          <input id="c" class="input font-mono tracking-widest" bind:value={code} inputmode="numeric"
            placeholder="123456" autocomplete="one-time-code" />
        </div>
      {/if}
      <button class="btn-primary w-full" disabled={busy}>
        {busy ? "Signing in…" : "Sign in"}
      </button>

      {#if canPasskey}
        <div class="flex items-center gap-3 text-muted text-xs">
          <div class="h-px bg-border flex-1"></div>
          or
          <div class="h-px bg-border flex-1"></div>
        </div>
        <button type="button" class="btn-secondary w-full" disabled={busy} onclick={passkeyLogin}>
          🔑 Sign in with a passkey
        </button>
      {/if}

      <div class="text-center">
        <button type="button" class="text-xs text-muted hover:text-text underline"
          onclick={() => (forgot = true)}>Forgot password?</button>
      </div>
    </form>
  {/if}
</div>
