import { describe, it, expect } from "vitest";
import { uploadFile, CHUNK_SIZES } from "./upload.js";

// A fake server that behaves like the thing in the way: it refuses any request
// body over `cap` with a 413, exactly as Cloudflare and nginx do, and otherwise
// records what it received. The whole point of the chunking is that it works on
// a network we cannot see, so the test has to be the network.
function fakeEndpoint(cap) {
  const chunks = [];
  const post = async (_url, fd) => {
    const blob = fd.get("file");
    if (blob.size > cap) {
      const e = new Error("request entity too large");
      e.status = 413;
      throw e;
    }
    chunks.push({
      size: blob.size,
      index: Number(fd.get("chunk_index") ?? -1),
      count: Number(fd.get("chunk_count") ?? 0),
      id: fd.get("upload_id") ?? "",
      name: fd.get("name") ?? "",
      path: fd.get("path"),
    });
  };
  return { post, chunks };
}

function fakeFile(size, name = "pack.zip") {
  const blob = new Blob([new Uint8Array(size)]);
  // A real File carries a name; Blob does not, and the code reads file.name.
  return Object.assign(blob, { name });
}

describe("uploadFile", () => {
  it("sends a small file in one request, with no chunk fields", async () => {
    const { post, chunks } = fakeEndpoint(100 * 1024 * 1024);
    await uploadFile(post, "srv1", fakeFile(1024), "sub");
    expect(chunks).toHaveLength(1);
    expect(chunks[0].index).toBe(-1); // absent — the old whole-file path
    expect(chunks[0].path).toBe("sub");
  });

  it("splits a large file and sends every byte, in order, under one upload id", async () => {
    const { post, chunks } = fakeEndpoint(100 * 1024 * 1024);
    const size = CHUNK_SIZES[0] * 2 + 1234;
    await uploadFile(post, "srv1", fakeFile(size), "");
    expect(chunks).toHaveLength(3);
    expect(chunks.map((c) => c.index)).toEqual([0, 1, 2]);
    expect(new Set(chunks.map((c) => c.id)).size).toBe(1);
    expect(chunks.every((c) => c.count === 3)).toBe(true);
    // The number next to the green: the bytes have to add up to the file, or a
    // perfectly ordered upload still delivers a truncated archive.
    expect(chunks.reduce((n, c) => n + c.size, 0)).toBe(size);
    expect(chunks[0].name).toBe("pack.zip");
  });

  // The reason the sizes descend. A proxy with a 5 MB limit rejects our 16 MB
  // piece, and the upload has to get smaller rather than fail — this is the case
  // that cannot be tested on our own network, where nothing refuses anything.
  it("starts over with smaller pieces when the network answers 413", async () => {
    const { post, chunks } = fakeEndpoint(5 * 1024 * 1024);
    const size = CHUNK_SIZES[0] * 2;
    await uploadFile(post, "srv1", fakeFile(size), "");

    expect(chunks.every((c) => c.size <= 5 * 1024 * 1024)).toBe(true);
    // Restarted, so a fresh upload id — the server truncates on index 0 and the
    // abandoned first attempt must not be continued into.
    const ids = new Set(chunks.map((c) => c.id));
    expect(ids.size).toBe(1);
    expect(chunks[0].index).toBe(0);
    expect(chunks.reduce((n, c) => n + c.size, 0)).toBe(size);
  });

  it("gives up and reports the error when even the smallest piece is refused", async () => {
    const { post } = fakeEndpoint(1024);
    await expect(uploadFile(post, "srv1", fakeFile(CHUNK_SIZES[0] * 2), "")).rejects.toThrow(
      /too large/,
    );
  });

  // Retrying a disk-full or permission error at three sizes would just be the
  // same failure three times, and the message the admin needs would arrive last.
  it("does not retry a failure that is not about size", async () => {
    let calls = 0;
    const post = async () => {
      calls++;
      const e = new Error("write: no space left on device");
      e.status = 500;
      throw e;
    };
    await expect(uploadFile(post, "srv1", fakeFile(CHUNK_SIZES[0] * 2), "")).rejects.toThrow(
      /no space/,
    );
    expect(calls).toBe(1);
  });

  it("reports progress up to 1", async () => {
    const { post } = fakeEndpoint(100 * 1024 * 1024);
    const seen = [];
    await uploadFile(post, "srv1", fakeFile(CHUNK_SIZES[0] * 2 + 5), "", (p) => seen.push(p));
    expect(seen.at(-1)).toBe(1);
    expect(seen).toEqual([...seen].sort((a, b) => a - b));
  });
});
