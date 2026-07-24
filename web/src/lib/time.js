// Shared timestamp formatting. The panel stores UTC timestamps as
// "YYYY-MM-DD HH:MM:SS" with no zone; normalize to a real Date first, then
// format in the viewer's locale/timezone.

function toDate(iso) {
  if (!iso) return null;
  const s = iso.includes("Z") || iso.includes("+") ? iso : iso.replace(" ", "T") + "Z";
  const d = new Date(s);
  return isNaN(d.getTime()) ? null : d;
}

// formatTime renders an absolute local date-time (falls back to the raw string).
export function formatTime(iso) {
  const d = toDate(iso);
  return d ? d.toLocaleString() : iso || "";
}

const rtf =
  typeof Intl !== "undefined" && Intl.RelativeTimeFormat
    ? new Intl.RelativeTimeFormat(undefined, { numeric: "auto" })
    : null;

// relativeTime renders a friendly "3 hours ago" style string.
export function relativeTime(iso) {
  const d = toDate(iso);
  if (!d) return iso || "";
  const sec = Math.round((Date.now() - d.getTime()) / 1000);
  const abs = Math.abs(sec);
  const units = [
    ["year", 31536000],
    ["month", 2592000],
    ["week", 604800],
    ["day", 86400],
    ["hour", 3600],
    ["minute", 60],
  ];
  for (const [name, s] of units) {
    if (abs >= s) {
      const n = Math.round(-sec / s); // negative = past
      return rtf ? rtf.format(n, name) : `${Math.abs(n)} ${name}${Math.abs(n) === 1 ? "" : "s"} ${n <= 0 ? "ago" : "from now"}`;
    }
  }
  return sec >= 0 ? "just now" : "in a moment";
}
