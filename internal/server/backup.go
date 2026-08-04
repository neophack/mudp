package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mudp/internal/store"
)

// BackupJob is one in-flight or finished backup. The registry keeps pointers to
// these so the handlers can return live progress; the cancel field is the only
// non-serializable part and is stripped by snapshot().
type BackupJob struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // always "backup.run"
	Name       string    `json:"name"`
	Status     string    `json:"status"` // running / done / error / cancelled
	Message    string    `json:"message"`
	Progress   int       `json:"progress"` // 0..100
	Total      int64     `json:"total"`
	Done       int64     `json:"done"`
	FileCount  int       `json:"fileCount"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Scheduled  bool      `json:"scheduled,omitempty"`
	OwnerID    int64     `json:"ownerId"`
	OwnerName  string    `json:"ownerName"`
	cancel     context.CancelFunc
	mu         sync.Mutex
}

// BackupJobRegistry holds all backup jobs. Jobs are kept for the lifetime of the
// process (no persistence): a restart drops in-flight progress, which matches
// the existing client-side jobs model. Completed jobs stay so the user can see
// the final status until the registry grows past maxKept, at which point the
// oldest finished jobs are pruned.
type BackupJobRegistry struct {
	mu      sync.Mutex
	jobs    map[string]*BackupJob
	maxKept int
}

func NewBackupJobRegistry() *BackupJobRegistry {
	return &BackupJobRegistry{jobs: make(map[string]*BackupJob), maxKept: 50}
}

func (r *BackupJobRegistry) add(j *BackupJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[j.ID] = j
	r.pruneLocked()
}

func (r *BackupJobRegistry) get(id string) *BackupJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[id]
}

func (r *BackupJobRegistry) snapshot() []BackupJob {
	return r.snapshotForOwner(0, true)
}

// snapshotForOwner returns job snapshots visible to the caller: every job for
// an admin, or only jobs owned by ownerID otherwise. Filtering happens here
// (against the live *BackupJob pointers) rather than on an already-built
// []BackupJob, so it never has to copy a value that embeds a sync.Mutex.
func (r *BackupJobRegistry) snapshotForOwner(ownerID int64, admin bool) []BackupJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BackupJob, 0, len(r.jobs))
	for _, j := range r.jobs {
		if !admin && j.owner() != ownerID {
			continue
		}
		out = append(out, j.snapshot())
	}
	// Newest first, matching the client jobs panel sort.
	sort.Slice(out, func(a, b int) bool {
		return out[a].StartedAt.After(out[b].StartedAt)
	})
	return out
}

// pruneLocked keeps the registry from growing without bound by dropping the
// oldest finished jobs once we exceed maxKept. Running jobs are never pruned.
func (r *BackupJobRegistry) pruneLocked() {
	if len(r.jobs) <= r.maxKept {
		return
	}
	type fin struct {
		id string
		ts time.Time
	}
	var finished []fin
	running := 0
	for id, j := range r.jobs {
		j.mu.Lock()
		st := j.Status
		ts := j.FinishedAt
		j.mu.Unlock()
		if st == "running" {
			running++
			continue
		}
		finished = append(finished, fin{id, ts})
	}
	sort.Slice(finished, func(a, b int) bool { return finished[a].ts.Before(finished[b].ts) })
	drop := len(r.jobs) - r.maxKept
	for i := 0; i < drop && i < len(finished); i++ {
		delete(r.jobs, finished[i].id)
	}
	_ = running
}

// snapshot returns a copy of the job safe to serialize (no cancel field).
func (j *BackupJob) snapshot() BackupJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return BackupJob{
		ID:         j.ID,
		Kind:       j.Kind,
		Name:       j.Name,
		Status:     j.Status,
		Message:    j.Message,
		Progress:   j.Progress,
		Total:      j.Total,
		Done:       j.Done,
		FileCount:  j.FileCount,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
		Scheduled:  j.Scheduled,
		OwnerID:    j.OwnerID,
		OwnerName:  j.OwnerName,
	}
}

func (j *BackupJob) setStatus(status, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = status
	if msg != "" {
		j.Message = msg
	}
	if status != "running" {
		j.FinishedAt = time.Now()
	}
}

func (j *BackupJob) addDone(n int64, fileCount int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Done += n
	j.FileCount += fileCount
	if j.Total > 0 {
		j.Progress = int(j.Done * 100 / j.Total)
		if j.Progress > 100 {
			j.Progress = 100
		}
	}
}

func (j *BackupJob) setMessage(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Message = msg
}

// owner returns the job's owner id, safe for concurrent access.
func (j *BackupJob) owner() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.OwnerID
}

// ---------------- Job lifecycle ----------------

// startBackupJob launches a backup of src → dst in a detached goroutine so it
// survives the HTTP request (and the browser tab) that started it. Returns the
// job id so the caller can report it to the client.
func (a *App) startBackupJob(u *store.User, src, dst, name string, scheduled bool) (string, error) {
	if a.backupJobs == nil {
		a.backupJobs = NewBackupJobRegistry()
	}
	id := newBackupJobID()
	job := &BackupJob{
		ID:        id,
		Kind:      "backup.run",
		Name:      name,
		Status:    "running",
		Message:   "Starting…",
		StartedAt: time.Now(),
		Scheduled: scheduled,
		OwnerID:   u.ID,
		OwnerName: u.Username,
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel
	a.backupJobs.add(job)
	go a.runBackup(ctx, job, src, dst)
	return id, nil
}

// runBackup walks the source tree and copies every regular file to dst, renaming
// on collision via nextBackupName (date-time stamp). It reports progress per
// file and bails out as soon as the context is cancelled.
func (a *App) runBackup(ctx context.Context, job *BackupJob, src, dst string) {
	// Validate source exists. (We only need to confirm it's reachable; the
	// directory-vs-file distinction no longer branches the copy below.)
	if _, err := os.Stat(src); err != nil {
		job.setStatus("error", "Source not found: "+err.Error())
		return
	}
	// Pre-compute total size for the progress bar.
	total := pathSize(src)
	job.mu.Lock()
	job.Total = total
	job.mu.Unlock()
	job.setMessage(fmt.Sprintf("Backing up %s (%s)", filepath.Base(src), fmtByteCount(total)))

	// Create the destination directory tree. dst is the user's backup root joined
	// with the optional sub-path the picker chose; the source basename lands
	// inside it (nextBackupName handles a name clash with an existing backup).
	// A directory and a file both land at the same path: for a file, the
	// basename is the file name; for a directory, the basename is the folder
	// name and its contents are copied into it.
	dstBase := filepath.Join(dst, filepath.Base(src))
	dstBase = nextBackupName(filepath.Dir(dstBase), filepath.Base(dstBase))
	if err := os.MkdirAll(filepath.Dir(dstBase), 0750); err != nil {
		job.setStatus("error", "Cannot create backup directory: "+err.Error())
		return
	}

	var failed int
	walkErr := backupTree(ctx, src, dstBase, job, &failed)
	if ctx.Err() != nil {
		job.setStatus("cancelled", "Cancelled by user")
		return
	}
	if walkErr != nil {
		job.setStatus("error", "Backup failed: "+walkErr.Error())
		return
	}
	job.mu.Lock()
	job.Progress = 100
	job.mu.Unlock()
	msg := fmt.Sprintf("Completed: %d file(s) copied", job.FileCount)
	if failed > 0 {
		msg = fmt.Sprintf("Completed with %d failed file(s)", failed)
	}
	job.setStatus("done", msg)
}

// backupTree copies src into dst (a sibling copy of src's basename). It is a
// self-contained walker because, unlike the netdisk copy, it must (a) check the
// context between files so a cancel stops promptly on slow disks, (b) report
// per-file progress, and (c) rename-on-collision using date-time stamps.
func backupTree(ctx context.Context, src, dst string, job *BackupJob, failed *int) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			*failed++
			return nil // keep going past unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Never follow symlinks: a backup must not escape the source tree.
		if isSymlink(path) {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0750)
		}
		// Files land at their exact relative path; only the top-level basename
		// collision was resolved above, so individual files just overwrite the
		// freshly-created tree (they can't pre-exist in a brand-new dstBase).
		if err := copyFileProgress(ctx, path, target, job); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			*failed++
			job.setMessage("Skipped " + filepath.Base(path) + ": " + err.Error())
		}
		return nil
	})
}

// copyFileProgress copies a single regular file and credits the job with the
// byte count. It checks for cancellation before opening either file so a cancel
// during a long prior copy is honored immediately.
func copyFileProgress(ctx context.Context, src, dst string, job *BackupJob) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, in)
	cerr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if cerr != nil {
		return cerr
	}
	job.addDone(n, 1)
	return nil
}

func (a *App) cancelBackupJob(id string) bool {
	if a.backupJobs == nil {
		return false
	}
	job := a.backupJobs.get(id)
	if job == nil {
		return false
	}
	job.mu.Lock()
	running := job.Status == "running"
	cancel := job.cancel
	job.mu.Unlock()
	if !running {
		return false
	}
	if cancel != nil {
		cancel()
	}
	// runBackup will observe ctx.Done() and set "cancelled"; also set it here
	// so the UI flips immediately rather than waiting for the goroutine to wake.
	job.setStatus("cancelled", "Cancelled by user")
	return true
}

func newBackupJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("backup_%d_%x", time.Now().UnixNano(), b[:])
}

// fmtByteCount is a compact human-readable byte count for status messages.
func fmtByteCount(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---------------- HTTP handlers ----------------

// netdiskBackup starts one or more backup jobs for the caller's selected paths.
// mode "now" runs immediately; "idle" defers to the configured backup window.
// (Idle-mode scheduling is handled by the daily ticker; here "idle" only
// validates the window and rejects if we're outside it, so a user can't queue a
// huge backup during peak hours by mistake.)
func (a *App) netdiskBackup(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot back up")
		return
	}
	var req struct {
		Paths []string `json:"paths"`
		To    string   `json:"to"`   // optional sub-folder on the backup disk
		Mode  string   `json:"mode"` // "now" | "idle"
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "paths are required")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "now"
	}
	if mode == "idle" && !a.inBackupWindow() {
		writeErr(w, http.StatusForbidden, "outside the configured backup window")
		return
	}
	srcRoot, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	backupRoot, err := a.userBackupRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Resolve the destination inside the backup disk (root, or a sub-folder the
	// picker navigated into). cleanUserPath confines it under the backup root.
	dstRoot, _, err := cleanUserPath(backupRoot, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Disk-space guard: refuse before touching the slow disk if there clearly
	// isn't room. Best-effort — real free space may differ by the time we write.
	var required int64
	type item struct{ src, name string }
	var items []item
	for _, p := range req.Paths {
		full, _, err := cleanUserPath(srcRoot, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if info, err := os.Stat(full); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		} else if isSymlink(full) {
			continue
		} else {
			required += pathSize(full)
			items = append(items, item{src: full, name: info.Name()})
		}
	}
	if len(items) == 0 {
		writeErr(w, http.StatusBadRequest, "no valid items to back up")
		return
	}
	if free, err := diskFree(dstRoot); err == nil && free >= 0 && required > free {
		writeErr(w, http.StatusInsufficientStorage, "not enough free space on backup disk")
		return
	}

	// Each selected top-level item becomes its own backup job so progress and
	// cancellation are per-item. They all write into the same dstRoot.
	jobIDs := make([]string, 0, len(items))
	for _, it := range items {
		name := fmt.Sprintf("%s · %s", u.Username, it.name)
		id, err := a.startBackupJob(u, it.src, dstRoot, name, mode == "idle")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jobIDs = append(jobIDs, id)
	}
	a.record(r, "netdisk.backup", fmt.Sprintf("%d item(s)", len(items)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "jobIds": jobIDs, "count": len(jobIDs)})
}

// netdiskBackupAll triggers a backup of every activated user's netdisk into
// their group's backup disk. Admin-only. Used by the manual "back up everyone"
// button. The daily scheduler calls the shared runBackupAllForAllUsers helper
// directly (it has no *http.Request).
func (a *App) netdiskBackupAll(w http.ResponseWriter, r *http.Request) {
	started := a.runBackupAllForAllUsers(false)
	a.record(r, "netdisk.backup.all", fmt.Sprintf("%d user(s)", started))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": started})
}

// netdiskTransfer copies or moves files between the caller's netdisk and backup
// disk. It also supports same-disk transfers, but the UI keeps using the
// dedicated same-disk endpoints for that path.
func (a *App) netdiskTransfer(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify files")
		return
	}
	var req struct {
		FromDisk string `json:"fromDisk"` // netdisk | backup | shareddisk
		ToDisk   string `json:"toDisk"`   // netdisk | backup | shareddisk
		Items    []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"items"`
		Move   bool   `json:"move"`
		Policy string `json:"policy"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "items are required")
		return
	}
	fromDisk := strings.ToLower(strings.TrimSpace(req.FromDisk))
	toDisk := strings.ToLower(strings.TrimSpace(req.ToDisk))
	// "shareddisk" (共享盘) is named distinctly from the netdisk's own
	// external-share-link feature ("share"/分享链接, netdiskShare* below) so
	// the two are never confused in a request payload.
	validDisk := func(d string) bool { return d == "netdisk" || d == "backup" || d == "shareddisk" }
	if !validDisk(fromDisk) || !validDisk(toDisk) {
		writeErr(w, http.StatusBadRequest, "fromDisk/toDisk must be netdisk, backup or shareddisk")
		return
	}
	policy := strings.ToLower(strings.TrimSpace(req.Policy))
	if policy == "" {
		policy = "rename"
	}
	if policy != "overwrite" && policy != "skip" && policy != "rename" {
		writeErr(w, http.StatusBadRequest, "policy must be overwrite, skip or rename")
		return
	}

	fromRoot, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch fromDisk {
	case "backup":
		fromRoot, err = a.userBackupRoot(u)
	case "shareddisk":
		// The whole pool is the read root: copying out of someone else's
		// shared folder is fine, only moving out of it is not (checked below,
		// per item, since only a move mutates the source).
		fromRoot, err = a.sharedDiskGroupRoot(u)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	toRoot, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch toDisk {
	case "backup":
		toRoot, err = a.userBackupRoot(u)
	case "shareddisk":
		// Ensure the caller's own subfolder exists before anything is written
		// into it, mirroring userNetdiskRoot's auto-create behaviour.
		if _, err2 := a.userSharedDiskOwnRoot(u); err2 != nil {
			writeErr(w, http.StatusBadRequest, err2.Error())
			return
		}
		toRoot, err = a.sharedDiskGroupRoot(u)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var required int64
	for _, it := range req.Items {
		src, srcRel, err := cleanUserPath(fromRoot, it.From)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if fromDisk == "shareddisk" && req.Move {
			if err := requireOwnSharedDiskPath(u, srcRel); err != nil {
				writeErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
		if isSymlink(src) {
			continue
		}
		required += pathSize(src)
	}
	if toDisk == "netdisk" {
		used := dirSize(toRoot)
		if u.NetdiskQuotaBytes > 0 && used+required > u.NetdiskQuotaBytes {
			writeErr(w, http.StatusInsufficientStorage, "transfer would exceed netdisk quota")
			return
		}
	}
	if len(req.Items) > 0 {
		dstProbe, _, err := cleanUserPath(toRoot, req.Items[0].To)
		if err == nil {
			if free, err := diskFree(dstProbe); err == nil && free >= 0 && required > free {
				writeErr(w, http.StatusInsufficientStorage, "not enough free disk space")
				return
			}
		}
	}

	task, doneTask := a.tasksReg().start("netdisk.transfer", fmt.Sprintf("%s · %s→%s · %d item(s)", u.Username, fromDisk, toDisk, len(req.Items)), u)
	task.setTotal(int64(len(req.Items)))
	defer doneTask()

	results := make([]map[string]string, 0, len(req.Items))
	count := 0
	for _, item := range req.Items {
		res := map[string]string{"from": item.From, "to": item.To}
		from, fromRel, err := cleanUserPath(fromRoot, item.From)
		if err == nil && fromDisk == "shareddisk" && req.Move {
			err = requireOwnSharedDiskPath(u, fromRel)
		}
		if err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
			results = append(results, res)
			task.addDone(1)
			continue
		}
		to, toRel, err := cleanUserPath(toRoot, item.To)
		if err == nil && toDisk == "shareddisk" {
			err = requireOwnSharedDiskPath(u, toRel)
		}
		if err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
			results = append(results, res)
			task.addDone(1)
			continue
		}
		task.setMessage(filepath.Base(from))
		if err := netdiskCopyOne(from, to, req.Move, policy); err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
		} else {
			if req.Move {
				res["status"] = "moved"
			} else {
				res["status"] = "copied"
			}
			count++
		}
		results = append(results, res)
		task.addDone(1)
	}
	a.record(r, "netdisk.transfer", fmt.Sprintf("%s->%s %d item(s)", fromDisk, toDisk, count))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "results": results})
}

func (a *App) backupJobsList(w http.ResponseWriter, r *http.Request) {
	if a.backupJobs == nil {
		writeJSON(w, http.StatusOK, []BackupJob{})
		return
	}
	u := currentUser(r)
	if u == nil {
		writeJSON(w, http.StatusOK, []BackupJob{})
		return
	}
	// Non-admins only ever see their own jobs; without this filter any
	// authenticated user could watch (and, via cancel, interfere with) every
	// other user's backups.
	writeJSON(w, http.StatusOK, a.backupJobs.snapshotForOwner(u.ID, u.Role == store.RoleAdmin))
}

// netdiskBackupBrowse lists directories on the caller's backup disk so the
// backup picker can navigate it (mirrors netdiskList but rooted at the backup
// disk, read-only, directories only — the picker only needs to pick a folder).
// netdiskBackupBrowse lists the caller's backup disk for the full management
// view (directories + files, with sizes and timestamps). It mirrors netdiskList
// but is rooted at the backup disk. Also serves the backup picker's
// directory-only navigation (the picker filters client-side).
func (a *App) netdiskBackupBrowse(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	root, err := a.userBackupRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	reqPath := strings.TrimSpace(r.URL.Query().Get("path"))
	// Lazily create the per-user backup dir on first browse so the view isn't
	// stuck on a "not found" error when no backup has ever run yet.
	if err := os.MkdirAll(root, 0750); err != nil {
		if reqPath == "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"path":        "",
				"items":       []fileItem{},
				"unavailable": true,
				"message":     "Backup disk is unavailable or not mounted. Ask an admin to mount the configured backup device.",
			})
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, rel, err := cleanUserPath(root, reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if reqPath == "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"path":        "",
				"items":       []fileItem{},
				"unavailable": true,
				"message":     "Backup disk is unavailable or not mounted. Ask an admin to mount the configured backup device.",
			})
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items := make([]fileItem, 0, len(entries))
	used, stale, updatedAt := a.dirSizeCached(root)
	quota := map[string]any{"usedBytes": used}
	if stale {
		quota["usedEstimating"] = true
	}
	if updatedAt != "" {
		quota["usedUpdatedAt"] = updatedAt
	}
	if free, err := diskFree(root); err == nil && free >= 0 {
		quota["diskFreeBytes"] = free
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Use Lstat so symlinks are not followed; skip them entirely.
		li, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil || li.Mode()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		if rel == "." {
			p = entry.Name()
		}
		items = append(items, fileItem{Name: entry.Name(), Path: p, Dir: entry.IsDir(), Size: info.Size(), ModTime: info.ModTime().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.ToSlash(rel), "items": items, "quota": quota, "unavailable": false})
}

// netdiskBackupMkdir creates a folder on the caller's backup disk (used by the
// backup picker's "+ New Folder" button).
func (a *App) netdiskBackupMkdir(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify backup disk")
		return
	}
	root, err := a.userBackupRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	full, _, err := cleanUserPath(root, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true}, os.MkdirAll(full, 0750))
}

// netdiskBackupDelete removes files/folders from the caller's backup disk.
// Mirrors netdiskDelete but rooted at the backup disk.
func (a *App) netdiskBackupDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify backup disk")
		return
	}
	root, err := a.userBackupRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "paths are required")
		return
	}
	for _, p := range req.Paths {
		full, _, err := cleanUserPath(root, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.RemoveAll(full); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// netdiskBackupRename moves a file/folder within the backup disk.
func (a *App) netdiskBackupRename(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify backup disk")
		return
	}
	root, err := a.userBackupRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(r, &req); err != nil || req.From == "" || req.To == "" {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	from, _, err := cleanUserPath(root, req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to, _, err := cleanUserPath(root, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(to), 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, map[string]bool{"ok": true}, os.Rename(from, to))
}

// netdiskBackupCopy duplicates files/folders within the backup disk (the picker
// is the netdisk one — copying from backup back into the netdisk uses the same
// netdisk copy endpoint with the source read off the backup disk). For in-disk
// copies we reuse netdiskCopyOne with the backup rename policy.
func (a *App) netdiskBackupCopy(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify backup disk")
		return
	}
	root, err := a.userBackupRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Items []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"items"`
		Move   bool   `json:"move"`
		Policy string `json:"policy"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "items are required")
		return
	}
	policy := strings.ToLower(strings.TrimSpace(req.Policy))
	if policy == "" {
		policy = "rename"
	}
	if policy != "overwrite" && policy != "skip" && policy != "rename" {
		writeErr(w, http.StatusBadRequest, "policy must be overwrite, skip or rename")
		return
	}
	kind := "netdisk.copy"
	if req.Move {
		kind = "netdisk.move"
	}
	task, doneTask := a.tasksReg().start(kind, fmt.Sprintf("%s · backup · %d item(s)", u.Username, len(req.Items)), u)
	task.setTotal(int64(len(req.Items)))
	defer doneTask()

	var results []map[string]string
	count := 0
	for _, item := range req.Items {
		res := map[string]string{"from": item.From, "to": item.To}
		from, _, err := cleanUserPath(root, item.From)
		if err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
			results = append(results, res)
			task.addDone(1)
			continue
		}
		to, _, err := cleanUserPath(root, item.To)
		if err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
			results = append(results, res)
			task.addDone(1)
			continue
		}
		task.setMessage(filepath.Base(from))
		if err := netdiskCopyOne(from, to, req.Move, policy); err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
		} else {
			if req.Move {
				res["status"] = "moved"
			} else {
				res["status"] = "copied"
			}
			count++
		}
		results = append(results, res)
		task.addDone(1)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "results": results})
}

// netdiskBackupRestore copies selected files/folders from the caller's backup
// disk back to their netdisk.
func (a *App) netdiskBackupRestore(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot restore files")
		return
	}
	bkRoot, err := a.userBackupRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	netdiskRoot, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Paths  []string `json:"paths"`
		To     string   `json:"to"`
		Policy string   `json:"policy"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "paths are required")
		return
	}
	policy := strings.ToLower(strings.TrimSpace(req.Policy))
	if policy == "" {
		policy = "rename"
	}
	if policy != "overwrite" && policy != "skip" && policy != "rename" {
		writeErr(w, http.StatusBadRequest, "policy must be overwrite, skip or rename")
		return
	}
	dstDir, _, err := cleanUserPath(netdiskRoot, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dstDir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	type task struct {
		src  string
		dst  string
		path string
	}
	planned := make(map[string]bool)
	tasks := make([]task, 0, len(req.Paths))
	var required int64
	for _, p := range req.Paths {
		src, _, err := cleanUserPath(bkRoot, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if isSymlink(src) {
			continue
		}
		info, err := os.Stat(src)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dst := filepath.Join(dstDir, info.Name())
		if policy == "rename" {
			dst = nextFreeNameWithPlanned(filepath.Dir(dst), filepath.Base(dst), planned)
		}
		planned[dst] = true
		required += pathSize(src)
		tasks = append(tasks, task{src: src, dst: dst, path: p})
	}
	if len(tasks) == 0 {
		writeErr(w, http.StatusBadRequest, "no valid items to restore")
		return
	}

	used := dirSize(netdiskRoot)
	if u.NetdiskQuotaBytes > 0 && used+required > u.NetdiskQuotaBytes {
		writeErr(w, http.StatusInsufficientStorage, "restore would exceed netdisk quota")
		return
	}
	if free, err := diskFree(dstDir); err == nil && free >= 0 && required > free {
		writeErr(w, http.StatusInsufficientStorage, "not enough free disk space")
		return
	}

	activeTask, doneTask := a.tasksReg().start("netdisk.restore", fmt.Sprintf("%s · %d item(s)", u.Username, len(tasks)), u)
	activeTask.setTotal(int64(len(tasks)))
	defer doneTask()

	results := make([]map[string]string, 0, len(tasks))
	count := 0
	for _, t := range tasks {
		res := map[string]string{"from": t.path, "to": filepath.ToSlash(t.dst)}
		activeTask.setMessage(filepath.Base(t.src))
		if err := copyPathWithPolicy(t.src, t.dst, policy); err != nil {
			res["status"] = "error"
			res["error"] = err.Error()
		} else {
			res["status"] = "restored"
			count++
		}
		results = append(results, res)
		activeTask.addDone(1)
	}
	a.record(r, "netdisk.backup.restore", fmt.Sprintf("%d item(s)", count))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "results": results})
}

// netdiskBackupDownload serves a single backup-disk file (or zips a folder) for
// download. Mirrors netdiskDownload.
func (a *App) netdiskBackupDownload(w http.ResponseWriter, r *http.Request) {
	root, err := a.userBackupRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if !info.IsDir() {
		serveFileDownload(w, r, full, info.Name())
		return
	}
	serveZipDownload(w, full, info.Name()+".zip")
}

// netdiskBackupRaw serves a single backup-disk file inline for preview (PDF /
// image / video / text). Mirrors netdiskRaw.
func (a *App) netdiskBackupRaw(w http.ResponseWriter, r *http.Request) {
	root, err := a.userBackupRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "path is a folder")
		return
	}
	serveFileInline(w, r, full, info.Name())
}

func (a *App) backupJobCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	u := currentUser(r)
	if a.backupJobs != nil {
		if job := a.backupJobs.get(req.ID); job != nil {
			if u == nil || (u.Role != store.RoleAdmin && job.owner() != u.ID) {
				writeErr(w, http.StatusForbidden, "job is not yours")
				return
			}
		}
	}
	if !a.cancelBackupJob(req.ID) {
		writeErr(w, http.StatusNotFound, "job not found or not running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// backupScheduleGet/Post read and write the single-row daily schedule. Admin-only.
func (a *App) backupScheduleGet(w http.ResponseWriter, r *http.Request) {
	s, err := a.db.BackupScheduleGet()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *App) backupSchedulePost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hour    int  `json:"hour"`
		Minute  int  `json:"minute"`
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := a.db.BackupScheduleSet(req.Hour, req.Minute, req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "backup.schedule", fmt.Sprintf("%02d:%02d enabled=%v", req.Hour, req.Minute, req.Enabled))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// inBackupWindow reports whether "now" is inside the configured daily backup
// window. The window is intentionally generous: from the scheduled time through
// the following 6 hours, so a 02:00 schedule covers 02:00–08:00.
func (a *App) inBackupWindow() bool {
	s, err := a.db.BackupScheduleGet()
	if err != nil || !s.Enabled {
		return false
	}
	now := time.Now()
	cur := now.Hour()*60 + now.Minute()
	start := s.Hour*60 + s.Minute
	end := start + 6*60
	if end > 24*60 {
		end -= 24 * 60
		return cur >= start || cur < end
	}
	return cur >= start && cur < end
}

// maybeRunScheduledBackup fires the once-daily all-user backup when the clock
// reaches the configured HH:MM. The last_run_at timestamp guards against the
// per-minute ticker firing twice within the same minute or re-firing across a
// restart that lands inside the window.
func (a *App) maybeRunScheduledBackup(ctx context.Context) {
	s, err := a.db.BackupScheduleGet()
	if err != nil || !s.Enabled {
		return
	}
	now := time.Now()
	// Only fire in the exact scheduled minute (the ticker checks every minute).
	if now.Hour() != s.Hour || now.Minute() != s.Minute {
		return
	}
	// Already ran today: skip. Parse last_run_at defensively.
	if s.LastRunAt != "" {
		if last, err := time.Parse(time.RFC3339, s.LastRunAt); err == nil {
			// Same calendar day (by date, not 24h) → already ran.
			if last.Year() == now.Year() && last.YearDay() == now.YearDay() {
				return
			}
		}
	}
	// Mark first so a slow all-user backup doesn't get re-triggered by the next
	// minute's tick while the first one is still running.
	_ = a.db.BackupScheduleMarkRun(now.Format(time.RFC3339))
	a.runBackupAllForAllUsers(true)
}

// runBackupAllForAllUsers is the shared body of the manual and scheduled
// "back up everyone" path. It mirrors netdiskBackupAll but lives here so the
// scheduler (which has no *http.Request) can call it.
func (a *App) runBackupAllForAllUsers(scheduled bool) int {
	users, err := a.db.Users()
	if err != nil {
		return 0
	}
	var started int
	for i := range users {
		u := &users[i]
		if u.Disabled {
			continue
		}
		srcRoot, err := a.userNetdiskRoot(u)
		if err != nil {
			continue
		}
		dstRoot, err := a.userBackupRoot(u)
		if err != nil {
			continue
		}
		if info, err := os.Stat(srcRoot); err != nil || !info.IsDir() {
			continue
		}
		name := fmt.Sprintf("%s (scheduled)", u.Username)
		if _, err := a.startBackupJob(u, srcRoot, dstRoot, name, scheduled); err != nil {
			continue
		}
		started++
	}
	return started
}
