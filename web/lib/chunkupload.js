// Large-file chunked + resumable upload coordinator.
//
// Files at or above the caller's threshold (1 GB by default) are uploaded as a
// stream of CRC32-verified 100 MB chunks instead of one giant multipart request, so:
//   - a multi-GB file never builds one FormData blob in memory,
//   - a dropped connection / browser refresh resumes from the last verified
//     chunk instead of restarting from byte 0,
//   - a corrupt chunk is detected and re-sent, never silently accepted.
//
// Protocol (server side in internal/server/chunkupload.go):
//   POST .../chunk/init?path=&name=      { path,name,size,chunkSize,totalChunks,fileCRC32 }
//       -> { uploadId, resume, received:[..], chunkSize, totalChunks }
//   POST .../chunk?path=&name=           multipart: uploadId, name, index, hash, chunk
//       -> { ok, index, crc32 }
//   POST .../chunk/complete?path=&name=  { uploadId, name, fileCRC32 }
//       -> { ok, crc32, path }  | 409 { missing:[..] }
//   POST .../chunk/abort?path=&name=     { uploadId, name }   (cleanup)
//
// The `path=` / `name=` query parameters carry the in-folder destination
// (`path`) and, for volumes, the volume identifier (`name` — distinct from the
// file name that travels in the body). They ride on every request as a query
// string rather than the body so the server can resolve the destination
// directory the same way regardless of whether the body is JSON or multipart.
//
// uploadLargeFile returns a Promise<{ok, crc32, path}> on success and rejects
// (with the last error) if it cannot make progress. It reports live per-file
// progress via onProgress({loaded, total, speedBps}), updating continuously as
// bytes stream out within each chunk — not just once a chunk fully lands.

import { hashChunkCRC32 } from "./hashfile.js";
import { uploadWithProgress } from "./upload.js";

export const DEFAULT_CHUNK_SIZE = 100 * 1024 * 1024; // 100 MiB
export const DEFAULT_CHUNK_CONCURRENCY = 3; // chunks in flight at once
const CHUNK_MAX_RETRIES = 3;

// computeTotalChunks divides `size` into `chunkSize`-sized pieces (last short).
export function computeTotalChunks(size, chunkSize) {
  if (size <= 0 || chunkSize <= 0) return 0;
  return Math.ceil(size / chunkSize);
}

// postJSON is a tiny fetch wrapper that sends CSRF + JSON and parses JSON back.
async function postJSON(url, body, csrfToken) {
  const resp = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken || "" },
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
  let data = {};
  try { data = await resp.json(); } catch { /* non-JSON error page */ }
  if (!resp.ok) {
    const err = new Error(data.error || `HTTP ${resp.status}`);
    err.status = resp.status;
    err.data = data;
    throw err;
  }
  return data;
}

// buildQuery renders the {path, name} pair the server needs to resolve the
// destination directory (netdisk root + path, or a volume's mountpoint + path)
// as a URL query string, e.g. "?path=docs%2F2024&name=myvol". Either key is
// omitted when empty (netdisk uploads have no volume identifier).
function buildQuery(dir, volume) {
  const q = new URLSearchParams();
  if (dir) q.set("path", dir);
  if (volume) q.set("name", volume);
  const s = q.toString();
  return s ? `?${s}` : "";
}

// uploadLargeFile uploads `file` (>= threshold) as verified chunks to the chunk
// endpoints rooted at `base` (e.g. "/api/netdisk"). `relPath` is the in-folder
// destination name; `dir` is the in-folder path (netdisk path / volume path);
// `volume` is the volume identifier (omit for netdisk, which has no separate
// identifier — the caller's own root is used).
export async function uploadLargeFile(file, relPath, opts) {
  const {
    base, dir = "", volume = "", csrfToken = "", chunkSize = DEFAULT_CHUNK_SIZE,
    concurrency = DEFAULT_CHUNK_CONCURRENCY, onProgress,
  } = opts || {};
  const size = file.size;
  const totalChunks = computeTotalChunks(size, chunkSize);
  if (!totalChunks) throw new Error("empty file");
  const qs = buildQuery(dir, volume);

  // 1) init — learn uploadId and which chunks the server already has (resume).
  const init = await postJSON(`${base}/chunk/init${qs}`, {
    path: dir, name: relPath, size, chunkSize, totalChunks, fileCRC32: "",
  }, csrfToken);
  const uploadId = init.uploadId;
  const received = new Set(Array.isArray(init.received) ? init.received : []);
  // committed = bytes belonging to chunks the server has fully accepted.
  // inFlight[index] = bytes of a chunk currently mid-upload (not yet committed),
  // reported live from that chunk's own XHR progress events. Together they let
  // onProgress advance smoothly within a single 100 MB chunk instead of jumping
  // once per chunk completion.
  let committed = 0;
  for (const i of received) committed += chunkBytes(i, totalChunks, chunkSize, size);
  const inFlight = new Map();

  let lastTime = performance.now();
  let lastLoaded = committed;
  let speedBps = 0;
  function reportProgress() {
    if (!onProgress) return;
    let loaded = committed;
    for (const v of inFlight.values()) loaded += v;
    const now = performance.now();
    const dt = (now - lastTime) / 1000;
    if (dt > 0) {
      // Clamp negative deltas: a chunk retry resets its in-flight byte count to
      // 0 (see uploadOneChunkWithRetry's onProgress?.(0)), which would otherwise
      // read here as a burst of negative "throughput" and drag the EMA down.
      const inst = Math.max(0, loaded - lastLoaded) / dt;
      speedBps = speedBps > 0 ? speedBps * 0.7 + inst * 0.3 : inst;
      lastTime = now;
      lastLoaded = loaded;
    }
    onProgress({ loaded, total: size, speedBps });
  }

  // 2) upload every missing chunk, up to `concurrency` at a time. Each chunk is
  //    CRC32-hashed first; the server verifies and refuses a mismatch.
  const missing = [];
  for (let i = 0; i < totalChunks; i++) if (!received.has(i)) missing.push(i);

  let idx = 0;
  async function worker() {
    while (idx < missing.length) {
      const myIdx = missing[idx++];
      const [start, end] = chunkRange(myIdx, totalChunks, chunkSize, size);
      const hash = await hashChunkCRC32(file, start, end);
      await uploadOneChunkWithRetry(`${base}/chunk${qs}`, {
        uploadId, name: relPath, index: myIdx, hash, blob: file.slice(start, end),
      }, csrfToken, (chunkLoaded) => {
        inFlight.set(myIdx, chunkLoaded);
        reportProgress();
      });
      inFlight.delete(myIdx);
      received.add(myIdx);
      committed += end - start;
      reportProgress();
    }
  }
  const pool = [];
  for (let i = 0; i < Math.min(concurrency, missing.length); i++) pool.push(worker());
  await Promise.all(pool);

  // 3) complete — assemble + whole-file CRC32 verify on the server. If it
  //    reports missing chunks (a late failure dropped one), re-send them and
  //    retry.
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const done = await postJSON(`${base}/chunk/complete${qs}`, {
        uploadId, name: relPath, fileCRC32: "",
      }, csrfToken);
      // Treat as delivered only when the server confirms with a crc32.
      return { ok: !!done.ok, crc32: done.crc32 || "", path: done.path || relPath };
    } catch (err) {
      const missing2 = err?.data?.missing;
      if (err.status === 409 && Array.isArray(missing2) && missing2.length) {
        // Re-upload the gaps the server says it's missing, then complete again.
        // These bytes were already folded into `committed` the first time this
        // chunk landed (either via the initial resume set or the worker loop
        // above), so re-adding them here would double-count the file's size —
        // inflating `loaded` past `size` and skewing the speed/ETA calculation.
        for (const i of missing2) {
          const [start, end] = chunkRange(i, totalChunks, chunkSize, size);
          const hash = await hashChunkCRC32(file, start, end);
          await uploadOneChunkWithRetry(`${base}/chunk${qs}`, {
            uploadId, name: relPath, index: i, hash, blob: file.slice(start, end),
          }, csrfToken, (chunkLoaded) => {
            inFlight.set(i, chunkLoaded);
            reportProgress();
          });
          inFlight.delete(i);
          reportProgress();
        }
        continue;
      }
      throw err;
    }
  }
  throw new Error("chunked upload could not complete");
}

// uploadOneChunkWithRetry POSTs one chunk, retrying transient failures a few
// times. A checksum mismatch surfaces as a 400 (the server deleted the bad
// segment) and is also retried — the next attempt recomputes the same hash, but
// re-slicing + re-POSTing a fresh body is the reliable fix for a transport garble.
// Uses XHR (via uploadWithProgress) rather than fetch so `onProgress` sees live
// byte counts as this chunk itself streams out, not just once it fully lands.
async function uploadOneChunkWithRetry(url, chunk, csrfToken, onProgress) {
  let lastErr;
  for (let attempt = 0; attempt < CHUNK_MAX_RETRIES; attempt++) {
    try {
      const fd = new FormData();
      fd.append("uploadId", chunk.uploadId);
      fd.append("name", chunk.name);
      fd.append("index", String(chunk.index));
      fd.append("hash", chunk.hash || "");
      fd.append("chunk", chunk.blob, chunk.name);
      return await uploadWithProgress(url, fd, {
        csrfToken,
        onProgress: onProgress ? (p) => onProgress(p.loaded) : undefined,
      });
    } catch (err) {
      lastErr = err;
      onProgress?.(0); // this attempt's bytes don't count; next attempt restarts from 0
      // Back off before retrying so a flaky/slow link isn't hammered with
      // back-to-back attempts; the delay grows with each attempt.
      if (attempt < CHUNK_MAX_RETRIES - 1) {
        await new Promise((resolve) => setTimeout(resolve, 500 * (attempt + 1)));
      }
    }
  }
  throw lastErr;
}

// chunkRange returns [start,end) for chunk index i given the layout.
function chunkRange(i, totalChunks, chunkSize, size) {
  const start = i * chunkSize;
  const end = Math.min(start + chunkSize, size);
  return [start, end];
}
function chunkBytes(i, totalChunks, chunkSize, size) {
  const [s, e] = chunkRange(i, totalChunks, chunkSize, size);
  return e - s;
}
