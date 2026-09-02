<script>
  // Auto-generates a settings form from a gameskill's `variables`.
  // Two-way binds into `values` (a plain object keyed by variable key).
  import PasswordField from "./PasswordField.svelte";
  import { seedVarDefaults } from "../lib/varDefaults.js";

  let { variables = [], values = $bindable({}) } = $props();

  // Seed EAGERLY, before the first render — not only in an $effect, which runs
  // after it. See varDefaults.js for why the timing matters.
  seedVarDefaults(variables, values);

  // …and again whenever the rune's variable list changes.
  $effect(() => seedVarDefaults(variables, values));

  // Treat string vars that look like a secret (password / pass / secret / token)
  // as password fields — gives them the show/generate/copy controls. A rune can
  // also opt in explicitly with `secret: true` on the variable.
  const isSecret = (v) =>
    (v.type === "string" || !v.type) &&
    (v.secret === true || /pass(word)?|secret|token/i.test(`${v.key} ${v.name || ""}`));

  // What the 🎲 should produce for a rune secret.
  //
  // It has to satisfy the rune, or the one button that exists to help hands you a
  // value the app rejects at boot. Plausible wants SECRET_KEY_BASE >= 32 bytes and
  // measures it itself; the old fixed 20 failed that check every time. So read the
  // minimum back out of the variable's own `pattern` when it states one, and never
  // go below 32 — a rune secret is pasted, not typed, so length costs nothing.
  const genLength = (v) => {
    const m = /\{(\d+),/.exec(v.pattern || "");
    return Math.max(32, m ? Number(m[1]) : 0);
  };
</script>

<div class="space-y-3">
  {#each variables as v}
    <div>
      <label class="label" for={`var-${v.key}`}>
        {v.name || v.key}
        {#if v.required}<span class="text-danger">*</span>{/if}
      </label>

      {#if v.type === "select"}
        <select id={`var-${v.key}`} class="input" bind:value={values[v.key]}>
          {#each v.options as opt}
            <option value={opt}>{opt}</option>
          {/each}
        </select>
      {:else if v.type === "bool"}
        <label class="inline-flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={values[v.key]} class="accent-accent2 w-4 h-4" />
          <span class="text-muted">Enabled</span>
        </label>
      {:else if v.type === "int"}
        <!-- min/max come from the rune. The server enforces them too — this is so
             you find out while typing rather than on submit. -->
        <input id={`var-${v.key}`} class="input" type="number" bind:value={values[v.key]}
          min={v.min ?? undefined} max={v.max ?? undefined} />
        {#if v.min != null || v.max != null}
          <p class="text-xs text-muted mt-1">
            {v.min != null && v.max != null
              ? `Between ${v.min} and ${v.max}.`
              : v.min != null
                ? `At least ${v.min}.`
                : `At most ${v.max}.`}
          </p>
        {/if}
      {:else if isSecret(v)}
        <!-- symbols={false}: rune secrets end up inside connection strings, where
             "@" and ":" are separators. See PasswordField. -->
        <PasswordField id={`var-${v.key}`} bind:value={values[v.key]}
          length={genLength(v)} symbols={false} />
      {:else}
        <input id={`var-${v.key}`} class="input" bind:value={values[v.key]} />
      {/if}
    </div>
  {/each}
</div>
