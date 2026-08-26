package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// processWatchInterval is how often the watcher polls each watched container.
const processWatchInterval = 10 * time.Second

// processes lists every process across the caller's running containers, plus
// the caller's active exit-watches. Admins see all containers.
func (a *App) processes(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	containers := a.runtimeContainers(u.Username, u.Role == "admin")
	processes := make([]dockerx.TopProcess, 0)
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		procs, err := a.docker.ContainerTop(r.Context(), c)
		if err != nil {
			continue
		}
		processes = append(processes, procs...)
	}
	watches, err := a.db.ProcessWatchesForUser(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load watches")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": processes, "watches": watches})
}

// containerProcesses lists the processes of one container the caller owns.
func (a *App) containerProcesses(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, id) {
		writeErr(w, http.StatusForbidden, "not your container")
		return
	}
	procs, err := a.docker.ContainerTop(r.Context(), dockerx.Container{ID: id})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list processes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": procs})
}

// processWatchAdd registers an exit-watch for one process. The container must
// belong to the caller; the PID must currently exist in it.
func (a *App) processWatchAdd(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		ContainerID string `json:"containerId"`
		PID         string `json:"pid"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ContainerID == "" || req.PID == "" {
		writeErr(w, http.StatusBadRequest, "containerId and pid are required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ContainerID) {
		writeErr(w, http.StatusForbidden, "not your container")
		return
	}
	procs, err := a.docker.ContainerTop(r.Context(), dockerx.Container{ID: req.ContainerID})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list processes")
		return
	}
	var command string
	for _, p := range procs {
		if p.PID == req.PID {
			command = p.Command
			break
		}
	}
	if command == "" {
		writeErr(w, http.StatusBadRequest, "process not found in container")
		return
	}
	name := a.containerDisplayName(req.ContainerID)
	id, err := a.db.AddProcessWatch(store.ProcessWatch{
		UserID: u.ID, ContainerID: req.ContainerID, ContainerName: name,
		PID: req.PID, Command: command,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to save watch")
		return
	}
	a.db.Audit(u.Username, "process.watch", fmt.Sprintf("%s pid=%s", name, req.PID))
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// processWatchDelete cancels one of the caller's exit-watches.
func (a *App) processWatchDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := a.db.DeleteProcessWatch(req.ID, u.ID, u.Role == "admin"); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to delete watch")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// watchProcesses is the watcher tick: for every registered watch, decide
// whether the watched process has ended, and fire the notification when it
// has. It runs on the background-jobs goroutine, so it needs no locking.
func (a *App) watchProcesses(ctx context.Context) {
	watches, err := a.db.ProcessWatches()
	if err != nil || len(watches) == 0 {
		return
	}
	for _, watch := range watches {
		procs, err := a.processProbe(ctx, watch.ContainerID)
		ended, reason := false, ""
		switch {
		case err != nil:
			if dockerx.IsUnavailableError(err) {
				continue // daemon hiccup: retry on the next tick
			}
			// The container itself is stopped or gone — its processes ended
			// with it. This is permanent, so fire without a grace period.
			ended, reason = true, "container stopped or removed"
		default:
			if cmd, ok := procs[watch.PID]; ok {
				// The PID still exists; a different command under the same PID
				// means the container restarted and reused it.
				if watch.Command != "" && cmd != watch.Command {
					ended, reason = true, "container restarted (PID reused)"
				}
			} else {
				ended, reason = true, "process exited"
			}
		}
		if ended {
			a.fireProcessWatch(watch, reason)
		}
	}
}

// fireProcessWatch removes the watch and notifies its owner through both the
// in-app feed and (for Feishu SSO users) the app bot.
func (a *App) fireProcessWatch(watch store.ProcessWatch, reason string) {
	if err := a.db.DeleteProcessWatch(watch.ID, watch.UserID, true); err != nil {
		log.Printf("process watch: delete %d: %v", watch.ID, err)
	}
	what := watch.Command
	if what == "" {
		what = "PID " + watch.PID
	}
	msg := fmt.Sprintf("%s — %s (container %s, PID %s)", what, reason, watch.ContainerName, watch.PID)
	_ = a.db.NotifyUser(watch.UserID, store.Notification{
		Type:    store.NotificationProcessFinished,
		Title:   "Process finished",
		Message: msg,
		Data:    map[string]any{"container": watch.ContainerName, "pid": watch.PID},
	})
	if u, err := a.db.UserByID(watch.UserID); err == nil && u.FeishuOpenID != "" {
		if err := a.sendFeishuText(u.ID, u.FeishuOpenID, store.FeishuKindProcessWatch, "MUDP 进程结束提醒: "+msg); err != nil {
			log.Printf("process watch: feishu notify user %d: %v", watch.UserID, err)
		}
	}
}

// defaultProcessProbe resolves one container's PID→command map. Kept as an
// App field so the watcher logic can be tested without a Docker daemon.
func (a *App) defaultProcessProbe(ctx context.Context, containerID string) (map[string]string, error) {
	procs, err := a.docker.ContainerTop(ctx, dockerx.Container{ID: containerID})
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(procs))
	for _, p := range procs {
		m[p.PID] = p.Command
	}
	return m, nil
}

// containerDisplayName resolves a container's short display name from the
// runtime cache, falling back to the truncated ID.
func (a *App) containerDisplayName(id string) string {
	for _, c := range a.runtimeContainers("", true) {
		if c.ID == id || len(c.ID) >= len(id) && c.ID[:len(id)] == id {
			return c.Name
		}
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
