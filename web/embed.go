package web

import "embed"

// Static assets for the web UI. YUV/RAW frame decoding now runs server-side
// (internal/server/raster.go), so the former web/wasm/ Go-compiled decoder is
// no longer built or embedded.
//
//go:embed index.html share.html share.js app.js styles.css world.geojson vendor modules lib
var Files embed.FS
