<script>
  // Auto-generates a settings form from a gameskill's `variables`.
  // Two-way binds into `values` (a plain object keyed by variable key).
  import PasswordField from "./PasswordField.svelte";

  let { variables = [], values = $bindable({}) } = $props();

  // Seed defaults for any unset keys.
  $effect(() => {
    for (const v of variables) {
      if (values[v.key] === undefined && v.default !== undefined && v.default !== null) {
        values[v.key] = v.default;
      }
      // Normalize bool vars: env round-trips through env_json as the *string*
      // "true"/"false", and a non-empty string like "false" is truthy — which
      // renders the checkbox as checked even when the stored value is false (so
      // saving silently writes "false" back). Coerce to a real boolean so the
      // toggle reflects — and saves — the actual value.
      if (v.type === "bool" && typeof values[v.key] === "string") {
        values[v.key] = values[v.key] === "true";
      }
    }
  });

  // Treat string vars that look like a secret (password / pass / secret / token)
  // as password fields — gives them the show/generate/copy controls. A rune can
  // also opt in explicitly with `secret: true` on the variable.
  const isSecret = (v) =>
    (v.type === "string" || !v.type) &&
    (v.secret === true || /pass(word)?|secret|token/i.test(`${v.key} ${v.name || ""}`));
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
        <PasswordField id={`var-${v.key}`} bind:value={values[v.key]} />
      {:else}
        <input id={`var-${v.key}`} class="input" bind:value={values[v.key]} />
      {/if}
    </div>
  {/each}
</div>
