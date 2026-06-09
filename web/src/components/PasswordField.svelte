<script>
  // A password input with show/hide, a strong-password generator, and a copy
  // button. Bind `value`; drop-in replacement for a bare <input type="password">.
  import { toast } from "../lib/toast.js";

  let {
    value = $bindable(""),
    id = "",
    placeholder = "",
    autocomplete = "off",
    length = 20,
  } = $props();

  let show = $state(false);

  // Crypto-strong, no visually ambiguous chars (0/O, 1/l/I), guaranteed one of
  // each class so it passes typical complexity rules.
  function generate() {
    const lower = "abcdefghijkmnpqrstuvwxyz";
    const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ";
    const digit = "23456789";
    const sym = "!@#$%^&*-_=+";
    const all = lower + upper + digit + sym;
    const rand = (n) => crypto.getRandomValues(new Uint32Array(1))[0] % n;
    const pick = (s) => s[rand(s.length)];
    const out = [pick(lower), pick(upper), pick(digit), pick(sym)];
    while (out.length < length) out.push(pick(all));
    // Fisher–Yates shuffle so the guaranteed chars aren't always first.
    for (let i = out.length - 1; i > 0; i--) {
      const j = rand(i + 1);
      [out[i], out[j]] = [out[j], out[i]];
    }
    value = out.join("");
    show = true;
  }

  async function copy() {
    const text = value || "";
    if (!text) return;
    try {
      // navigator.clipboard needs a secure context (https/localhost). The panel
      // is often reached over plain http on the LAN, so fall back to execCommand.
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      toast("Copied to clipboard", "success");
    } catch {
      toast("Couldn't copy — select and copy manually", "error");
    }
  }
</script>

<div class="flex gap-1">
  <input
    {id}
    class="input flex-1"
    type={show ? "text" : "password"}
    bind:value
    {placeholder}
    {autocomplete}
  />
  <button type="button" class="btn-ghost px-2 shrink-0" title={show ? "Hide" : "Show"} aria-label={show ? "Hide password" : "Show password"} onclick={() => (show = !show)}>
    {show ? "🙈" : "👁"}
  </button>
  <button type="button" class="btn-ghost px-2 shrink-0" title="Generate a strong password" aria-label="Generate password" onclick={generate}>🎲</button>
  <button type="button" class="btn-ghost px-2 shrink-0" title="Copy to clipboard" aria-label="Copy password" onclick={copy} disabled={!value}>📋</button>
</div>
