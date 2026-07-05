package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// stacks handles GET (list) and POST (create/update).
func (a *App) stacks(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	switch r.Method {
	case http.MethodGet:
		stacks, err := a.db.StacksForUser(u.ID, u.Role == "admin")
		// Augment each with live container status from Docker.
		out := make([]stackView, 0, len(stacks))
		for _, s := range stacks {
			sv := stackView{Stack: s}
			if containers, err := a.docker.StackContainers(r.Context(), s.ProjectName); err == nil {
				sv.Services = len(containers)
				for _, c := range containers {
					if c.State == "running" {
						sv.Running++
					}
				}
			}
			out = append(out, sv)
		}
		respond(w, out, err)
	case http.MethodPost:
		if !canMutate(u) {
			writeErr(w, http.StatusForbidden, "read-only role cannot manage stacks")
			return
		}
		var req struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			ComposeYAML string `json:"composeYaml"`
			EnvRaw      string `json:"env"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.ComposeYAML = strings.TrimSpace(req.ComposeYAML)
		if req.Name == "" || req.ComposeYAML == "" {
			writeErr(w, http.StatusBadRequest, "name and compose yaml are required")
			return
		}
		// Validate the YAML parses and count services for quota guard.
		svcCount, err := dockerx.CountServices(req.ComposeYAML)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if svcCount == 0 {
			writeErr(w, http.StatusBadRequest, "compose file defines no services")
			return
		}
		env := dockerx.SplitEnvLines(req.EnvRaw)
		envJSON, _ := json.Marshal(env)

		if req.ID != 0 {
			// Update existing: ownership check.
			existing, err := a.db.StackByID(req.ID)
			if err != nil {
				writeErr(w, http.StatusNotFound, "stack not found")
				return
			}
			if existing.OwnerID != u.ID && u.Role != "admin" {
				writeErr(w, http.StatusForbidden, "stack is not yours")
				return
			}
			if err := a.db.UpdateStack(req.ID, req.ComposeYAML, string(envJSON)); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			a.record(r, "stack.update", req.Name)
			writeJSON(w, http.StatusOK, map[string]int64{"id": req.ID})
			return
		}
		project := dockerx.StackProjectName(u.Username, req.Name)
		id, err := a.db.CreateStack(u.ID, req.Name, req.ComposeYAML, string(envJSON), project)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.record(r, "stack.create", req.Name)
		writeJSON(w, http.StatusOK, map[string]int64{"id": id})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// stackGet returns a single stack (for the editor).
func (a *App) stackGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	id := parseID(r.URL.Query().Get("id"))
	if id == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	s, err := a.db.StackByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "stack not found")
		return
	}
	if s.OwnerID != u.ID && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "stack is not yours")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// stackDelete removes a stack record. It does NOT run `compose down` first —
// that's a separate explicit action so users can tear down before deleting.
func (a *App) stackDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot delete stacks")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	s, err := a.db.StackByID(req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "stack not found")
		return
	}
	if s.OwnerID != u.ID && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "stack is not yours")
		return
	}
	if err := a.db.DeleteStack(req.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "stack.delete", s.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// stackUpStream deploys a stack via `docker compose up -d`, streaming progress
// over SSE. Performs the container-cap quota guard before running.
func (a *App) stackUpStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot deploy stacks")
		return
	}
	if !dockerx.ComposeAvailable() {
		writeErr(w, http.StatusServiceUnavailable, "docker CLI with compose plugin is not installed on the host")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	s, err := a.db.StackByID(req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "stack not found")
		return
	}
	if s.OwnerID != u.ID && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "stack is not yours")
		return
	}

	// Quota guard: existing containers + new services must fit the cap.
	svcCount, _ := dockerx.CountServices(s.ComposeYAML)
	existing, _ := a.docker.ListContainers(r.Context(), u.Username, false)
	if u.Role != "admin" && len(existing)+svcCount > u.ContainerCap {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("stack would exceed container cap (%d + %d > %d)", len(existing), svcCount, u.ContainerCap))
		return
	}

	a.runStackSSE(w, r, s, "up", "deploying")
}

// stackDownStream tears down a stack via `docker compose down`, streaming.
func (a *App) stackDownStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot tear down stacks")
		return
	}
	if !dockerx.ComposeAvailable() {
		writeErr(w, http.StatusServiceUnavailable, "docker CLI with compose plugin is not installed on the host")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	s, err := a.db.StackByID(req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "stack not found")
		return
	}
	if s.OwnerID != u.ID && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "stack is not yours")
		return
	}
	a.runStackSSE(w, r, s, "down", "tearing down")
}

// runStackSSE is the shared up/down runner that emits progress over SSE and
// cancels on client disconnect.
func (a *App) runStackSSE(w http.ResponseWriter, r *http.Request, s store.Stack, verb, action string) {
	var env map[string]string
	_ = json.Unmarshal([]byte(s.EnvJSON), &env)
	composeBody := dockerx.SubstituteEnv(s.ComposeYAML, env)
	project, err := dockerx.NewComposeProject(s.ProjectName, composeBody, env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer project.Cleanup()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	send := func(event string, payload any) {
		body, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send("progress", map[string]string{"message": fmt.Sprintf("%s stack %s (%d services)", action, s.Name, mustCount(composeBody))})

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	go func() {
		<-r.Context().Done()
		cancel()
	}()

	progress := func(line string) {
		if line != "" {
			send("progress", map[string]string{"message": line})
		}
	}
	var runErr error
	if verb == "up" {
		runErr = project.ComposeUp(ctx, progress)
	} else {
		runErr = project.ComposeDown(ctx, progress)
	}
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			send("cancelled", map[string]string{"message": "cancelled by client"})
			return
		}
		send("error", map[string]string{"message": runErr.Error()})
		return
	}
	a.record(r, "stack."+verb, s.Name)
	send("done", map[string]string{"name": s.Name, "verb": verb})
}

func mustCount(composeYAML string) int {
	n, _ := dockerx.CountServices(composeYAML)
	return n
}

// stackView is the list-row: a stored stack plus live Docker status.
type stackView struct {
	store.Stack
	Services int `json:"services"`
	Running  int `json:"running"`
}
