import { describe, it, expect } from "vitest";

// Mirrors the message-selection in api.js. Kept as a pure helper here so the
// rule can be tested without standing up fetch: an API error must NEVER end up
// with an empty .message, or callers show a generic "something went wrong" and
// the real reason (private repo, rate limit, bad ref) is lost.
function errorMessage(data, statusText, status) {
  const plain =
    typeof data === "string" && data.trim() && !/^\s*</.test(data)
      ? data.trim().slice(0, 300)
      : "";
  return (data && data.error) || plain || statusText || `HTTP ${status}`;
}

describe("api error message selection", () => {
  it("prefers the server's JSON error field", () => {
    expect(errorMessage({ error: "not found: a/b@main" }, "Bad Request", 400))
      .toBe("not found: a/b@main");
  });

  it("falls back to a plain-text body", () => {
    expect(errorMessage("upstream exploded", "", 502)).toBe("upstream exploded");
  });

  it("does not dump an HTML error page into the message", () => {
    // A proxy replacing our JSON with its own error page: don't show markup.
    const msg = errorMessage("<!DOCTYPE html><html>Bad gateway</html>", "", 502);
    expect(msg).toBe("HTTP 502");
    expect(msg).not.toContain("<");
  });

  it("never returns empty — HTTP/2 has no statusText", () => {
    // The exact combination that produced Error("") in the wild.
    expect(errorMessage(null, "", 502)).toBe("HTTP 502");
    expect(errorMessage(undefined, undefined, 500)).toBe("HTTP 500");
    expect(errorMessage("", "", 400)).toBe("HTTP 400");
  });

  it("uses statusText when there is one and no body", () => {
    expect(errorMessage(null, "Forbidden", 403)).toBe("Forbidden");
  });

  it("truncates a very long plain body", () => {
    expect(errorMessage("x".repeat(1000), "", 500)).toHaveLength(300);
  });
});
