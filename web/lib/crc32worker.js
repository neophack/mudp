// Web Worker that computes the CRC32 (IEEE / zlib polynomial) of a File/Blob in
// chunks, so the main thread never blocks even when hashing millions of files.
// CRC32 (not MD5) because this hash is only a corruption check, not a security
// digest, and the reflected-polynomial table lookup below does a fraction of
// the per-byte work MD5's block rounds require.
//
// Protocol:
//   main -> worker: { id, file }       // File is structured-cloned (not transferred)
//   worker -> main: { id, type: "progress", loaded, total }
//   worker -> main: { id, type: "done", crc32 }          // lowercase hex, or "" on error
//
// Must match the server's hash/crc32.NewIEEE() (Go stdlib), which is the same
// standard CRC-32 used by zlib/gzip/PNG/zip: reflected polynomial 0xEDB88320,
// init 0xFFFFFFFF, final XOR 0xFFFFFFFF.

self.onmessage = async (e) => {
  const { id, file, start, end } = e.data || {};
  if (!file) {
    self.postMessage({ id, type: "done", crc32: "" });
    return;
  }
  try {
    // An optional [start,end) byte range hashes just that slice — used for
    // per-chunk CRC32 in large-file uploads. Without it the whole file is hashed.
    const crc32 = await computeCRC32(file, (loaded, total) => {
      self.postMessage({ id, type: "progress", loaded, total });
    }, start, end);
    self.postMessage({ id, type: "done", crc32 });
  } catch {
    // Any failure (unreadable file, OOM) resolves to an empty digest so the
    // caller can degrade gracefully: the server still writes + checksums.
    self.postMessage({ id, type: "done", crc32: "" });
  }
};

const CHUNK = 4 * 1024 * 1024; // 4 MiB

async function computeCRC32(file, onProgress, start, end) {
  const s = new CRC32Stream();
  const lo = Math.max(0, Number.isFinite(start) ? start : 0);
  const hi = Math.min(file.size, Number.isFinite(end) ? end : file.size);
  if (lo >= hi) return s.hex(); // empty range -> CRC32 of empty input
  let off = lo;
  while (off < hi) {
    const sliceEnd = Math.min(off + CHUNK, hi);
    const buf = await sliceToUint8(file, off, sliceEnd);
    s.update(buf);
    off = sliceEnd;
    onProgress(off - lo, hi - lo);
    // Yield so the worker stays responsive to cancellation between chunks.
    await microtask();
  }
  return s.hex();
}

function sliceToUint8(file, start, end) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(new Uint8Array(reader.result));
    reader.onerror = () => reject(reader.error || new Error("read failed"));
    reader.readAsArrayBuffer(file.slice(start, end));
  });
}

function microtask() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// ---- Incremental CRC32 (IEEE / zlib polynomial) -----------------------------
// A 256-entry lookup table trades a small amount of setup for O(1)-per-byte
// hashing, so a multi-GB file never needs more than the running 4-byte state
// plus the current chunk in memory.
const CRC_TABLE = buildCrcTable();

function buildCrcTable() {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) {
      c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    }
    table[n] = c >>> 0;
  }
  return table;
}

function CRC32Stream() {
  this.crc = 0xffffffff; // running register, pre-final-XOR
}
CRC32Stream.prototype.update = function (data) {
  let crc = this.crc;
  for (let i = 0; i < data.length; i++) {
    crc = (CRC_TABLE[(crc ^ data[i]) & 0xff] ^ (crc >>> 8)) >>> 0;
  }
  this.crc = crc;
};
CRC32Stream.prototype.hex = function () {
  const out = (this.crc ^ 0xffffffff) >>> 0;
  return out.toString(16).padStart(8, "0");
};
