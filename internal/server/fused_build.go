package server

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// fusedBuildStream drives a manual fused-image build (admin "Build SSH Image" /
// "Build VS Code Image" buttons) and streams the docker build log live over
// Server-Sent Events. The fused image pre-installs SSH and/or VSCode so
// containers created from the matching base image boot fast without re-running
// the install. A placeholder password is used — the real per-container password
// is applied at runtime via the MUDP_ACCESS_PASSWORD env var.
// POST /api/scripts/fused/build/stream  body: { baseImage, which }
func (a *App) fusedBuildStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	var req struct {
		BaseImage string `json:"baseImage"` // display name of a base image in the catalog
		Which     string `json:"which"`     // "ssh" or "vscode"
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.BaseImage = strings.TrimSpace(req.BaseImage)
	req.Which = strings.ToLower(strings.TrimSpace(req.Which))
	if req.BaseImage == "" {
		writeErr(w, http.StatusBadRequest, "baseImage is required")
		return
	}
	if req.Which != "ssh" && req.Which != "vscode" {
		writeErr(w, http.StatusBadRequest, "which must be ssh or vscode")
		return
	}
	// Resolve the base image record (admin sees all images).
	img, err := a.db.ImageByDisplayNameForUser(req.BaseImage, u.ID, true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unknown base image: "+req.BaseImage)
		return
	}
	scripts, err := a.db.ScriptSettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	enableSSH := req.Which == "ssh"
	enableVSCode := req.Which == "vscode"
	plan, err := a.fusedPlanForBuild(r.Context(), img.DockerRef, scripts.SSHScript, scripts.VSCodeScript, enableSSH, enableVSCode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Acc-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	send := sseSender(w, flusher)
	// Send the fusedRef up front so the client can check the final image status
	// even if the SSE stream is dropped mid-build (proxy timeout, etc.): after the
	// stream ends it queries /api/scripts/fused/list and matches by fusedRef.
	send("progress", map[string]string{"message": "Building " + req.Which + " image for " + req.BaseImage + "…", "fusedRef": plan.FusedRef})

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	go func() {
		<-r.Context().Done()
		cancel()
	}()

	err = a.docker.BuildFusedImage(ctx, plan, func(stage, msg string) {
		send("progress", map[string]string{"stage": stage, "message": msg})
	})
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	// Record the fused_images row only after the build succeeds, so the orphan
	// pruning in fusedList (which deletes rows whose image isn't present) can't
	// nuke this entry while the build is still running.
	a.recordFusedImage(plan)
	a.record(r, "fused.build", req.BaseImage+"/"+req.Which)
	send("done", map[string]string{
		"fusedRef": plan.FusedRef,
		"baseRef":  plan.BaseRef,
		"which":    req.Which,
		"readyAt":  time.Now().Format(time.RFC3339),
	})
}

// fusedList returns all cached fused-image rows so the admin "Fused Images"
// status card can show which base images are pre-built and when.
// GET /api/scripts/fused/list
func (a *App) fusedList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := a.db.ListFusedImages()
	// Prune rows whose image was removed out from under us so the UI stays honest.
	if err == nil {
		for _, f := range items {
			if exists, _ := a.docker.ImageExists(r.Context(), f.FusedRef); !exists {
				_ = a.db.DeleteFusedImage(f.CacheKey)
			}
		}
		items, _ = a.db.ListFusedImages()
	}
	respond(w, items, err)
}

// fusedDelete removes a cached fused image (both the Docker image and its row).
// POST /api/scripts/fused/delete  body: { fusedRef }
func (a *App) fusedDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		FusedRef string `json:"fusedRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.FusedRef = strings.TrimSpace(req.FusedRef)
	if req.FusedRef == "" {
		writeErr(w, http.StatusBadRequest, "fusedRef is required")
		return
	}
	// Remove the image (RemoveManagedImage guards on the mudp- prefix, which
	// mudp-fused-... satisfies), then drop the cache row by looking it up.
	_ = a.docker.RemoveManagedImage(r.Context(), req.FusedRef)
	items, _ := a.db.ListFusedImages()
	for _, f := range items {
		if f.FusedRef == req.FusedRef {
			_ = a.db.DeleteFusedImage(f.CacheKey)
			break
		}
	}
	a.record(r, "fused.delete", req.FusedRef)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
