// Netdisk upload engine: streams an async iterable of {file, relPath} entries
// into multipart batches (plus the chunked/resumable protocol for files >= 1
// GiB), driving the bounded UploadOverlay card. Ported from the old netdisk.js
// module; the view passes an onDone callback to refresh its listing.

import { readCSRFCookie } from "@/api";
import { store } from "@/store";
import { tt } from "@/i18n";
import { showUploadOverlay } from "@/uploadOverlay";
import { makeFileIterator, prependIterator, makeDropIterator } from "@/lib/uploadStream.js";
import { hashFileCRC32 } from "@/lib/hashfile.js";
import { uploadLargeFile } from "@/lib/chunkupload.js";
import { uploadWithProgress } from "@/lib/upload.js";

// Guard against concurrent uploads: the overlay is shared, so a second pick
// while one is running would race it.
let uploading = false;

// A single multipart request carries at most UPLOAD_BATCH_FILES files /
// UPLOAD_BATCH_BYTES bytes. The byte cap keeps a batch of large files well
// under the 2 GiB ParseMultipartForm cap (and any reverse proxy body limit);
// it can make a batch smaller than the file cap, never larger.
const UPLOAD_BATCH_BYTES = 256 << 20; // 256 MiB
const UPLOAD_BATCH_FILES = 20;
// How many batches run concurrently: the pool pulls the next batch from the
// stream as soon as a slot frees.
const UPLOAD_CONCURRENCY = 3;
// Files at or above this size use the chunked/resumable protocol instead of
// one multipart request, so a multi-GB file resumes after a drop and is
// verified in 100 MB pieces rather than as a single giant blob.
const CHUNK_THRESHOLD = 1 << 30; // 1 GiB
// Whole-file re-attempts for a chunked upload before surfacing the manual
// Retry button. Cheap: the server keeps every verified chunk, so an attempt
// resumes from the first missing chunk instead of restarting at byte 0.
const LARGE_FILE_MAX_ATTEMPTS = 5;
const LARGE_FILE_RETRY_BASE_MS = 1000; // doubles each attempt: 1s, 2s, 4s, 8s

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// walkEntry recursively reads a FileSystemEntry, calling push(entry) for every
// file it discovers. push may return a Promise (a gate): when it does, walkEntry
// awaits it before reading the next entry. The upload queue supplies a gate that
// resolves only once the in-memory buffer has been drained below its cap, so a
// million-file folder is traversed at the speed of the wire rather than dumped
// into memory all at once. `prefix` is the directory path accumulated so far.
export function walkEntry(entry, prefix, push) {
  return new Promise((resolve) => {
    if (!entry) return resolve();
    const fullPath = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isFile) {
      entry.file(
        (file) => { Promise.resolve(push({ file, relPath: fullPath })).then(resolve); },
        () => resolve(), // unreadable file: skip rather than abort the batch
      );
      return;
    }
    if (entry.isDirectory) {
      const reader = entry.createReader();
      const readAll = () => {
        // readEntries returns a page of results; an empty page signals the end.
        reader.readEntries(
          async (page) => {
            if (!page || page.length === 0) return resolve();
            for (const child of page) await walkEntry(child, fullPath, push);
            readAll(); // keep paging until the directory is exhausted
          },
          () => resolve(), // unreadable dir: skip it, keep the rest
        );
      };
      readAll();
      return;
    }
    resolve();
  });
}

// The drop iterator is wired to walkEntry so traversal of a dropped folder
// back-pressures the upload and only ~one batch of File references is held in
// memory at a time.
export function dropStream(itemList) {
  return makeDropIterator(itemList, walkEntry, UPLOAD_BATCH_FILES * 2);
}

export function isUploading() {
  return uploading;
}

async function toast(msg, ok) {
  const { ElMessage } = await import("element-plus");
  if (ok) ElMessage.success(msg);
  else ElMessage.error(msg);
}

// uploadFilesHandler uploads entries to the netdisk (never the backup disk —
// uploads are netdisk-only). `source` is either an async iterable yielding
// entries one at a time (a dropped folder) or a plain array of picked files.
// Entries are pulled only fast enough to fill the next request, so at most one
// batch is held in memory regardless of folder size.
export async function uploadFilesHandler(source, path, onDone) {
  if (uploading) return toast(tt("netdisk.uploadInProgress"));
  uploading = true;
  const overlay = showUploadOverlay();

  const csrfToken = readCSRFCookie() || store.csrfToken || "";
  // totalBytes is accumulated as files are seen, so the progress bar is
  // meaningful even when N is unknown up front (a folder still being walked).
  let totalBytes = 0;
  let doneBytes = 0;
  let ok = 0;
  let failed = 0;
  let seen = 0;
  const batchStart = performance.now();
  let iter;
  try {
    iter = source[Symbol.asyncIterator]
      ? source[Symbol.asyncIterator]()
      : makeFileIterator(source);
  } catch {
    uploading = false;
    overlay.close();
    return;
  }

  const nextEntry = () => Promise.resolve(iter.next()).then((r) => (r.done ? null : r.value));

  // failedFiles collects entries the server rejected (write error or CRC32
  // mismatch) so they can be retried. Appended to while batches resolve, so it
  // must not be re-traversed concurrently — only after the run completes.
  const failedFiles = [];

  function uploadBatch(cur, curBytes) {
    const slots = cur.map((e) =>
      overlay.addActive({ name: e.relPath || e.file.name, size: e.file.size }),
    );
    return sendBatch(cur, slots, curBytes);
  }

  async function sendBatch(cur, slots, _curBytes) {
    // Large files (>= CHUNK_THRESHOLD) skip the multipart batch entirely and go
    // through the chunked/resumable protocol; small files proceed as one batch.
    const large = [];
    const small = [];
    const smallSlots = [];
    for (let i = 0; i < cur.length; i++) {
      if ((cur[i].file.size || 0) >= CHUNK_THRESHOLD) {
        large.push({ entry: cur[i], slot: slots[i] });
      } else {
        small.push(cur[i]);
        smallSlots.push(slots[i]);
      }
    }
    const largePromises = large.map(({ entry, slot }) => uploadOneLarge(entry, slot));
    let smallResp = null;
    if (small.length) smallResp = await sendSmallBatch(small, smallSlots);
    await Promise.allSettled(largePromises);
    return smallResp;
  }

  async function uploadOneLarge(entry, slot) {
    let lastLoaded = 0;
    let lastErr;
    for (let attempt = 0; attempt < LARGE_FILE_MAX_ATTEMPTS; attempt++) {
      try {
        await uploadLargeFile(entry.file, entry.relPath, {
          base: "/api/netdisk", dir: path, csrfToken,
          onProgress: ({ loaded, total, speedBps }) => {
            const delta = Math.max(0, loaded - lastLoaded);
            lastLoaded = loaded;
            doneBytes += delta;
            overlay.updateActive(slot, {
              loaded, total,
              percent: total > 0 ? Math.min(100, Math.round((loaded / total) * 100)) : 0,
              speedBps,
            });
          },
        });
        overlay.settleActive(slot, "done");
        ok++;
        return;
      } catch (err) {
        lastErr = err;
        if (attempt < LARGE_FILE_MAX_ATTEMPTS - 1) {
          await sleep(LARGE_FILE_RETRY_BASE_MS * 2 ** attempt);
        }
      }
    }
    failedFiles.push(entry);
    failed++;
    armRetry(slot, entry, lastErr?.message);
  }

  async function sendSmallBatch(cur, slots) {
    // Compute each file's CRC32 up front (off-main-thread) so the server can
    // verify integrity. Files that can't be hashed yield "" and are still
    // uploaded — the server checksums them.
    const hashes = await Promise.all(cur.map((e) => hashFileCRC32(e.file)));

    const fd = new FormData();
    fd.append("path", path);
    for (let i = 0; i < cur.length; i++) {
      // The relative path travels in its own "paths" field, one value per file
      // part in the same order: a part's filename cannot carry directories
      // (RFC 7578 §4.2), and the backend's multipart parser strips them.
      fd.append("paths", cur[i].relPath);
      fd.append("hashes", hashes[i] || "");
      fd.append("files", cur[i].file, cur[i].relPath);
    }
    const loaded = new Array(cur.length).fill(0);
    const curBytes = cur.reduce((n, e) => n + (e.file.size || 0), 0);
    let resp;
    try {
      resp = await uploadWithProgress("/api/netdisk/upload", fd, {
        csrfToken,
        onProgress: (p) => {
          // uploadWithProgress reports bytes for the whole batch request;
          // attribute them proportionally so each row still advances.
          const batchTotal = p.total || curBytes;
          const ratio = batchTotal > 0 ? p.loaded / batchTotal : 0;
          for (let i = 0; i < cur.length; i++) {
            const fileTotal = cur[i].file.size || 0;
            const fileLoaded = fileTotal > 0 ? Math.min(fileTotal, Math.round(fileTotal * ratio)) : 0;
            doneBytes += Math.max(0, fileLoaded - loaded[i]);
            loaded[i] = fileLoaded;
            overlay.updateActive(slots[i], {
              loaded: fileLoaded,
              total: fileTotal,
              percent: fileTotal > 0 ? Math.min(100, Math.round((fileLoaded / fileTotal) * 100)) : 100,
              speedBps: p.speedBps,
            });
          }
        },
      });
    } catch (err) {
      // Network/HTTP failure for the whole request: every file is retriable.
      for (let i = 0; i < cur.length; i++) {
        failedFiles.push(cur[i]);
        failed++;
        armRetry(slots[i], cur[i], err.message);
      }
      return;
    }
    // A file is delivered only when it has no error AND (the client hash was
    // empty OR it matches the server crc32).
    const results = Array.isArray(resp?.results) ? resp.results : [];
    for (let i = 0; i < cur.length; i++) {
      const r = results[i] || {};
      const serverCrc32 = (r.crc32 || "").toLowerCase();
      const clientCrc32 = (hashes[i] || "").toLowerCase();
      const okFile = !r.error && (!clientCrc32 || clientCrc32 === serverCrc32);
      if (okFile) {
        overlay.settleActive(slots[i], "done");
        ok++;
      } else {
        failedFiles.push(cur[i]);
        failed++;
        const why = r.error || (clientCrc32 ? tt("netdisk.crc32Mismatch") : "Failed");
        armRetry(slots[i], cur[i], why);
      }
    }
    return resp;
  }

  // armRetry marks a failed row with a Retry button that re-queues just that
  // file, reusing the same overlay row and re-running it through sendBatch so
  // CRC32 verification + result checking apply identically.
  function armRetry(slot, entry, msg) {
    overlay.markFailedWithRetry(slot, msg, async () => {
      overlay.reactivate(slot, { name: entry.relPath || entry.file.name, size: entry.file.size });
      const idx = failedFiles.indexOf(entry);
      if (idx >= 0) failedFiles.splice(idx, 1);
      failed = Math.max(0, failed - 1);
      refreshOverall();
      await sendBatch([entry], [slot], entry.file.size || 0);
      if (onDone) onDone();
    });
  }

  function refreshOverall() {
    const elapsed = (performance.now() - batchStart) / 1000;
    const speedBps = elapsed > 0 ? doneBytes / elapsed : 0;
    const percent = totalBytes > 0 ? Math.min(100, (doneBytes / totalBytes) * 100) : 0;
    const etaSec = speedBps > 0 ? (totalBytes - doneBytes) / speedBps : 0;
    overlay.updateOverall({
      done: ok, failed, total: seen, loaded: doneBytes, bytesTotal: totalBytes, speedBps, etaSec, percent,
    });
  }

  // Concurrent pool: keep up to UPLOAD_CONCURRENCY batches in flight, pulling
  // the next batch from the stream whenever a slot frees.
  const inflight = new Set();
  for (;;) {
    while (inflight.size < UPLOAD_CONCURRENCY) {
      const cur = [];
      let curBytes = 0;
      while (cur.length < UPLOAD_BATCH_FILES) {
        const entry = await nextEntry();
        if (!entry) break;
        const size = entry.file.size || 0;
        // A single oversized file forms its own batch rather than being split.
        if (cur.length > 0 && curBytes + size > UPLOAD_BATCH_BYTES) {
          iter = prependIterator(iter, entry); // push back for the next batch
          break;
        }
        cur.push(entry);
        curBytes += size;
        totalBytes += size;
        seen++;
      }
      if (!cur.length) break; // stream exhausted
      overlay.setLabel(tt("netdisk.uploadingRange", { lo: ok + failed + 1, hi: ok + failed + cur.length, total: seen }));
      const p = uploadBatch(cur, curBytes).then(() => {
        inflight.delete(p);
        refreshOverall();
      });
      inflight.add(p);
    }
    if (!inflight.size) break;
    await Promise.race(inflight);
    refreshOverall();
  }
  if (inflight.size) { await Promise.allSettled(inflight); refreshOverall(); }

  overlay.setLabel(tt("netdisk.uploadedN", { ok, total: seen }));
  const summary = failed
    ? tt("netdisk.uploadedNFailed", { ok, fail: failed })
    : tt("netdisk.uploadedN", { ok, total: seen });
  toast(summary, !failed);
  if (onDone) onDone();
  refreshOverall();
  // The card stays open until the user closes it: an auto-dismiss can hide a
  // failure before the user gets to click Retry.
  uploading = false;
}
