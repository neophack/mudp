// Module worker used by lib/raw.js's decodeBayerParallel to demosaic one
// horizontal band of a RAW frame off the main thread. Stateless: each message
// is a self-contained decode job, reply carries just that band's RGBA bytes.
//
// Decoding runs in the same Go-compiled WASM module as the main thread (see
// lib/wasmRaster.js, cmd/rasterwasm), but this worker loads its own instance
// with its own linear memory — that's what gives the pool real multi-core
// parallelism instead of contending on one WASM instance.

import * as wasmRaster from "./wasmRaster.js";

// Start loading/instantiating immediately so it's warm by the time the pool
// creator sends the first decode job, instead of waiting until onmessage.
wasmRaster.ensureRasterWasm();

self.onmessage = async (e) => {
  const { buf, width, height, bitDepth, patternKey, rowStart, rowEnd } = e.data;
  const rgba = await wasmRaster.bayerDecode(patternKey, bitDepth, width, height, rowStart, rowEnd, buf);
  self.postMessage({ rgba, rowStart, rowEnd }, [rgba.buffer]);
};
