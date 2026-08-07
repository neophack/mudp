// Bridge to the Go-compiled WASM raster decoder (cmd/rasterwasm), used by
// both the main thread (lib/yuv.js, lib/raw.js) and the RAW-decode worker
// pool (lib/rawDecodeWorker.js). Each context gets its own WASM instance and
// linear memory — instantiating is cheap enough (once per viewer/worker
// lifetime, not per frame) that there's no need to share one across threads.
//
// wasm_exec.js (loaded as a classic <script> in index.html/share.html, or
// imported for its side effect here inside the worker) defines
// globalThis.Go. The compiled program registers globalThis.mudpRaster and
// then blocks forever (see cmd/rasterwasm/main.go) — go.run() therefore
// never resolves, so we don't await it; instead the program calls
// globalThis.__mudpRasterReady() right after registering its exports, and
// that's what ensureRasterWasm() actually waits on.

let readyPromise = null;

export function ensureRasterWasm() {
  if (!readyPromise) {
    readyPromise = (async () => {
      if (typeof globalThis.Go !== "function") {
        // Inside a module Worker, wasm_exec.js isn't loaded by the host page.
        await import("/wasm/wasm_exec.js");
      }
      const go = new globalThis.Go();
      const resp = await fetch("/wasm/raster.wasm");
      const bytes = await resp.arrayBuffer();
      const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
      const ready = new Promise((resolve) => {
        globalThis.__mudpRasterReady = resolve;
      });
      go.run(instance); // never resolves (main blocks on select{}); don't await
      await ready;
    })();
  }
  return readyPromise;
}

export async function yuvDecode(format, width, height, src) {
  await ensureRasterWasm();
  return globalThis.mudpRaster.yuvDecode(format, width, height, src);
}

export async function bayerDecode(pattern, bitDepth, width, height, rowStart, rowEnd, src) {
  await ensureRasterWasm();
  return globalThis.mudpRaster.bayerDecode(pattern, bitDepth, width, height, rowStart, rowEnd, src);
}

export async function applyIsp(rgba, saturation = 1.4) {
  await ensureRasterWasm();
  return globalThis.mudpRaster.applyIsp(rgba, saturation);
}
