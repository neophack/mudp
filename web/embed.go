package web

import "embed"

//go:embed index.html app.js styles.css vendor modules
var Files embed.FS
