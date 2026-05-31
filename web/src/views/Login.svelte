<script>
  import { login } from "../lib/auth.js";
  import { toast } from "../lib/toast.js";

  let username = $state("");
  let password = $state("");
  let busy = $state(false);

  async function submit(e) {
    e.preventDefault();
    busy = true;
    try {
      await login(username, password);
      location.hash = "#/";
    } catch (err) {
      toast(err.message === "unauthorized" ? "Invalid credentials" : err.message, "error");
    } finally {
      busy = false;
    }
  }
</script>

<div class="min-h-screen grid place-items-center p-4">
  <form onsubmit={submit} class="card p-6 w-full max-w-sm space-y-4">
    <div class="text-center">
      <div class="text-3xl">🌳</div>
      <h1 class="text-xl font-semibold mt-1">Yggdrasil</h1>
      <p class="text-muted text-sm">Sign in to manage your game servers</p>
    </div>
    <div>
      <label class="label" for="u">Username</label>
      <input id="u" class="input" bind:value={username} autocomplete="username" />
    </div>
    <div>
      <label class="label" for="p">Password</label>
      <input id="p" class="input" type="password" bind:value={password} autocomplete="current-password" />
    </div>
    <button class="btn-primary w-full" disabled={busy}>
      {busy ? "Signing in…" : "Sign in"}
    </button>
  </form>
</div>
