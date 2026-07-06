package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// fusedBuildStream drives a manual fused-layer build (admin "Build SSH Layer" /
// "Build VS Code Layer" buttons) and streams the docker build log live over
// Server-Sent Events. The layer image captures the file-system delta for a
// single service; it is later merged into a final fused image when a container
// is created with that service enabled.
// POST /api/scripts/fused/layers/build/stream  body: { baseImage, which }
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
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	send := sseSender(w, flusher)
	layerRef := plan.SSHLayerRef
	if req.Which == "vscode" {
		layerRef = plan.VSCodeLayerRef
	}
	// Send the layerRef up front so the client can check the final layer status
	// even if the SSE stream is dropped mid-build (proxy timeout, etc.): after the
	// stream ends it queries /api/scripts/fused/layers/list and matches by layerRef.
	send("progress", map[string]string{"message": "Building " + req.Which + " layer for " + req.BaseImage + "…", "layerRef": layerRef})

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	go func() {
		<-r.Context().Done()
		cancel()
	}()

	// Keep the SSE connection alive through reverse proxies during layer builds.
	sseKeepalive(ctx, send)

	err = a.docker.BuildFusedLayer(ctx, plan, req.Which, func(stage, msg string) {
		send("progress", map[string]string{"stage": stage, "message": msg})
	})
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}

	// Validate the layer by building a temporary runtime image and probing the
	// service in a throwaway container. Only record the layer as ready if it works.
	validationPassword, err := randomValidationPassword()
	if err != nil {
		send("error", map[string]string{"message": "generate validation password: " + err.Error()})
		return
	}
	if err := a.docker.ValidateFusedLayer(ctx, plan, req.Which, validationPassword, func(stage, msg string) {
		send("progress", map[string]string{"stage": stage, "message": msg})
	}); err != nil {
		send("error", map[string]string{"message": "Layer validation failed: " + err.Error()})
		return
	}

	// Record the fused_layers row only after the build and validation succeed, so
	// the orphan pruning in fusedLayerList can't nuke this entry mid-build.
	a.recordFusedLayer(plan, req.Which)
	a.record(r, "fused.layer.build", req.BaseImage+"/"+req.Which)
	send("done", map[string]string{
		"layerRef":  layerRef,
		"baseRef":   plan.BaseRef,
		"which":     req.Which,
		"readyAt":   time.Now().Format(time.RFC3339),
		"validated": "true",
	})
}

// randomValidationPassword returns a random 16-byte hex password used only for
// temporary layer-validation containers.
func randomValidationPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// fusedList returns all cached fused-image rows so the admin can see which
// final fused images exist locally. Final images are created lazily when a
// container with SSH/VSCode enabled is created.
// GET /api/scripts/fused/list
func (a *App) fusedList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := a.db.ListFusedImages()
	if err == nil {
		go a.pruneMissingFusedImages(items)
	}
	respond(w, items, err)
}

// fusedLayerList returns all cached fused-layer rows so the admin "Fused Layers"
// status card can show which incremental layers are pre-built and when.
// GET /api/scripts/fused/layers/list
func (a *App) fusedLayerList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := a.db.ListFusedLayers()
	if err == nil {
		go a.pruneMissingFusedLayers(items)
	}
	respond(w, items, err)
}

func (a *App) pruneMissingFusedImages(items []store.FusedImage) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, f := range items {
		exists, err := a.docker.ImageExists(ctx, f.FusedRef)
		if err == nil && !exists {
			_ = a.db.DeleteFusedImage(f.CacheKey)
		}
	}
}

func (a *App) pruneMissingFusedLayers(items []store.FusedLayer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, f := range items {
		exists, err := a.docker.ImageExists(ctx, f.LayerRef)
		if err == nil && !exists {
			_ = a.db.DeleteFusedLayer(f.CacheKey)
		}
	}
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
	// Prune dangling images afterwards so deleted layers do not leave <none>
	// garbage behind in Docker Desktop.
	_ = a.docker.RemoveManagedImage(r.Context(), req.FusedRef)
	_, _, _ = a.docker.PruneImages(r.Context())
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

// syncFusedCache scans Docker for locally-tagged mudp fused images and layers
// and ensures there is a database row for each one. This keeps the Settings
// image lists intact across restarts (the rows are the source of truth for the
// UI even when Docker images survive process restarts).
func (a *App) syncFusedCache(ctx context.Context) {
	infos, err := a.docker.ScanManagedImages(ctx)
	if err != nil {
		log.Printf("sync fused cache: scan images: %v", err)
		return
	}
	for _, info := range infos {
		// Newer images carry labels. For legacy layers built before labels were
		// added, fall back to parsing the image reference (e.g.
		// mudp-layer-ssh-mudp-ubuntu-latest-25b61121).
		if info.Labels["mudp.fused.layer"] == "true" || strings.HasPrefix(info.Ref, dockerx.Prefix+"layer-") {
			service, baseRef, ok := parseLayerRef(info.Ref)
			if !ok {
				continue
			}
			cacheKey := info.CacheKey
			if cacheKey == "" {
				// Legacy layer: synthesize a stable cache key from the ref so the
				// row survives until the user rebuilds or deletes it.
				cacheKey = "legacy:" + info.Ref
			}
			if info.Service != "" {
				service = info.Service
			}
			if info.BaseRef != "" {
				baseRef = info.BaseRef
			}
			if err := a.db.SaveFusedLayer(store.FusedLayer{
				CacheKey:    cacheKey,
				BaseRef:     baseRef,
				BaseImageID: info.BaseImageID,
				LayerRef:    info.Ref,
				Service:     service,
				ScriptHash:  info.ScriptHash,
			}); err != nil {
				log.Printf("sync fused cache: save layer %s: %v", info.Ref, err)
			}
			continue
		}
		if info.Labels["mudp.fused"] == "true" || strings.HasPrefix(info.Ref, dockerx.Prefix+"fused-") {
			if info.CacheKey == "" || info.BaseRef == "" {
				continue
			}
			enableSSH := strings.Contains(info.Labels["mudp.enable.ssh"], "true")
			enableVSCode := strings.Contains(info.Labels["mudp.enable.vscode"], "true")
			if err := a.db.SaveFusedImage(store.FusedImage{
				CacheKey:     info.CacheKey,
				BaseRef:      info.BaseRef,
				BaseImageID:  info.BaseImageID,
				FusedRef:     info.Ref,
				EnableSSH:    enableSSH,
				EnableVSCode: enableVSCode,
				ScriptHash:   info.ScriptHash,
			}); err != nil {
				log.Printf("sync fused cache: save image %s: %v", info.Ref, err)
			}
		}
	}
}

// parseLayerRef extracts the service ("ssh"/"vscode") and base image reference
// from a mudp incremental layer tag like
// "mudp-layer-ssh-mudp-ubuntu-latest-25b61121:latest".
// It is a best-effort fallback for legacy layers that were built before image
// labels were introduced.
func parseLayerRef(ref string) (service, baseRef string, ok bool) {
	ref = strings.TrimPrefix(ref, dockerx.Prefix+"layer-")
	before, _, _ := strings.Cut(ref, ":")
	parts := strings.Split(before, "-")
	if len(parts) < 5 {
		return "", "", false
	}
	service = parts[0]
	if service != "ssh" && service != "vscode" {
		return "", "", false
	}
	// Last part is the 8-char cache-key short hash.
	slugged := strings.Join(parts[1:len(parts)-1], "-")
	if slugged == "" {
		return "", "", false
	}
	// Best-effort recovery of the managed base tag: "...-latest" -> "...:latest".
	baseRef = strings.TrimSuffix(slugged, "-latest") + ":latest"
	return service, baseRef, true
}

// fusedLayerDelete removes a cached fused layer (both the Docker image and its row).
// POST /api/scripts/fused/layers/delete  body: { layerRef }
func (a *App) fusedLayerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		LayerRef string `json:"layerRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.LayerRef = strings.TrimSpace(req.LayerRef)
	if req.LayerRef == "" {
		writeErr(w, http.StatusBadRequest, "layerRef is required")
		return
	}
	// Remove the layer image (RemoveManagedImage guards on the mudp- prefix, which
	// mudp-layer-... satisfies), then drop the cache row by looking it up.
	// Prune dangling images afterwards so deleted layers do not leave <none>
	// garbage behind in Docker Desktop.
	_ = a.docker.RemoveManagedImage(r.Context(), req.LayerRef)
	_, _, _ = a.docker.PruneImages(r.Context())
	items, _ := a.db.ListFusedLayers()
	for _, f := range items {
		if f.LayerRef == req.LayerRef {
			_ = a.db.DeleteFusedLayer(f.CacheKey)
			break
		}
	}
	a.record(r, "fused.layer.delete", req.LayerRef)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
