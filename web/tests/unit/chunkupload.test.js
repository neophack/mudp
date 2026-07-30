// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { uploadLargeFile, computeTotalChunks } from "../../lib/chunkupload.js";

describe("computeTotalChunks", () => {
  it("divides size into chunkSize pieces, rounding up for a short last chunk", () => {
    expect(computeTotalChunks(100, 10)).toBe(10);
    expect(computeTotalChunks(101, 10)).toBe(11);
  });
  it("returns 0 for a non-positive size or chunkSize", () => {
    expect(computeTotalChunks(0, 10)).toBe(0);
    expect(computeTotalChunks(10, 0)).toBe(0);
    expect(computeTotalChunks(-1, 10)).toBe(0);
  });
});

function makeFile(size, name = "big.bin") {
  return new File([new Uint8Array(size)], name);
}

// requestPath pulls the pathname a fetch/postJSON call hit, independent of
// whether it came in as a full URL string or a Request-like first arg.
function requestPath(url) {
  return new URL(String(url), "http://localhost").pathname;
}

describe("uploadLargeFile", () => {
  let calls;

  beforeEach(() => {
    calls = [];
    global.fetch = vi.fn(async (url, opts) => {
      calls.push({ url: String(url), opts });
      const path = requestPath(url);
      if (path.endsWith("/chunk/init")) {
        return {
          ok: true,
          json: async () => ({ uploadId: "up1", resume: false, received: [], chunkSize: 8, totalChunks: 2 }),
        };
      }
      if (path.endsWith("/chunk/complete")) {
        return { ok: true, json: async () => ({ ok: true, md5: "deadbeef", path: "big.bin" }) };
      }
      if (path.endsWith("/chunk")) {
        return { ok: true, json: async () => ({ ok: true, index: 0, md5: "" }) };
      }
      return { ok: false, json: async () => ({ error: `unexpected path ${path}` }) };
    });
  });

  // Regression coverage for a bug where the volume identifier and in-folder
  // path were never sent to the server at all (for volumes, resolving the
  // wrong/no volume) or were read from the wrong place server-side (for
  // netdisk, silently dropping the current folder). Both now travel as a
  // `?path=&name=` query string on every request in the protocol.
  it("carries the volume identifier and folder path as a query string on every request", async () => {
    const file = makeFile(16);
    const result = await uploadLargeFile(file, "big.bin", {
      base: "/api/volumes/files", dir: "docs/2024", volume: "myvol", chunkSize: 8, csrfToken: "tok",
    });
    expect(result.ok).toBe(true);
    expect(calls.length).toBeGreaterThanOrEqual(3); // init + >=1 chunk + complete
    for (const c of calls) {
      expect(c.url).toMatch(/[?&]path=docs%2F2024(&|$)/);
      expect(c.url).toMatch(/[?&]name=myvol(&|$)/);
    }
    expect(requestPath(calls[0].url)).toBe("/api/volumes/files/chunk/init");
    expect(requestPath(calls.at(-1).url)).toBe("/api/volumes/files/chunk/complete");
  });

  it("omits the query string for netdisk, which has no separate volume identifier", async () => {
    const file = makeFile(16);
    await uploadLargeFile(file, "big.bin", { base: "/api/netdisk", dir: "", chunkSize: 8 });
    for (const c of calls) {
      expect(c.url).not.toMatch(/\?/);
    }
  });

  it("sends only the current folder path when there is no volume", async () => {
    const file = makeFile(16);
    await uploadLargeFile(file, "big.bin", { base: "/api/netdisk", dir: "sub/dir", chunkSize: 8 });
    for (const c of calls) {
      expect(c.url).toMatch(/[?&]path=sub%2Fdir(&|$)/);
      expect(c.url).not.toMatch(/[?&]name=/);
    }
  });

  it("skips chunks the init response already reports as received (resume)", async () => {
    global.fetch = vi.fn(async (url) => {
      calls.push({ url: String(url) });
      const path = requestPath(url);
      if (path.endsWith("/chunk/init")) {
        // 16 bytes / 8-byte chunks = 2 chunks; chunk 0 already landed.
        return {
          ok: true,
          json: async () => ({ uploadId: "up1", resume: true, received: [0], chunkSize: 8, totalChunks: 2 }),
        };
      }
      if (path.endsWith("/chunk/complete")) {
        return { ok: true, json: async () => ({ ok: true, md5: "x", path: "big.bin" }) };
      }
      if (path.endsWith("/chunk")) {
        return { ok: true, json: async () => ({ ok: true, index: 1, md5: "" }) };
      }
      return { ok: false, json: async () => ({}) };
    });
    const file = makeFile(16);
    await uploadLargeFile(file, "big.bin", { base: "/api/netdisk", dir: "", chunkSize: 8 });
    const chunkPosts = calls.filter((c) => requestPath(c.url).endsWith("/chunk") && !requestPath(c.url).endsWith("/chunk/init"));
    expect(chunkPosts.length).toBe(1); // only the missing chunk (index 1) was sent
  });
});
