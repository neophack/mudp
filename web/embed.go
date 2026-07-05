package web

import "embed"

//go:embed index.html share.html share.js app.js styles.css vendor modules
var Files embed.FS
