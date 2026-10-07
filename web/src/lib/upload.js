// Uploading a file to a server's data directory, in pieces when it has to be.
//
// Not for progress — because a single request carrying a big file does not
// arrive. A panel reached over a Cloudflare tunnel has its request body capped
// at 100 MB, and an nginx in front of one has a limit of its own, so a 400 MB
// modpack or world is refused with a 413 that names neither the proxy nor the
// size. For a user whose only access to the server is the Files tab, that is not
// an inconvenience: it is no way in at all.
//
// Lives here rather than in the component so the fallback below can be tested
// without a browser — it is the part that only runs on somebody else's network.

// Descending, because the cap in the path is not knowable from here. 16 MB
// clears every default worth the name; if something still answers 413 the upload
// starts over smaller rather than giving up, which is safe because chunk 0
// truncates server-side.
export const CHUNK_SIZES = [16, 4, 1].map((mb) => mb * 1024 * 1024);

export function newUploadID() {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}

// uploadFile sends one file to dest, whole when it is small and in pieces when
// it is not. onProgress is called with 0..1 so a 400 MB upload is not a frozen
// tab. post is the API client's POST, injected so this is testable.
export async function uploadFile(post, serverId, file, dest, onProgress) {
  const url = `/servers/${serverId}/files/upload`;
  if (file.size <= CHUNK_SIZES[0]) {
    const fd = new FormData();
    fd.append("path", dest);
    fd.append("file", file);
    await post(url, fd);
    onProgress?.(1);
    return;
  }
  let lastErr;
  for (const size of CHUNK_SIZES) {
    const id = newUploadID();
    const count = Math.ceil(file.size / size);
    try {
      for (let i = 0; i < count; i++) {
        const fd = new FormData();
        fd.append("path", dest);
        fd.append("name", file.name);
        fd.append("upload_id", id);
        fd.append("chunk_index", String(i));
        fd.append("chunk_count", String(count));
        fd.append("file", file.slice(i * size, (i + 1) * size), file.name);
        await post(url, fd);
        onProgress?.(Math.min((i + 1) * size, file.size) / file.size);
      }
      return;
    } catch (e) {
      lastErr = e;
      // 413 is something in the path refusing this body size, which smaller
      // pieces can fix. Any other failure would only repeat at another size, so
      // it is raised now rather than after two more attempts at it.
      if (e?.status !== 413) throw e;
    }
  }
  throw lastErr;
}
