// Web Worker that computes the MD5 of a File/Blob in chunks, so the main thread
// never blocks even when hashing millions of files.
//
// Protocol:
//   main -> worker: { id, file }       // File is structured-cloned (not transferred)
//   worker -> main: { id, type: "progress", loaded, total }
//   worker -> main: { id, type: "done", md5 }            // lowercase hex, or "" on error
//
// The MD5 implementation below is a self-contained, dependency-free incremental
// version (RFC 1321) so the project needs no vendored hashing library. It feeds
// the file in 4 MiB slices via FileReader.readAsArrayBuffer and updates the
// running digest, which keeps peak memory to a single chunk.

self.onmessage = async (e) => {
  const { id, file, start, end } = e.data || {};
  if (!file) {
    self.postMessage({ id, type: "done", md5: "" });
    return;
  }
  try {
    // An optional [start,end) byte range hashes just that slice — used for
    // per-chunk MD5 in large-file uploads. Without it the whole file is hashed.
    const md5 = await computeMD5(file, (loaded, total) => {
      self.postMessage({ id, type: "progress", loaded, total });
    }, start, end);
    self.postMessage({ id, type: "done", md5 });
  } catch {
    // Any failure (unreadable file, OOM) resolves to an empty digest so the
    // caller can degrade gracefully: the server still writes + checksums.
    self.postMessage({ id, type: "done", md5: "" });
  }
};

const CHUNK = 4 * 1024 * 1024; // 4 MiB

async function computeMD5(file, onProgress, start, end) {
  const s = new MD5Stream();
  const lo = Math.max(0, Number.isFinite(start) ? start : 0);
  const hi = Math.min(file.size, Number.isFinite(end) ? end : file.size);
  if (lo >= hi) return s.hex(); // empty range -> MD5 of empty input
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

// ---- Incremental MD5 (RFC 1321), public-domain-style reference impl --------
// Operates on byte chunks so a multi-GB file never needs to be in memory at once.
function MD5Stream() {
  this.state = md5Init(); // chaining vars a,b,c,d (4 words)
  this.buf = new Uint8Array(64); // current partial block (< 64 bytes valid)
  this.buflen = 0; // valid bytes in buf
  this.len = 0; // total message bytes processed (excludes padding)
}
MD5Stream.prototype.update = function (data) {
  let i = 0;
  // Fill + flush the partial block first so we always start from a clean 64.
  if (this.buflen) {
    const need = 64 - this.buflen;
    const take = Math.min(need, data.length);
    this.buf.set(data.subarray(0, take), this.buflen);
    this.buflen += take;
    i = take;
    if (this.buflen === 64) {
      md5Block(this.state, this.buf);
      this.buflen = 0;
    }
  }
  while (data.length - i >= 64) {
    md5Block(this.state, data.subarray(i, i + 64));
    i += 64;
  }
  if (i < data.length) {
    this.buf.set(data.subarray(i), 0);
    this.buflen = data.length - i;
  }
  this.len += data.length;
};
MD5Stream.prototype.hex = function () {
  // Build the final padded message tail by hand rather than via update(): the
  // length field has to land in the last 8 bytes of the last block, and a plain
  // append would split it across the existing partial buffer incorrectly.
  // Layout: [current buflen bytes][0x80][zeros...][64-bit LE bit length].
  // One 64-byte block holds the data + marker + length when buflen <= 55; at
  // 56+ bytes there is no room for the length, so it spills into a second block.
  const bitLen = BigInt(this.len) * 8n;
  const tail = new Uint8Array(this.buflen <= 55 ? 64 : 128);
  // Copy the buffered data bytes into the tail.
  for (let i = 0; i < this.buflen; i++) tail[i] = this.buf[i];
  tail[this.buflen] = 0x80;
  // 64-bit little-endian bit length in the final 8 bytes.
  for (let i = 0; i < 8; i++) {
    tail[tail.length - 8 + i] = Number((bitLen >> BigInt(i * 8)) & 0xffn);
  }
  // Process the finalized blocks directly (don't touch this.buf/buflen).
  for (let i = 0; i < tail.length; i += 64) {
    md5Block(this.state, tail.subarray(i, i + 64));
  }
  return toHex(this.state);
};

function md5Init() {
  // a,b,c,d from RFC 1321; updated in place by md5Block.
  return [0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476];
}

// md5Block processes one 64-byte block, mutating `state` (length-4 array a,b,c,d).
function md5Block(state, block) {
  const x = new Array(16);
  const dv = new DataView(block.buffer || block, block.byteOffset || 0, 64);
  for (let i = 0; i < 16; i++) x[i] = dv.getUint32(i * 4, true);

  let a = state[0], b = state[1], c = state[2], d = state[3];

  // Round 1
  let r = (x0, s, ac) => { a = add(a, F(b, c, d), x0, ac); a = rotl(a, s); a = add(a, b); [a, b, c, d] = [d, a, b, c]; };
  r(x[0], 7, 0xd76aa478); r(x[1], 12, 0xe8c7b756); r(x[2], 17, 0x242070db); r(x[3], 22, 0xc1bdceee);
  r(x[4], 7, 0xf57c0faf); r(x[5], 12, 0x4787c62a); r(x[6], 17, 0xa8304613); r(x[7], 22, 0xfd469501);
  r(x[8], 7, 0x698098d8); r(x[9], 12, 0x8b44f7af); r(x[10], 17, 0xffff5bb1); r(x[11], 22, 0x895cd7be);
  r(x[12], 7, 0x6b901122); r(x[13], 12, 0xfd987193); r(x[14], 17, 0xa679438e); r(x[15], 22, 0x49b40821);
  // Round 2
  let r2 = (x0, s, ac) => { a = add(a, G(b, c, d), x0, ac); a = rotl(a, s); a = add(a, b); [a, b, c, d] = [d, a, b, c]; };
  r2(x[1], 5, 0xf61e2562); r2(x[6], 9, 0xc040b340); r2(x[11], 14, 0x265e5a51); r2(x[0], 20, 0xe9b6c7aa);
  r2(x[5], 5, 0xd62f105d); r2(x[10], 9, 0x02441453); r2(x[15], 14, 0xd8a1e681); r2(x[4], 20, 0xe7d3fbc8);
  r2(x[9], 5, 0x21e1cde6); r2(x[14], 9, 0xc33707d6); r2(x[3], 14, 0xf4d50d87); r2(x[8], 20, 0x455a14ed);
  r2(x[13], 5, 0xa9e3e905); r2(x[2], 9, 0xfcefa3f8); r2(x[7], 14, 0x676f02d9); r2(x[12], 20, 0x8d2a4c8a);
  // Round 3
  let r3 = (x0, s, ac) => { a = add(a, H(b, c, d), x0, ac); a = rotl(a, s); a = add(a, b); [a, b, c, d] = [d, a, b, c]; };
  r3(x[5], 4, 0xfffa3942); r3(x[8], 11, 0x8771f681); r3(x[11], 16, 0x6d9d6122); r3(x[14], 23, 0xfde5380c);
  r3(x[1], 4, 0xa4beea44); r3(x[4], 11, 0x4bdecfa9); r3(x[7], 16, 0xf6bb4b60); r3(x[10], 23, 0xbebfbc70);
  r3(x[13], 4, 0x289b7ec6); r3(x[0], 11, 0xeaa127fa); r3(x[3], 16, 0xd4ef3085); r3(x[6], 23, 0x04881d05);
  r3(x[9], 4, 0xd9d4d039); r3(x[12], 11, 0xe6db99e5); r3(x[15], 16, 0x1fa27cf8); r3(x[2], 23, 0xc4ac5665);
  // Round 4
  let r4 = (x0, s, ac) => { a = add(a, I(b, c, d), x0, ac); a = rotl(a, s); a = add(a, b); [a, b, c, d] = [d, a, b, c]; };
  r4(x[0], 6, 0xf4292244); r4(x[7], 10, 0x432aff97); r4(x[14], 15, 0xab9423a7); r4(x[5], 21, 0xfc93a039);
  r4(x[12], 6, 0x655b59c3); r4(x[3], 10, 0x8f0ccc92); r4(x[10], 15, 0xffeff47d); r4(x[1], 21, 0x85845dd1);
  r4(x[8], 6, 0x6fa87e4f); r4(x[15], 10, 0xfe2ce6e0); r4(x[6], 15, 0xa3014314); r4(x[13], 21, 0x4e0811a1);
  r4(x[4], 6, 0xf7537e82); r4(x[11], 10, 0xbd3af235); r4(x[2], 15, 0x2ad7d2bb); r4(x[9], 21, 0xeb86d391);

  state[0] = add(state[0], a);
  state[1] = add(state[1], b);
  state[2] = add(state[2], c);
  state[3] = add(state[3], d);
}

const F = (b, c, d) => (b & c) | (~b & d);
const G = (b, c, d) => (b & d) | (c & ~d);
const H = (b, c, d) => b ^ c ^ d;
const I = (b, c, d) => c ^ (b | ~d);

function rotl(x, n) { return ((x << n) | (x >>> (32 - n))) >>> 0; }
function add(...xs) {
  let s = 0;
  for (const v of xs) s = (s + v) & 0xffffffff;
  return s >>> 0;
}
function toHex(words) {
  let out = "";
  for (const w of words) {
    const b = new Uint8Array(4);
    new DataView(b.buffer).setUint32(0, w, true);
    for (const by of b) out += by.toString(16).padStart(2, "0");
  }
  return out;
}
