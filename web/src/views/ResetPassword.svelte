<script>
  import { route, navigate } from "../lib/router.js";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import PasswordField from "../components/PasswordField.svelte";

  // The token arrives in the emailed link: #/reset?token=…
  const token = $derived($route.query.get("token") || "");

  let password = $state("");
  let confirm = $state("");
  let busy = $state(false);
  let done = $state(false);

  async function submit(e) {
    e.preventDefault();
    if (password !== confirm) return toast("Passwords don’t match", "warn");
    busy = true;
    try {
      await api.post("/auth/reset", { token, password }, { allow401: true });
      done = true;
      toast("Password updated — you can sign in now", "success");
    } catch (err) {
      toast(err.message || "Reset failed", "error");
    } finally {
      busy = false;
    }
  }
</script>

<div class="min-h-screen grid place-items-center p-4">
  <div class="card p-6 w-full max-w-sm space-y-4">
    <div class="text-center">
      <div class="text-3xl">🌳</div>
      <h1 class="text-xl font-semibold mt-1">Choose a new password</h1>
    </div>

    {#if !token}
      <div class="rounded-md bg-panel2 border border-border p-4 text-sm text-muted">
        This reset link is missing its token. Request a new one from the sign-in page.
      </div>
      <button class="btn-primary w-full" onclick={() => navigate("/login")}>Back to sign in</button>
    {:else if done}
      <div class="rounded-md bg-panel2 border border-border p-4 text-sm text-muted">
        Your password has been updated and any old sessions were signed out.
      </div>
      <button class="btn-primary w-full" onclick={() => navigate("/login")}>Sign in</button>
    {:else}
      <form onsubmit={submit} class="space-y-4">
        <div>
          <label class="label" for="np">New password</label>
          <PasswordField id="np" bind:value={password} autocomplete="new-password" />
          <p class="text-xs text-muted mt-1">At least 12 characters.</p>
        </div>
        <div>
          <label class="label" for="cp">Confirm password</label>
          <PasswordField id="cp" bind:value={confirm} autocomplete="new-password" />
        </div>
        <button class="btn-primary w-full" disabled={busy || !password || !confirm}>
          {busy ? "Updating…" : "Update password"}
        </button>
        <button type="button" class="btn-ghost w-full" onclick={() => navigate("/login")}>Cancel</button>
      </form>
    {/if}
  </div>
</div>
