package web

import "embed"

//go:embed index.html share.html share.js app.js styles.css world.geojson vendor modules lib
var Files embed.FS
