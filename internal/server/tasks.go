package server

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"mudp/internal/store"
)

// ActiveTask represents one long-running, in-flight file operation: a bulk
// netdisk copy/move, a cross-disk transfer, a backup restore, or a
// (non-chunked) multi-file upload. Unlike BackupJob, active tasks are purely
// ephemeral — they exist only for the lifetime of the HTTP request that is
// doing the work and are removed the instant it returns (see startTask
// below), so the admin task list always answers "is anything in flight right
// now" rather than keeping history of finished work.
type ActiveTask struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	OwnerID   int64     `json:"ownerId"`
	OwnerName string    `json:"ownerName"`
	StartedAt time.Time `json:"startedAt"`
	Progress  int       `json:"progress"` // 0..100, -1 while indeterminate
	Done      int64     `json:"done"`
	Total     int64     `json:"total"`
	Message   string    `json:"message"`
	mu        sync.Mutex
}

func (t *ActiveTask) snapshot() ActiveTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	return ActiveTask{
		ID: t.ID, Kind: t.Kind, Name: t.Name,
		OwnerID: t.OwnerID, OwnerName: t.OwnerName,
		StartedAt: t.StartedAt, Progress: t.Progress,
		Done: t.Done, Total: t.Total, Message: t.Message,
	}
}

// setTotal records the unit count/byte count the task expects to process.
// Called once known (some callers know it up front, others only after an
// initial scan), so it is separate from newActiveTask.
func (t *ActiveTask) setTotal(n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Total = n
}

// addDone credits n more units/bytes as processed and recomputes Progress.
func (t *ActiveTask) addDone(n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Done += n
	if t.Total > 0 {
		t.Progress = min(int(t.Done*100/t.Total), 100)
	}
}

func (t *ActiveTask) setMessage(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Message = msg
}

// setName updates the display name after more precise information (e.g. the
// actual file count) becomes available post-registration.
func (t *ActiveTask) setName(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Name = name
}

// ActiveTaskRegistry tracks tasks currently in flight, keyed by id.
type ActiveTaskRegistry struct {
	mu    sync.Mutex
	tasks map[string]*ActiveTask
}

func NewActiveTaskRegistry() *ActiveTaskRegistry {
	return &ActiveTaskRegistry{tasks: make(map[string]*ActiveTask)}
}

// tasksReg lazily initializes the active-task registry, mirroring
// startBackupJob's guard for a.backupJobs: production always goes through
// New(), this only protects tests/tools that construct *App directly.
func (a *App) tasksReg() *ActiveTaskRegistry {
	if a.activeTasks == nil {
		a.activeTasks = NewActiveTaskRegistry()
	}
	return a.activeTasks
}

// chunkReg lazily initializes the chunked-upload session registry; see tasksReg.
func (a *App) chunkReg() *ChunkUploadRegistry {
	if a.chunkUploads == nil {
		a.chunkUploads = NewChunkUploadRegistry()
	}
	return a.chunkUploads
}

// start registers a new in-flight task and returns it plus a done func the
// caller must defer immediately, so the task is removed however the handler
// returns (success, validation error, or a later panic recovered upstream).
func (r *ActiveTaskRegistry) start(kind, name string, owner *store.User) (*ActiveTask, func()) {
	t := &ActiveTask{
		ID:        newActiveTaskID(),
		Kind:      kind,
		Name:      name,
		StartedAt: time.Now(),
		Progress:  -1,
	}
	if owner != nil {
		t.OwnerID = owner.ID
		t.OwnerName = owner.Username
	}
	r.mu.Lock()
	r.tasks[t.ID] = t
	r.mu.Unlock()
	return t, func() {
		r.mu.Lock()
		delete(r.tasks, t.ID)
		r.mu.Unlock()
	}
}

// snapshot returns every task in flight right now, oldest first.
func (r *ActiveTaskRegistry) snapshot() []ActiveTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ActiveTask, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, t.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func newActiveTaskID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("task_%d_%x", time.Now().UnixNano(), b[:])
}

// ---------------- Chunked-upload sessions ----------------
//
// A chunked upload spans many short-lived HTTP requests (init, one per chunk,
// complete/abort) rather than a single goroutine, so it can't report progress
// the way ActiveTask does. Instead we register a lightweight session at init
// and remove it at complete/abort; progress is computed on demand, by
// re-reading the on-disk resume state (chunkUploadState), only when the admin
// task list is actually requested.

type chunkUploadSession struct {
	ID        string
	Dst       string // absolute path; also the key used to re-read resume state
	Name      string
	Total     int64
	OwnerID   int64
	OwnerName string
	StartedAt time.Time
}

type ChunkUploadRegistry struct {
	mu       sync.Mutex
	sessions map[string]*chunkUploadSession // keyed by Dst
}

func NewChunkUploadRegistry() *ChunkUploadRegistry {
	return &ChunkUploadRegistry{sessions: make(map[string]*chunkUploadSession)}
}

// start registers (or refreshes) the session for dst. Called from chunk/init;
// harmless to call again for a resumed upload (just overwrites with the same
// identity).
func (r *ChunkUploadRegistry) start(dst, name string, total int64, owner *store.User) {
	s := &chunkUploadSession{ID: newActiveTaskID(), Dst: dst, Name: name, Total: total, StartedAt: time.Now()}
	if owner != nil {
		s.OwnerID = owner.ID
		s.OwnerName = owner.Username
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.sessions[dst]; ok {
		s.StartedAt = existing.StartedAt // preserve the original start time across resumes
	}
	r.sessions[dst] = s
}

// finish removes the session for dst (called from chunk/complete and
// chunk/abort). Safe to call even if no session was registered.
func (r *ChunkUploadRegistry) finish(dst string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, dst)
}

// chunkSessionStaleAfter bounds how long an upload with no forward progress
// (client crashed/closed the tab without calling abort) still counts as
// "active" for the admin view. The on-disk resume state is untouched either
// way -- a client can still come back and resume it later -- this only
// affects whether it's surfaced as a currently-running task.
const chunkSessionStaleAfter = 10 * time.Minute

// snapshot returns one ActiveTask per session that still has resume state on
// disk and was touched recently, re-reading that state for a fresh
// done/total. Stale or already-finished sessions (no state file left) are
// dropped from the registry as a side effect.
func (r *ChunkUploadRegistry) snapshot() []ActiveTask {
	r.mu.Lock()
	sessions := make([]*chunkUploadSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	out := make([]ActiveTask, 0, len(sessions))
	for _, s := range sessions {
		info, err := os.Stat(chunkStatePath(s.Dst))
		if err != nil {
			r.finish(s.Dst) // state file gone: completed or aborted elsewhere
			continue
		}
		if time.Since(info.ModTime()) > chunkSessionStaleAfter {
			continue // no recent activity; leave the resume state alone, just don't show it
		}
		st, err := readChunkState(s.Dst)
		if err != nil {
			continue
		}
		var done int64
		for i := range st.Received {
			start, end := chunkByteRange(st, i)
			done += end - start
		}
		total := s.Total
		if total <= 0 {
			total = st.Size
		}
		progress := -1
		if total > 0 {
			progress = min(int(done*100/total), 100)
		}
		out = append(out, ActiveTask{
			ID: s.ID, Kind: "netdisk.upload.chunked", Name: s.Name,
			OwnerID: s.OwnerID, OwnerName: s.OwnerName, StartedAt: s.StartedAt,
			Progress: progress, Done: done, Total: total,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// ---------------- Task list (HTTP) ----------------

// collectTasks merges every kind of long-running operation currently known to
// the server into one list: in-flight bulk netdisk copy/move/transfer/
// restore/upload requests, chunked upload sessions, and backup jobs still
// running. Shared by the admin-wide view and the per-user one below.
func (a *App) collectTasks() []ActiveTask {
	var out []ActiveTask
	if a.activeTasks != nil {
		out = append(out, a.activeTasks.snapshot()...)
	}
	if a.chunkUploads != nil {
		out = append(out, a.chunkUploads.snapshot()...)
	}
	if a.backupJobs != nil {
		jobs := a.backupJobs.snapshot()
		for i := range jobs {
			j := &jobs[i]
			if j.Status != "running" {
				continue
			}
			out = append(out, ActiveTask{
				ID: j.ID, Kind: j.Kind, Name: j.Name,
				OwnerID: j.OwnerID, OwnerName: j.OwnerName,
				StartedAt: j.StartedAt, Progress: j.Progress,
				Done: j.Done, Total: j.Total, Message: j.Message,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// adminTasks exposes every user's tasks (not just the caller's) for the
// admin-only "long-running tasks" card -- the whole point is letting an admin
// confirm nothing is in flight before restarting or updating the app, so it
// shows tasks of any age, not just ones that have been running a while.
func (a *App) adminTasks(w http.ResponseWriter, r *http.Request) {
	out := a.collectTasks()
	if out == nil {
		out = []ActiveTask{}
	}
	writeJSON(w, http.StatusOK, out)
}

// taskVisibleAfter is how long a task must have been running before it
// surfaces in the caller's own "Background jobs" panel. Most bulk copy/move/
// upload requests finish in well under this, so routine file operations never
// flash a job row; only ones long enough to matter show up.
const taskVisibleAfter = 10 * time.Second

// userTasks returns the caller's own long-running tasks (every user's, for an
// admin, matching backupJobsList's convention) that have been running longer
// than taskVisibleAfter, for the personal "Background jobs" panel.
//
// ?all=1 bypasses the taskVisibleAfter delay (still scoped to the caller's own
// tasks, or every user's for an admin): the floating copy/move progress
// overlay polls with this set so it can show live counts from the moment an
// operation starts, rather than waiting out the delay meant to keep routine
// fast copies from flashing a row in the Jobs panel.
func (a *App) userTasks(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeJSON(w, http.StatusOK, []ActiveTask{})
		return
	}
	admin := u.Role == store.RoleAdmin
	bypassDelay := r.URL.Query().Get("all") == "1"
	cutoff := time.Now().Add(-taskVisibleAfter)
	all := a.collectTasks()
	out := make([]ActiveTask, 0, len(all))
	for i := range all {
		t := &all[i]
		if !admin && t.OwnerID != u.ID {
			continue
		}
		if !bypassDelay && t.StartedAt.After(cutoff) {
			continue
		}
		// Rebuilt as a fresh literal (rather than copying *t) so the copy never
		// touches t's zero-value mutex field.
		out = append(out, ActiveTask{
			ID: t.ID, Kind: t.Kind, Name: t.Name,
			OwnerID: t.OwnerID, OwnerName: t.OwnerName,
			StartedAt: t.StartedAt, Progress: t.Progress,
			Done: t.Done, Total: t.Total, Message: t.Message,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
