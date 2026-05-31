<script>
  // Auto-generates a settings form from a gameskill's `variables`.
  // Two-way binds into `values` (a plain object keyed by variable key).
  let { variables = [], values = $bindable({}) } = $props();

  // Seed defaults for any unset keys.
  $effect(() => {
    for (const v of variables) {
      if (values[v.key] === undefined && v.default !== undefined && v.default !== null) {
        values[v.key] = v.default;
      }
    }
  });
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
        <input id={`var-${v.key}`} class="input" type="number" bind:value={values[v.key]} />
      {:else}
        <input id={`var-${v.key}`} class="input" bind:value={values[v.key]} />
      {/if}
    </div>
  {/each}
</div>
