<script>
  // A bare inline sparkline — no card, no label — for embedding in a table row or
  // card to show a server's recent CPU trend at a glance.
  let { values = [], color = "rgb(var(--c-accent2))", width = 60, height = 16 } = $props();

  const clean = $derived(values.filter((v) => typeof v === "number" && v >= 0));
  const max = $derived(clean.length ? Math.max(...clean, 0.0001) : 1);
  const pts = $derived.by(() => {
    const n = values.length;
    if (n < 2) return "";
    return values
      .map((v, i) => {
        const x = (i / (n - 1)) * width;
        const val = typeof v === "number" && v >= 0 ? v : 0;
        const y = height - 1 - (val / max) * (height - 2);
        return `${x.toFixed(1)},${y.toFixed(1)}`;
      })
      .join(" ");
  });
</script>

{#if pts}
  <svg
    {width}
    {height}
    viewBox="0 0 {width} {height}"
    class="inline-block align-middle shrink-0"
    aria-hidden="true"
  >
    <polyline points={pts} fill="none" stroke={color} stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" />
  </svg>
{/if}
