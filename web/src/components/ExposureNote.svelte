<script>
  // Every control in Yggdrasil that opens something to a network the operator
  // does not control carries one of these, and they all read the same way:
  // WHAT is opened, to WHOM, and how it is closed again.
  //
  // One component rather than a sentence written at each call site, because
  // the sentences drift. The UPnP toggle described the protocol in detail and
  // never said that turning it on lets anyone on the internet reach the
  // server; auto-forward explained how to switch it OFF without mentioning
  // that it is ON by default.
  //
  // `safe` is not decoration. A set of controls where every one carries a
  // warning teaches the reader to skip all of them; saying plainly that an
  // outbound tunnel opens no inbound port is what makes the others worth
  // reading, and it points at the option an operator should usually prefer.
  let {
    opens = "",     // what this exposes, in one clause
    reach = "",     // who can reach it
    undo = "",      // how to close it again
    danger = false, // reachable from the internet with nothing authenticating in front
    safe = false,   // opens no inbound port at all
  } = $props();

  // Written out in full, never assembled. Tailwind generates a class only if it
  // can SEE the literal string in the source, and this project has no safelist,
  // so `border-{tone}` compiles to a note with no colour at all -- styling that
  // silently does nothing, on the one component whose whole job is to be
  // noticed. Measured: `border-danger` appears in the built CSS, `border-` + a
  // variable does not.
  const styles = {
    safe: { box: "border-accent bg-accent/5", text: "text-accent", mark: "✓", head: "Opens no inbound port" },
    danger: { box: "border-danger bg-danger/5", text: "text-danger", mark: "⚠️", head: "Reachable from the internet" },
    warn: { box: "border-warn bg-warn/5", text: "text-warn", mark: "⚠️", head: "Exposes this beyond your network" },
  };
  const s = $derived(safe ? styles.safe : danger ? styles.danger : styles.warn);
</script>

<div class="mt-2 rounded-sm border-l-4 px-3 py-2 text-xs {s.box}">
  <p class="font-semibold {s.text}">{s.mark} {s.head}</p>
  {#if opens}<p class="mt-1 text-text">{opens}</p>{/if}
  {#if reach}<p class="mt-1 text-muted">{reach}</p>{/if}
  {#if undo}<p class="mt-1 text-muted">{undo}</p>{/if}
</div>
