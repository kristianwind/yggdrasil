<script>
  import { toasts } from "../lib/toast.js";
  const color = {
    info: "bg-panel2 border-border",
    success: "bg-accent2/20 border-accent2 text-accent",
    error: "bg-danger/20 border-danger text-danger",
    warn: "bg-warn/20 border-warn text-warn",
  };
</script>

<div class="fixed bottom-4 right-4 z-50 flex flex-col gap-2 max-w-sm">
  {#each $toasts as t (t.id)}
    <div class="card border px-4 py-2 text-sm shadow-lg {color[t.type] || color.info}">
      {t.message}
    </div>
  {/each}
</div>
