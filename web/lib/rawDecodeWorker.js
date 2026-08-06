// Module worker used by lib/raw.js's decodeBayerParallel to demosaic one
// horizontal band of a RAW frame off the main thread. Stateless: each message
// is a self-contained decode job, reply carries just that band's RGBA bytes.

import { decodeBayer } from "./raw.js";

self.onmessage = (e) => {
  const { buf, width, height, bitDepth, patternKey, rowStart, rowEnd } = e.data;
  const rgba = decodeBayer(buf, width, height, bitDepth, patternKey, rowStart, rowEnd);
  self.postMessage({ rgba, rowStart, rowEnd }, [rgba.buffer]);
};
