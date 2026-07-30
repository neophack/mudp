// hashFileMD5 computes the MD5 of a File/Blob off the main thread using the
// md5worker.js Web Worker, so hashing millions of files never freezes the UI.
//
// Returns a Promise resolving to the lowercase hex digest, or "" if hashing is
// unavailable (Worker construction failed, or the browser blocks workers). The
// upload pipeline treats "" as "no client hash": the file is still uploaded and
// the server checksums/verifies it server-side, so correctness is preserved.
//
// Concurrency is capped: at most MAX_WORKERS hashing jobs run at once. Beyond
// that, requests queue (FIFO). This keeps memory flat — each Worker holds one
// 4 MiB slice at a time, but unbounded parallelism would still allocate one
// Worker + transferable per file.
const MAX_WORKERS = 4;
const workerUrl = new URL("./md5worker.js", import.meta.url).href;

let worker = null;
let nextId = 1;
const pending = new Map(); // id -> { resolve, onProgress }
const queue = []; // backlog of { file, onProgress, resolve, reject }
let active = 0; // jobs currently handed to the worker, awaiting reply

function ensureWorker() {
  if (worker) return worker;
  try {
    // A module worker would let us import, but classic workers are more broadly
    // supported and md5worker.js is self-contained.
    worker = new Worker(workerUrl);
    worker.onmessage = (e) => {
      const { id, type } = e.data || {};
      const slot = pending.get(id);
      if (!slot) return;
      if (type === "progress") {
        slot.onProgress?.(e.data.loaded, e.data.total);
      } else if (type === "done") {
        pending.delete(id);
        slot.resolve(e.data.md5 || "");
        active--;
        drain();
      }
    };
    worker.onerror = () => {
      // Worker died: fail every pending job and reset so the next call retries.
      for (const slot of pending.values()) slot.resolve("");
      pending.clear();
      active = 0;
      worker = null;
      drain();
    };
  } catch {
    worker = null; // construction threw (e.g. CSP / file://) — degrade.
  }
  return worker;
}

// drain launches queued jobs while a worker slot is free.
function drain() {
  const w = ensureWorker();
  if (!w) {
    // No worker available: resolve every queued job to "" (server-side fallback).
    while (queue.length) queue.shift().resolve("");
    return;
  }
  while (active < MAX_WORKERS && queue.length) {
    const job = queue.shift();
    active++;
    const id = nextId++;
    pending.set(id, { resolve: job.resolve, onProgress: job.onProgress });
    // Post WITHOUT a transfer list: a File is cheap to structured-clone (its
    // bytes are only materialized when the worker reads them), and transferring
    // would neuter the original File the caller still needs for FormData upload.
    // An optional {start,end} byte range hashes just that slice (per-chunk MD5
    // for large-file uploads).
    w.postMessage({ id, file: job.file, start: job.start, end: job.end });
  }
}

// hashFileMD5(file, onProgress) -> Promise<string>
// `file` is NOT transferred (see drain): the original stays valid for the
// FormData upload that follows. Hashes the whole file off the main thread.
export function hashFileMD5(file, onProgress) {
  // Fast skip for empty files: MD5("") is the well-known constant, and hashing
  // a 0-byte file through the worker is pointless overhead.
  if (!file || file.size === 0) return Promise.resolve(file ? EMPTY_MD5 : "");
  return new Promise((resolve, reject) => {
    queue.push({ file, onProgress, resolve, reject });
    drain();
  });
}

// hashChunkMD5(file, start, end, onProgress) -> Promise<string>
// Hashes the byte range [start,end) of `file`, for per-chunk verification of a
// large-file upload. Shares the same capped worker pool as hashFileMD5.
export function hashChunkMD5(file, start, end, onProgress) {
  if (!file) return Promise.resolve("");
  const lo = Math.max(0, start || 0);
  const hi = Math.min(file.size, end != null ? end : file.size);
  if (lo >= hi) return Promise.resolve(EMPTY_MD5);
  return new Promise((resolve, reject) => {
    queue.push({ file, start: lo, end: hi, onProgress, resolve, reject });
    drain();
  });
}

const EMPTY_MD5 = "d41d8cd98f00b204e9800998ecf8427e";
