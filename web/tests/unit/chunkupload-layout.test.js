// @vitest-environment jsdom
// Layout-consistency tests for the chunked-upload client, backing
// docs/SECURITY-AUDIT.md M-1.
//
// The audit found the SERVER does not validate the size/chunkSize/totalChunks
// triple (quota-bypass vector). These tests pin the CLIENT's half of the
// contract: the honest frontend always sends an arithmetically consistent
// layout and chunk slices that exactly tile the file — so when the server-side
// fix lands (init consistency check + per-chunk byte-range enforcement +
// assembled-size recheck), the shipped client keeps working unchanged. If a
// future client refactor breaks any invariant below, it would start tripping
// the hardened server.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { uploadLargeFile, computeTotalChunks } from "../../lib/chunkupload.js";

function makeFile(size, name = "big.bin") {
  return new File([new Uint8Array(size)], name);
}

function requestPath(url) {
  return new URL(String(url), "http://localhost").pathname;
}

// installMocks captures every fetch and XHR call. Chunk POSTs go over XHR
// (uploadWithProgress), so their FormData is inspected via calls.push in send().
function installMocks(calls) {
  global.XMLHttpRequest = class MockXHR {
    constructor() {
      this.upload = {};
      this.status = 0;
      this.responseText = "";
    }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader() {}
    send(body) {
      calls.push({ kind: "chunk", url: this.url, body });
      this.status = 200;
      this.responseText = JSON.stringify({ ok: true, index: 0, crc32: "" });
      if (typeof this.onload === "function") this.onload();
    }
  };
  global.fetch = vi.fn(async (url, opts) => {
    const path = requestPath(url);
    calls.push({ kind: path.endsWith("/chunk/init") ? "init" : path.endsWith("/chunk/complete") ? "complete" : "other", url: String(url), opts });
    if (path.endsWith("/chunk/init")) {
      return { ok: true, json: async () => ({ uploadId: "up1", resume: false, received: [], chunkSize: 8, totalChunks: 3 }) };
    }
    if (path.endsWith("/chunk/complete")) {
      return { ok: true, json: async () => ({ ok: true, crc32: "deadbeef", path: "big.bin" }) };
    }
    return { ok: false, json: async () => ({ error: `unexpected path ${path}` }) };
  });
}

describe("chunked-upload client layout consistency (audit M-1)", () => {
  let calls;

  beforeEach(() => {
    calls = [];
    installMocks(calls);
  });

  it("sends an init layout where totalChunks === ceil(size / chunkSize) and size is the real file size", async () => {
    // 20 bytes in 8-byte chunks -> 3 chunks (last one short). Any layout the
    // hardened server will demand (see docs/SECURITY-AUDIT.md M-1 fix) must
    // hold for the shipped client: size honest, chunkSize as configured,
    // totalChunks derived, never inflated or deflated.
    const file = makeFile(20);
    await uploadLargeFile(file, "big.bin", { base: "/api/netdisk", dir: "", chunkSize: 8 });

    const init = calls.find((c) => c.kind === "init");
    expect(init).toBeTruthy();
    const body = JSON.parse(init.opts.body);
    expect(body.size).toBe(20);
    expect(body.chunkSize).toBe(8);
    expect(body.totalChunks).toBe(3);
    expect(computeTotalChunks(20, 8)).toBe(3);
    expect(computeTotalChunks(24, 8)).toBe(3); // exact multiple: no phantom short chunk
  });

  it("chunks exactly tile the file: every index once, each within chunkSize, bytes summing to size", async () => {
    const file = makeFile(20);
    await uploadLargeFile(file, "big.bin", { base: "/api/netdisk", dir: "", chunkSize: 8 });

    const chunks = calls.filter((c) => c.kind === "chunk");
    expect(chunks.length).toBe(3);

    const seenIdx = new Set();
    let totalBytes = 0;
    for (const c of chunks) {
      const fd = c.body;
      expect(fd).toBeInstanceOf(FormData);
      const index = Number(fd.get("index"));
      expect(Number.isInteger(index)).toBe(true);
      expect(seenIdx.has(index)).toBe(false); // each index uploaded exactly once
      seenIdx.add(index);
      expect(index).toBeGreaterThanOrEqual(0);
      expect(index).toBeLessThan(3);

      const blob = fd.get("chunk");
      expect(blob).toBeTruthy();
      // The byte-range enforcement the hardened server will add: a chunk's
      // payload may never exceed the declared chunkSize...
      expect(blob.size).toBeLessThanOrEqual(8);
      // ...and the declared size is only exceeded by no chunk at all.
      totalBytes += blob.size;
    }
    expect(totalBytes).toBe(20); // ...and the chunks sum to exactly the file size
  });

  it("sends the declared name on every protocol request (id ↔ path pairing the server checks)", async () => {
    const file = makeFile(16);
    await uploadLargeFile(file, "report.tar", { base: "/api/netdisk", dir: "", chunkSize: 8 });

    const init = calls.find((c) => c.kind === "init");
    expect(JSON.parse(init.opts.body).name).toBe("report.tar");
    for (const c of calls.filter((x) => x.kind === "chunk")) {
      expect(c.body.get("name")).toBe("report.tar");
      expect(c.body.get("uploadId")).toBe("up1");
    }
    const complete = calls.find((c) => c.kind === "complete");
    expect(JSON.parse(complete.opts.body).name).toBe("report.tar");
  });

  // Pin (docs/SECURITY-AUDIT.md M-1): the whole-file CRC32 is sent as "" by
  // the shipped client — so the server's optional whole-file CRC check never
  // fires in real traffic. The server compensates by re-checking the
  // assembled file SIZE against the declared size (assembleChunks), which the
  // honest client always satisfies. If client-side full-file hashing is ever
  // added, flip this to assert a real 8-hex-digit digest.
  it("sends fileCRC32 as empty on init and complete (server checks assembled size instead — audit M-1)", async () => {
    const file = makeFile(16);
    await uploadLargeFile(file, "big.bin", { base: "/api/netdisk", dir: "", chunkSize: 8 });
    const init = JSON.parse(calls.find((c) => c.kind === "init").opts.body);
    const complete = JSON.parse(calls.find((c) => c.kind === "complete").opts.body);
    expect(init.fileCRC32).toBe("");
    expect(complete.fileCRC32).toBe("");
  });
});
