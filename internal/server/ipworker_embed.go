package server

import (
	_ "embed"
	"net/http"
)

// ipworkerSource is the deployable Cloudflare Worker source, embedded so the
// binary is self-contained and the admin can copy it straight from the settings
// page without a separate repo checkout. The Worker exposes only an unauthenticated
// /whoami (the visitor's own IP + geo from request.cf), so the source carries no
// secrets — copying it is side-effect-free and safe to share.
//
//go:embed ipworker.js
var ipworkerSource string

// workerSourceHandler returns the Worker source as text/plain so the settings
// page can show it in a <pre> and offer a one-click copy. The source is a plain
// /whoami endpoint with no embedded credentials, so nothing here is sensitive.
func (a *App) workerSourceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// Inline-disposition so browsers render rather than download; the UI fetches
	// it via JS anyway.
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write([]byte(ipworkerSource))
}
