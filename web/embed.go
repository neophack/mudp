package web

import "embed"

// Static assets for the web UI. dist/ holds the built Vue SPA (npm run build);
// share.html/share.js/share.css and lib/ back the standalone public share
// page, which stays framework-free. A minimal dist/index.html placeholder is
// committed so `go build` works without a node toolchain; run `npm run build`
// in web/ to produce the real bundle.
//
//go:embed all:dist share.html share.js share.css lib
var Files embed.FS
