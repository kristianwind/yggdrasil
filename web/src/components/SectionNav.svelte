<script>
  // A sticky "on this page" list for long pages.
  //
  // The entries are read from the DOM rather than declared, because the pages this
  // serves (Settings, a server's detail view) grow a section at a time and a
  // hand-kept list would be wrong within a week. Whatever <h2> headings are
  // rendered right now are the sections, and each gets an id if it hasn't one.
  //
  // `rescan` is a value to watch — pass the active tab, so switching tabs rebuilds
  // the list after the new content has rendered.
  let { container = null, rescan = null, minItems = 3 } = $props();

  let items = $state([]);
  let activeId = $state("");
  let observer = null;

  const slug = (s) =>
    "sec-" +
    (s || "")
      .toLowerCase()
      .normalize("NFD")
      .replace(/[\u0300-\u036f]/g, "") // strip combining marks so æøå give stable ids
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 48);

  function build() {
    observer?.disconnect();
    if (!container) {
      items = [];
      return;
    }
    const found = [...container.querySelectorAll("h2")];
    const seen = new Set();
    const list = [];
    for (const el of found) {
      const label = (el.textContent || "").trim();
      if (!label) continue;
      if (!el.id) {
        let id = slug(label);
        // Two sections can legitimately share a title; keep ids unique anyway.
        let n = 2;
        while (seen.has(id) || document.getElementById(id)) id = `${slug(label)}-${n++}`;
        el.id = id;
      }
      seen.add(el.id);
      // scroll-margin so a sticky header never covers the heading we jump to
      el.style.scrollMarginTop = "1.5rem";
      list.push({ id: el.id, label });
    }
    items = list;
    activeId = list[0]?.id ?? "";

    if (list.length >= minItems && "IntersectionObserver" in window) {
      // Highlight the heading nearest the top of the viewport.
      observer = new IntersectionObserver(
        (entries) => {
          const visible = entries
            .filter((e) => e.isIntersecting)
            .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
          if (visible[0]) activeId = visible[0].target.id;
        },
        { rootMargin: "-10% 0px -70% 0px", threshold: 0 },
      );
      for (const el of found) if (el.id) observer.observe(el);
    }
  }

  // Rebuild after the tab's content has actually rendered.
  $effect(() => {
    void rescan;
    void container;
    const t = setTimeout(build, 0);
    return () => {
      clearTimeout(t);
      observer?.disconnect();
    };
  });

  function go(id) {
    const el = document.getElementById(id);
    if (!el) return;
    el.scrollIntoView({ behavior: "smooth", block: "start" });
    activeId = id;
  }
</script>

{#if items.length >= minItems}
  <!-- Hidden on narrow screens: there the page is one column and a side rail would
       squeeze the content it is meant to help with. -->
  <nav class="hidden xl:block w-56 shrink-0" aria-label="On this page">
    <div class="sticky top-4">
      <div class="text-[11px] uppercase tracking-wide text-muted mb-2 px-2">On this page</div>
      <ul class="space-y-0.5 border-l border-border">
        {#each items as it}
          <li>
            <button
              class="w-full text-left text-sm px-2 py-1 -ml-px border-l-2 truncate {activeId === it.id
                ? 'border-accent text-text font-medium'
                : 'border-transparent text-muted hover:text-text'}"
              title={it.label}
              onclick={() => go(it.id)}
            >
              {it.label}
            </button>
          </li>
        {/each}
      </ul>
    </div>
  </nav>
{/if}
