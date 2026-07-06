package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// imagesDetailed returns the full local image inventory (admin/operator).
func (a *App) imagesDetailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := a.docker.ListImagesDetailed(r.Context())
	respond(w, items, err)
}

// imageBuildStream builds an image from a Dockerfile body via SSE.
func (a *App) imageBuildStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot build images")
		return
	}
	var req struct {
		Dockerfile string            `json:"dockerfile"`
		Tags       []string          `json:"tags"`
		BuildArgs  map[string]string `json:"buildArgs"`
		Name       string            `json:"name"`
		GroupIDs   []int64           `json:"groupIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Dockerfile = strings.TrimSpace(req.Dockerfile)
	if req.Dockerfile == "" {
		writeErr(w, http.StatusBadRequest, "dockerfile is required")
		return
	}
	if len(req.Tags) == 0 {
		writeErr(w, http.StatusBadRequest, "at least one tag is required")
		return
	}
	// Convert build args to the pointer map the API wants.
	args := map[string]*string{}
	for k, v := range req.BuildArgs {
		vv := v
		args[k] = &vv
	}
	auth := a.registryAuthForRef(req.Tags[0])

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
	send("progress", map[string]string{"message": "Building " + strings.Join(req.Tags, ", ")})

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	go func() {
		<-r.Context().Done()
		cancel()
	}()
	sseKeepalive(ctx, send)
	err := a.docker.BuildImage(ctx, dockerx.BuildOptions{
		Dockerfile: req.Dockerfile,
		Tags:       req.Tags,
		BuildArgs:  args,
		Auth:       auth,
	}, func(line string) {
		send("progress", map[string]string{"message": line})
	})
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	a.record(r, "image.build", strings.Join(req.Tags, ","))
	// Register the first tag in the mudp image catalog so it shows in the
	// images list immediately after the build, without requiring a separate
	// "Register" step.
	if len(req.Tags) > 0 {
		displayName := strings.TrimSpace(req.Name)
		if displayName == "" {
			displayName = dockerx.PublicImageName(req.Tags[0])
		}
		if displayName != "" {
			mudpRef := dockerx.MUDPImageRef(displayName)
			if err := a.docker.TagImage(r.Context(), req.Tags[0], mudpRef); err == nil {
				if dbErr := a.db.SaveImage(displayName, mudpRef, req.Tags[0]); dbErr == nil && len(req.GroupIDs) > 0 {
					u := currentUser(r)
					imgs, _ := a.db.ImagesForUser(u.ID, true)
					for _, img := range imgs {
						if img.DisplayName == displayName {
							_ = a.db.SetImageGroups(img.ID, req.GroupIDs)
							break
						}
					}
				}
			}
		}
	}
	send("done", map[string][]string{"tags": req.Tags})
}

// imageImport loads an image from an uploaded tar file via SSE.
func (a *App) imageImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot import images")
		return
	}
	// 256 MiB cap on uploaded image tarballs.
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
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
	send("progress", map[string]string{"message": "Loading image…"})
	err := a.docker.ImportImage(r.Context(), r.Body, func(line string) {
		send("progress", map[string]string{"message": line})
	})
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	a.record(r, "image.import", "tar")
	send("done", map[string]bool{"ok": true})
}

// imageSave streams a docker save tarball for download.
func (a *App) imageSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot export images")
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.tar"`, sanitizeFilename(ref)))
	if err := a.docker.SaveImage(r.Context(), ref, w); err != nil {
		// Headers already sent; best-effort error in body.
		fmt.Fprintf(w, "\n[export error: %v]\n", err)
	}
	a.record(r, "image.export", ref)
}

// imagePrune removes dangling images.
func (a *App) imagePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot prune images")
		return
	}
	count, bytes, err := a.docker.PruneImages(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "image.prune", u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"removed": count, "bytesFreed": bytes})
}

// imageTag applies an additional tag.
func (a *App) imageTag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot retag images")
		return
	}
	var req struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Src == "" || req.Dst == "" {
		writeErr(w, http.StatusBadRequest, "src and dst are required")
		return
	}
	if err := a.docker.TagImage(r.Context(), req.Src, req.Dst); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.record(r, "image.tag", req.Src+"→"+req.Dst)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// imagePush pushes an image to a registry via SSE.
func (a *App) imagePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot push images")
		return
	}
	var req struct {
		Ref string `json:"ref"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	auth := a.registryAuthForRef(req.Ref)
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
	send("progress", map[string]string{"message": "Pushing " + req.Ref})
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	go func() {
		<-r.Context().Done()
		cancel()
	}()
	sseKeepalive(ctx, send)
	err := a.docker.PushImage(ctx, req.Ref, auth, func(line string) {
		send("progress", map[string]string{"message": line})
	})
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	a.record(r, "image.push", req.Ref)
	send("done", map[string]string{"ref": req.Ref})
}

// imageRegister registers an existing local Docker image into the managed image
// list by tagging it under the mudp- namespace, without pulling from a registry.
func (a *App) imageRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot register images")
		return
	}
	var req struct {
		DockerRef string  `json:"dockerRef"`
		Name      string  `json:"name"`
		GroupIDs  []int64 `json:"groupIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.DockerRef = strings.TrimSpace(req.DockerRef)
	if req.DockerRef == "" {
		writeErr(w, http.StatusBadRequest, "dockerRef is required")
		return
	}
	displayName := dockerx.PublicImageName(req.Name)
	if displayName == "" {
		displayName = dockerx.PublicImageName(req.DockerRef)
	}
	mudpRef := dockerx.MUDPImageRef(displayName)
	if err := a.docker.TagImage(r.Context(), req.DockerRef, mudpRef); err != nil {
		writeErr(w, http.StatusBadRequest, "image not found locally: "+err.Error())
		return
	}
	if err := a.db.SaveImage(displayName, mudpRef, req.DockerRef); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(req.GroupIDs) > 0 {
		imgs, _ := a.db.ImagesForUser(u.ID, true)
		for _, img := range imgs {
			if img.DisplayName == displayName {
				_ = a.db.SetImageGroups(img.ID, req.GroupIDs)
				break
			}
		}
	}
	a.record(r, "image.register", req.DockerRef+"→"+mudpRef)
	writeJSON(w, http.StatusOK, map[string]string{"dockerRef": mudpRef, "name": displayName})
}

// registryAuthForRef resolves the registry auth blob for an image ref from the
// stored credentials. Empty when no match (public pulls).
func (a *App) registryAuthForRef(ref string) string {
	items, err := a.db.Registries()
	if err != nil || len(items) == 0 {
		return ""
	}
	creds := make([]dockerx.RegistryCred, 0, len(items))
	for _, it := range items {
		creds = append(creds, dockerx.RegistryCred{
			Host: it.URL, Username: it.Username, Token: it.Token,
		})
	}
	return dockerx.AuthForRef(ref, creds)
}

// sseSender returns a closure that writes one SSE event and flushes.
func sseSender(w http.ResponseWriter, flusher http.Flusher) func(event string, payload any) {
	var mu sync.Mutex
	return func(event string, payload any) {
		body, _ := json.Marshal(payload)
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// sanitizeFilename strips characters that break Content-Disposition.
func sanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, ":", "_")
	return s
}

// containerStatsStream emits live CPU/mem/net samples over SSE.
func (a *App) containerStatsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, id) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	interval := 2 * time.Second
	if s := r.URL.Query().Get("interval"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 1 && secs <= 30 {
			interval = time.Duration(secs) * time.Second
		}
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
	ch := make(chan dockerx.StatsSample, 4)
	// Resolve the container's GPU spec so live samples include GPU utilization.
	gpuSpec := ""
	if spec, err := a.docker.ContainerLabel(r.Context(), id, "mudp.gpu"); err == nil {
		gpuSpec = spec
	}
	// StreamStats runs until the request context is cancelled (client disconnect).
	go a.docker.StreamStats(r.Context(), id, gpuSpec, interval, ch)
	for {
		select {
		case sample, ok := <-ch:
			if !ok {
				return
			}
			send("sample", sample)
		case <-r.Context().Done():
			return
		}
	}
}

// containerLogsStream live-tails container logs over SSE.
func (a *App) containerLogsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, id) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	tail := "200"
	if t := r.URL.Query().Get("tail"); t != "" {
		tail = t
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
	// Use the existing docker logs reader in follow mode via a fresh stream.
	rc, err := a.docker.LogsStream(r.Context(), id, tail)
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	defer rc.Close()

	// Read in a dedicated goroutine so a blocked Docker log follower does not
	// keep this handler alive after the client disconnects.
	readCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(readCh)
		buf := make([]byte, 32*1024)
		for {
			n, err := rc.Read(buf)
			if n > 0 {
				// Copy the slice because buf is reused after the next Read.
				out := make([]byte, n)
				copy(out, buf[:n])
				readCh <- out
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	for {
		select {
		case data, ok := <-readCh:
			if !ok {
				return
			}
			send("log", map[string]string{"line": string(data)})
			if flusher != nil {
				flusher.Flush()
			}
		case <-errCh:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// imagePreset stores an admin-defined default configuration for an image. Admins
// use this to pre-wire per-image conventions (the VNC_PW password env var, the
// port an app listens on, the GPUs/devices it needs). Users picking the image get
// these defaults auto-filled in the create form but can still override them.
func (a *App) imagePreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ImageID int64              `json:"imageId"`
		Preset  *store.ImagePreset `json:"preset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ImageID == 0 {
		writeErr(w, http.StatusBadRequest, "imageId is required")
		return
	}
	if err := store.ValidatePreset(req.Preset); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.db.SetImagePreset(req.ImageID, req.Preset); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	respond(w, map[string]any{"ok": true}, nil)
}
