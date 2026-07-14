<script>
  // A compact, self-contained SVG line+area chart. No external libs.
  // props: values (number[]), label, unit, color, format (fn), height
  let { values = [], label = "", unit = "", color = "rgb(var(--c-accent2))", format = (v) => v, height = 56 } = $props();

  const W = 240; // viewBox width; scales to container via width:100%

  let clean = $derived(values.filter((v) => typeof v === "number" && !isNaN(v) && v >= 0));
  let max = $derived(clean.length ? Math.max(...clean, 0.0001) : 1);
  let cur = $derived(clean.length ? clean[clean.length - 1] : null);
  let peak = $derived(clean.length ? Math.max(...clean) : null);

  // Build the polyline points across the full width; y inverted (0 at top).
  let pts = $derived.by(() => {
    const n = values.length;
    if (n < 2) return "";
    const pad = 3;
    const h = height - pad * 2;
    return values
      .map((v, i) => {
        const x = (i / (n - 1)) * W;
        const val = typeof v === "number" && v >= 0 ? v : 0;
        const y = pad + h - (val / max) * h;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
      })
      .join(" ");
  });
  let areaPts = $derived(pts ? `0,${height} ${pts} ${W},${height}` : "");
  const uid = "spk" + Math.random().toString(36).slice(2, 8);
</script>

<div class="card p-3">
  <div class="flex items-baseline justify-between mb-1">
    <span class="text-xs uppercase tracking-wide text-muted">{label}</span>
    <span class="text-sm font-semibold" style="color:{color}">
      {cur == null ? "—" : format(cur)}{unit}
    </span>
  </div>
  {#if values.length >= 2}
    <svg viewBox="0 0 {W} {height}" preserveAspectRatio="none" class="w-full" style="height:{height}px" role="img" aria-label="{label} history">
      <defs>
        <linearGradient id={uid} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color={color} stop-opacity="0.28" />
          <stop offset="100%" stop-color={color} stop-opacity="0" />
        </linearGradient>
      </defs>
      <polygon points={areaPts} fill="url(#{uid})" />
      <polyline points={pts} fill="none" stroke={color} stroke-width="1.5" vector-effect="non-scaling-stroke" stroke-linejoin="round" stroke-linecap="round" />
    </svg>
    <div class="text-[10px] text-muted mt-0.5">peak {peak == null ? "—" : format(peak)}{unit}</div>
  {:else}
    <div class="text-xs text-muted py-4 text-center" style="height:{height}px">Collecting… (samples every 5 min)</div>
  {/if}
</div>
