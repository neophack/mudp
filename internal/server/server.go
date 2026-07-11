package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"mudp/internal/auth"
	"mudp/internal/config"
	"mudp/internal/dockerx"
	"mudp/internal/mcp"
	"mudp/internal/middleware"
	"mudp/internal/store"
	"mudp/web"
)

type App struct {
	cfg              config.Config
	db               *store.DB
	docker           *dockerx.Client
	auth             auth.Signer
	mcpHub           *mcp.SSEHub
	lastSnapshot     time.Time
	snapshotMu       sync.Mutex
	cacheMu          sync.RWMutex
	refreshMu        sync.Mutex
	cachedSystem     dockerx.SystemInfo
	cachedContainers []dockerx.Container
	cacheAt          time.Time
	registryMu       sync.Mutex
}

type contextKey string

const userKey contextKey = "user"

// maxWSFrameSize caps the payload of a single WebSocket frame to prevent a
// malicious client from asking the server to allocate arbitrary memory.
const maxWSFrameSize = 1 << 20 // 1 MiB

func New(cfg config.Config, db *store.DB) (*App, error) {
	dc, err := dockerx.NewWithHost(cfg.DockerHost)
	if err != nil {
		return nil, err
	}
	return &App{
		cfg: cfg, db: db, docker: dc, auth: auth.New(cfg.SessionSecret),
		mcpHub: mcp.NewSSEHub(),
	}, nil
}

// Close releases resources held by the app, such as the Docker client.
func (a *App) Close() error {
	if a.docker == nil {
		return nil
	}
	return a.docker.Close()
}

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func (a *App) readyz(w http.ResponseWriter, r *http.Request) {
	if err := a.docker.DockerPing(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(w, "# HELP mudp_go_goroutines Number of running goroutines.\n")
	fmt.Fprintf(w, "# TYPE mudp_go_goroutines gauge\n")
	fmt.Fprintf(w, "mudp_go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP mudp_go_memory_bytes_alloc_total Total allocated bytes.\n")
	fmt.Fprintf(w, "# TYPE mudp_go_memory_bytes_alloc_total counter\n")
	fmt.Fprintf(w, "mudp_go_memory_bytes_alloc_total %d\n", ms.TotalAlloc)
	fmt.Fprintf(w, "# HELP mudp_go_memory_bytes_heap_inuse In-use heap bytes.\n")
	fmt.Fprintf(w, "# TYPE mudp_go_memory_bytes_heap_inuse gauge\n")
	fmt.Fprintf(w, "mudp_go_memory_bytes_heap_inuse %d\n", ms.HeapInuse)
}

func (a *App) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(a.recoverPanic)
	r.Use(middleware.RequestLogger)

	apiRateLimiter := middleware.DefaultAPIRateLimiter()
	loginRateLimiter := middleware.StrictRateLimiter()

	// Observability endpoints (no auth required).
	r.Get("/healthz", a.healthz)
	r.Get("/readyz", a.readyz)
	r.Get("/metrics", a.metrics)

	// Public auth surface (no session required).
	r.With(loginRateLimiter.Middleware).Get("/api/login", a.login) // POST also handled below
	r.With(loginRateLimiter.Middleware).Post("/api/login", a.login)
	r.Post("/api/logout", a.logout)
	r.Get("/api/me", a.me)
	r.Get("/api/setup/status", a.setupStatus)
	r.Post("/api/setup/init", a.setupInit)
	r.Get("/api/feishu/config", a.feishuConfigPublic)
	r.Get("/api/feishu/login", a.feishuLogin)
	r.Get("/api/feishu/callback", a.feishuCallback)
	r.Get("/api/netdisk/share/public", a.netdiskSharePublic)
	r.Get("/api/netdisk/share/download", a.netdiskShareDownload)
	r.Get("/pan/{token}", a.netdiskSharePage)
	r.Handle("/api/dashboard", a.requireRole(rankUser, a.dashboard))

	// MCP Streamable HTTP endpoint (bearer-token authenticated, independent of
	// the browser session — registered outside the CSRF group on purpose).
	r.Post("/mcp/{token}", a.mcpStreamableHTTP)
	// MCP SSE transport: GET opens the event stream, POST sends JSON-RPC.
	r.Get("/mcp/{token}/sse", a.mcpSSE)
	r.Post("/mcp/{token}/messages", a.mcpMessages)

	// Activated-user business endpoints (any non-pending role).
	r.Group(func(r chi.Router) {
		r.Use(apiRateLimiter.Middleware)
		r.Use(a.authMiddleware)
		r.Use(a.activatedMiddleware)
		r.Use(middleware.CSRFProtect)

		r.Get("/api/containers", a.containers)
		r.Post("/api/containers", a.containers)
		r.Post("/api/containers/create/stream", a.createStream)
		r.Post("/api/containers/action", a.containerAction)
		r.Post("/api/containers/batch", a.containerBatch)
		r.Get("/api/containers/inspect", a.inspectContainer)
		r.Get("/api/containers/inspect/raw", a.inspectContainerRaw)
		r.Get("/api/containers/terminal", a.containerTerminal)
		r.Get("/api/containers/files/list", a.containerFilesList)
		r.Get("/api/containers/files/download", a.containerFilesDownload)
		r.Post("/api/containers/files/copy", a.containerFilesCopy)
		r.Get("/api/images", a.images)
		r.Get("/api/groups", a.groups)
		r.Post("/api/groups", a.groups)
		r.Get("/api/volumes", a.volumes)
		r.Post("/api/volumes", a.volumes)
		r.Post("/api/volumes/delete", a.volumeDelete)
		r.Post("/api/volumes/prune", a.volumePrune)
		r.Get("/api/volumes/files", a.volumeFilesList)
		r.Get("/api/volumes/files/download", a.volumeFilesDownload)
		r.Post("/api/volumes/files/mkdir", a.volumeFilesMkdir)
		r.Post("/api/volumes/files/delete", a.volumeFilesDelete)
		r.Post("/api/volumes/files/rename", a.volumeFilesRename)
		r.Post("/api/volumes/files/upload", a.volumeFilesUpload)
		r.Get("/api/networks", a.networks)
		r.Post("/api/networks", a.networks)
		r.Post("/api/networks/delete", a.networkDelete)
		r.Get("/api/networks/inspect", a.networkInspect)
		r.Post("/api/networks/connect", a.networkConnect)
		r.Post("/api/networks/disconnect", a.networkDisconnect)
		r.Get("/api/stacks", a.stacks)
		r.Post("/api/stacks", a.stacks)
		r.Get("/api/stacks/get", a.stackGet)
		r.Post("/api/stacks/delete", a.stackDelete)
		r.Post("/api/stacks/up/stream", a.stackUpStream)
		r.Post("/api/stacks/down/stream", a.stackDownStream)
		r.Get("/api/images/detailed", a.imagesDetailed)
		r.Get("/api/containers/stats/stream", a.containerStatsStream)
		r.Get("/api/containers/logs/stream", a.containerLogsStream)
		r.Post("/api/containers/update", a.containerUpdate)
		r.Post("/api/containers/duplicate", a.containerDuplicate)
		r.Get("/api/resources/history", a.resourceHistory)
		r.Get("/api/netdisk", a.netdiskList)
		r.Post("/api/netdisk/mkdir", a.netdiskMkdir)
		r.Post("/api/netdisk/delete", a.netdiskDelete)
		r.Post("/api/netdisk/rename", a.netdiskRename)
		r.Post("/api/netdisk/copy", a.netdiskCopy)
		r.Post("/api/netdisk/upload", a.netdiskUpload)
		r.Get("/api/netdisk/download", a.netdiskDownload)
		r.Get("/api/netdisk/quota", a.netdiskQuota)
		r.Get("/api/netdisk/shares", a.netdiskShares)
		r.Post("/api/netdisk/share", a.netdiskShareCreate)
		r.Post("/api/netdisk/share/delete", a.netdiskShareDelete)
		r.Post("/api/netdisk/share/save", a.netdiskShareSave)
		// Hardware monitoring is visible to all activated users (admins see host-wide
		// detail; users see the shared GPU/CPU/memory snapshot of the host they run on).
		r.Get("/api/hardware", a.hardwareSnapshot)
		r.Get("/api/hardware/gpus", a.hardwareGPUList)
		// MCP token management: list/create/delete per-container access tokens
		// for AI tools (Claude Code, Codex, Kimi). The mutating endpoints
		// additionally check canMutate() inside the handler.
		r.Get("/api/mcp/tokens", a.mcpTokenList)
		r.Post("/api/mcp/tokens", a.mcpTokenCreate)
		r.Delete("/api/mcp/tokens/{id}", a.mcpTokenDelete)

		// In-app notifications for activated users.
		r.Get("/api/notifications", a.notifications)
		r.Post("/api/notifications/read", a.notificationsRead)
	})

	// Operator+ : image group visibility (assign images to groups). Pull/build/import are
	// admin-only below; group assignment stays here so operators can still curate
	// which user groups see a given image.
	r.Group(func(r chi.Router) {
		r.Use(apiRateLimiter.Middleware)
		r.Use(a.authMiddleware)
		r.Use(a.activatedMiddleware)
		r.Use(a.minRoleMiddleware(rankOperator))
		r.Use(middleware.CSRFProtect)
		r.Post("/api/images/delete", a.deleteImage)
		r.Post("/api/images/groups", a.setImageGroups)
	})

	// Admin surface.
	r.Group(func(r chi.Router) {
		r.Use(apiRateLimiter.Middleware)
		r.Use(a.authMiddleware)
		r.Use(a.minRoleMiddleware(rankAdmin))
		r.Use(middleware.CSRFProtect)

		// Image lifecycle is admin-only: only admins pull, build, import, tag, push,
		// prune, or configure presets. Users consume the images admins publish.
		r.Post("/api/images/pull", a.pullImage)
		r.Post("/api/images/pull/stream", a.pullImageStream)
		r.Post("/api/images/register", a.imageRegister)
		r.Post("/api/images/build/stream", a.imageBuildStream)
		r.Post("/api/images/import", a.imageImport)
		r.Get("/api/images/save", a.imageSave)
		r.Post("/api/images/prune", a.imagePrune)
		r.Post("/api/images/tag", a.imageTag)
		r.Post("/api/images/push", a.imagePush)
		r.Post("/api/images/preset", a.imagePreset)
		r.Post("/api/containers/commit", a.containerCommit)

		r.Get("/api/users", a.users)
		r.Post("/api/users", a.users)
		r.Post("/api/users/update", a.userUpdate)
		r.Post("/api/users/delete", a.userDelete)
		r.Post("/api/users/groups", a.setUserGroups)
		r.Post("/api/users/approve", a.approveUser)
		r.Post("/api/users/deactivate", a.deactivateUser)
		r.Get("/api/registries", a.registries)
		r.Post("/api/registries", a.registries)
		r.Post("/api/registries/delete", a.registryDelete)
		r.Post("/api/registries/test", a.registryTest)
		r.Get("/api/admin/usage", a.usage)
		r.Get("/api/admin/audit", a.audit)
		r.Get("/api/settings/feishu", a.feishuSettings)
		r.Post("/api/settings/feishu", a.feishuSettings)
		r.Post("/api/groups/netdisk", a.groupNetdisk)
		r.Get("/api/admin/netdisk/shares", a.netdiskSharesAdmin)
		r.Post("/api/admin/netdisk/shares/delete", a.netdiskSharesDeleteAdmin)
		r.Get("/api/admin/disks", a.disks)
		r.Post("/api/admin/disks/mount", a.diskMount)
		r.Post("/api/admin/disks/unmount", a.diskUnmount)
		r.Post("/api/admin/backup", a.backupData)
		r.Get("/api/admin/processes", a.adminProcesses)
	})

	// Static UI: embedded FS in production, or disk in dev (MUDP_WEB_DIR).
	r.Handle("/*", a.staticHandler())
	return r
}

// staticHandler serves the web console from the embedded FS by default, or from
// cfg.WebDir when set (so frontend edits don't need a rebuild). Unknown paths
// fall back to index.html so deep links to SPA routes (e.g. /containers) work.
func (a *App) staticHandler() http.Handler {
	var fsys fs.FS
	if a.cfg.WebDir != "" {
		fsys = os.DirFS(a.cfg.WebDir)
	} else {
		content, err := fs.Sub(web.Files, ".")
		if err != nil {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeErr(w, http.StatusInternalServerError, "static assets unavailable")
			})
		}
		fsys = content
	}
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(fsys, path); err != nil {
			// Let the SPA router handle the client-side route.
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// authMiddleware loads the session user into the request context, or rejects.
func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := a.auth.UserID(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "login required")
			return
		}
		u, err := a.db.UserByID(uid)
		if err != nil || u.Disabled {
			writeErr(w, http.StatusUnauthorized, "login required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

// activatedMiddleware blocks pending Feishu users from business endpoints.
func (a *App) activatedMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPending(currentUser(r)) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "account pending approval", "pending": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// minRoleMiddleware enforces a minimum role rank.
func (a *App) minRoleMiddleware(minRank int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if roleRank(currentUser(r).Role) < minRank {
				writeErr(w, http.StatusForbidden, "insufficient privileges")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := a.db.Authenticate(req.Username, req.Password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	a.auth.Set(w, r, u.ID)
	csrfToken, err := middleware.CSRFToken(w, r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "csrfToken": csrfToken})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.auth.Clear(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.auth.UserID(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	u, err := a.db.UserByID(uid)
	if err != nil || u.Disabled {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	// Surface pending state so the UI can show the waiting-for-approval screen.
	type meUser struct {
		*store.User
		Pending   bool   `json:"pending"`
		CSRFToken string `json:"csrfToken,omitempty"`
	}
	writeJSON(w, http.StatusOK, meUser{User: u, Pending: isPending(u), CSRFToken: middleware.CSRFTokenFromRequest(r)})
}

// isPending reports whether a user is still awaiting admin approval: a non-admin
// whose only group membership is the holding "pending" group.
func isPending(u *store.User) bool {
	if u == nil || u.Role == "admin" {
		return false
	}
	activated := false
	for _, g := range u.Groups {
		if g != store.PendingGroup {
			activated = true
			break
		}
	}
	return !activated
}

func (a *App) users(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := a.db.Users()
		respond(w, users, err)
	case http.MethodPost:
		var req struct {
			Username          string  `json:"username"`
			Password          string  `json:"password"`
			Role              string  `json:"role"`
			GroupIDs          []int64 `json:"groupIds"`
			ContainerCap      int     `json:"containerCap"`
			NetdiskQuotaBytes int64   `json:"netdiskQuotaBytes"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Role == "" {
			req.Role = "user"
		}
		if !store.ValidRole(req.Role) {
			writeErr(w, http.StatusBadRequest, "invalid role")
			return
		}
		if req.Username == "" || req.Password == "" {
			writeErr(w, http.StatusBadRequest, "username and password are required")
			return
		}
		if err := a.db.CreateUser(req.Username, req.Password, req.Role, req.GroupIDs, req.ContainerCap, req.NetdiskQuotaBytes); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.record(r, "user.create", req.Username)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) groups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		groups, err := a.db.Groups()
		respond(w, groups, err)
	case http.MethodPost:
		if roleRank(currentUser(r).Role) < rankAdmin {
			writeErr(w, http.StatusForbidden, "only admins may create groups")
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
			writeErr(w, http.StatusBadRequest, "group name is required")
			return
		}
		a.record(r, "group.create", req.Name)
		respond(w, map[string]bool{"ok": true}, a.db.CreateGroup(strings.TrimSpace(req.Name)))
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) images(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	imgs, err := a.db.ImagesForUser(u.ID, u.Role == "admin")
	respond(w, imgs, err)
}

type pullRequest struct {
	SourceRef   string
	DisplayName string
	GroupIDs    []int64
}

func parsePullRequest(r *http.Request) (pullRequest, error) {
	var req struct {
		SourceRef   string  `json:"sourceRef"`
		DisplayName string  `json:"name"`
		GroupIDs    []int64 `json:"groupIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return pullRequest{}, err
	}
	req.DisplayName = dockerx.PublicImageName(req.DisplayName)
	if req.DisplayName == "" {
		req.DisplayName = dockerx.PublicImageName(req.SourceRef)
	}
	if req.SourceRef == "" || req.DisplayName == "" {
		return pullRequest{}, errors.New("source image is required")
	}
	return pullRequest{SourceRef: req.SourceRef, DisplayName: req.DisplayName, GroupIDs: req.GroupIDs}, nil
}

// publishPulledImage persists a freshly pulled image and applies group visibility.
func (a *App) publishPulledImage(ctx context.Context, req pullRequest, adminID int64) (string, error) {
	ref, err := a.docker.PullAndTag(ctx, req.SourceRef, req.DisplayName)
	if err != nil {
		return "", err
	}
	if err := a.db.SaveImage(req.DisplayName, ref, req.SourceRef); err != nil {
		return ref, err
	}
	if len(req.GroupIDs) > 0 {
		imgs, _ := a.db.ImagesForUser(adminID, true)
		for _, img := range imgs {
			if img.DisplayName == req.DisplayName {
				_ = a.db.SetImageGroups(img.ID, req.GroupIDs)
				break
			}
		}
	}
	return ref, nil
}

func (a *App) pullImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := parsePullRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	ref, err := a.publishPulledImage(ctx, req, currentUser(r).ID)
	if err == nil {
		a.record(r, "image.pull", req.SourceRef)
	}
	respond(w, map[string]string{"dockerRef": ref}, err)
}

// pullImageStream pulls an image while streaming registry progress over SSE.
func (a *App) pullImageStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := parsePullRequest(r)
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

	send := func(event string, payload any) {
		body, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send("progress", map[string]string{"message": "Pulling " + req.SourceRef})

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
	ref, err := a.docker.PullAndTagProgress(ctx, req.SourceRef, req.DisplayName, progress)
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	if err := a.db.SaveImage(req.DisplayName, ref, req.SourceRef); err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	if len(req.GroupIDs) > 0 {
		imgs, _ := a.db.ImagesForUser(currentUser(r).ID, true)
		for _, img := range imgs {
			if img.DisplayName == req.DisplayName {
				_ = a.db.SetImageGroups(img.ID, req.GroupIDs)
				break
			}
		}
	}
	a.record(r, "image.pull", req.SourceRef)
	send("done", map[string]string{"dockerRef": ref, "name": req.DisplayName})
}

func (a *App) setImageGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ImageID  int64   `json:"imageId"`
		GroupIDs []int64 `json:"groupIds"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ImageID == 0 {
		writeErr(w, http.StatusBadRequest, "image id is required")
		return
	}
	if err := a.db.SetImageGroups(req.ImageID, req.GroupIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "image.groups", fmt.Sprintf("image#%d", req.ImageID))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) deleteImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ImageID   int64  `json:"imageId"`
		DockerRef string `json:"dockerRef"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ImageID == 0 || strings.TrimSpace(req.DockerRef) == "" {
		writeErr(w, http.StatusBadRequest, "image id and docker ref are required")
		return
	}
	if err := a.docker.RemoveManagedImage(r.Context(), strings.TrimSpace(req.DockerRef)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "image.delete", req.DockerRef)
	respond(w, map[string]bool{"ok": true}, a.db.DeleteImage(req.ImageID))
}

func (a *App) containers(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.runtimeContainers(u.Username, u.Role == "admin"))
	case http.MethodPost:
		if !canMutate(u) {
			writeErr(w, http.StatusForbidden, "read-only role cannot create containers")
			return
		}
		var req createRequest
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		opts, err := a.validateCreate(r.Context(), u, &req)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errForbiddenImage) {
				status = http.StatusForbidden
			}
			writeErr(w, status, err.Error())
			return
		}
		id, err := a.docker.CreateContainer(r.Context(), opts)
		if err == nil {
			a.triggerRuntimeCacheRefresh()
		}
		respond(w, map[string]string{"id": id}, err)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type createRequest struct {
	Name          string   `json:"name"`
	Image         string   `json:"image"`
	Env           []string `json:"env"`
	GPUs          string   `json:"gpus"`
	Forward8080   bool     `json:"forward8080"`
	Forward80     bool     `json:"forward80"`
	PortsRaw      string   `json:"ports"`
	MountsRaw     string   `json:"mounts"`
	Networks      []string `json:"networks"`
	MountNetdisk  *bool    `json:"mountNetdisk"`
	MountShm      *bool    `json:"mountShm"`
	RestartPolicy string   `json:"restartPolicy"`
	Devices       []string `json:"devices"`
	CDIDevices    []string `json:"cdiDevices"`
	// Advanced create fields (all optional). Command/Entrypoint are space-split
	// from a single string by the frontend; the backend tolerates either form.
	Command    string            `json:"command"`
	Entrypoint string            `json:"entrypoint"`
	WorkingDir string            `json:"workingDir"`
	Hostname   string            `json:"hostname"`
	RunUser    string            `json:"runUser"`
	NanoCPUs   int64             `json:"nanoCpus"`
	MemoryMB   int64             `json:"memoryMb"`
	PidsLimit  int64             `json:"pidsLimit"`
	Sysctls    map[string]string `json:"sysctls"`
	CapAdd     []string          `json:"capAdd"`
	CapDrop    []string          `json:"capDrop"`
	Labels     map[string]string `json:"labels"`
}

var errForbiddenImage = errors.New("image not visible")

// validateCreate normalises a create request and resolves the image + scripts.
// It does not perform the docker create; callers do that themselves so the SSE
// handler can stream progress.
func (a *App) validateCreate(ctx context.Context, u *store.User, req *createRequest) (dockerx.CreateOptions, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Image = strings.TrimSpace(req.Image)
	if req.Name == "" || req.Image == "" {
		return dockerx.CreateOptions{}, errors.New("name and image are required")
	}
	existing, err := a.docker.ListContainers(ctx, u.Username, false)
	if err != nil {
		return dockerx.CreateOptions{}, err
	}
	if u.Role != "admin" && len(existing) >= u.ContainerCap {
		return dockerx.CreateOptions{}, errors.New("container limit reached")
	}
	img, err := a.db.ImageByDisplayNameForUser(req.Image, u.ID, u.Role == "admin")
	if err != nil {
		return dockerx.CreateOptions{}, errForbiddenImage
	}
	mountNetdisk := true
	if req.MountNetdisk != nil {
		mountNetdisk = *req.MountNetdisk
	}
	mountShm := true
	if req.MountShm != nil {
		mountShm = *req.MountShm
	}
	netdiskPath := ""
	if mountNetdisk {
		root, err := a.db.NetdiskPathForUser(u.ID)
		if err != nil {
			return dockerx.CreateOptions{}, err
		}
		if root != "" {
			path, err := a.ensureUserNetdisk(u, root)
			if err != nil {
				return dockerx.CreateOptions{}, err
			}
			netdiskPath = path
		}
	}
	return dockerx.CreateOptions{
		Username: u.Username, Name: req.Name, ImageRef: img.DockerRef, ImageName: img.DisplayName,
		Env: normalizeEnv(req.Env), GPUs: req.GPUs,
		Forward8080: req.Forward8080, Forward80: req.Forward80,
		Ports: splitLines(req.PortsRaw), PortPrefix: u.PortPrefix, Mounts: splitLines(req.MountsRaw),
		Networks: req.Networks, MountNetdisk: mountNetdisk, MountShm: mountShm, NetdiskPath: netdiskPath,
		Devices: req.Devices, CDIDevices: req.CDIDevices,
		RestartPolicy: req.RestartPolicy,
		// Advanced fields. Command/Entrypoint accept a shell-style string (split
		// into argv) for ergonomics in the wizard; empty means "use image default".
		Command:    shellSplit(req.Command),
		Entrypoint: shellSplit(req.Entrypoint),
		WorkingDir: req.WorkingDir, Hostname: req.Hostname, RunUser: req.RunUser,
		NanoCPUs: req.NanoCPUs, MemoryMB: req.MemoryMB, PidsLimit: req.PidsLimit,
		Sysctls: req.Sysctls, CapAdd: req.CapAdd, CapDrop: req.CapDrop, Labels: req.Labels,
	}, nil
}

// createStream runs container creation while streaming progress events over
// Server-Sent Events. On failure the partial container is removed.
func (a *App) createStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	var req createRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	opts, err := a.validateCreate(r.Context(), u, &req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errForbiddenImage) {
			status = http.StatusForbidden
		}
		writeErr(w, status, err.Error())
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

	var sendMu sync.Mutex
	send := func(event string, payload any) {
		body, _ := json.Marshal(payload)
		sendMu.Lock()
		defer sendMu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		if flusher != nil {
			flusher.Flush()
		}
	}

	progress := func(stage, msg string) {
		send("progress", map[string]any{"stage": stage, "message": msg, "ts": time.Now().Unix()})
	}
	opts.Progress = progress

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Detect client disconnect.
	notifyCtx := r.Context()
	go func() {
		<-notifyCtx.Done()
		cancel()
	}()

	// Keep the SSE connection alive through reverse proxies during long-running
	// operations (e.g., fused image builds).
	sseKeepalive(ctx, send)

	id, err := a.docker.CreateContainer(ctx, opts)
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	progress("refresh", "Refreshing container list…")
	a.refreshRuntimeCache(ctx)
	send("done", map[string]string{"id": id})
}

// sseKeepalive sends periodic ping events on an SSE stream so reverse proxies
// and browsers do not close idle long-running connections.
func sseKeepalive(ctx context.Context, send func(event string, payload any)) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				send("ping", map[string]int64{"ts": time.Now().Unix()})
			}
		}
	}()
}

func (a *App) containerAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Tail   int    `json:"tail"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id and action are required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	if req.Action == "logs" {
		tail := req.Tail
		if tail <= 0 {
			tail = 300
		}
		text, err := a.docker.Logs(r.Context(), req.ID, tail)
		respond(w, map[string]string{"logs": text}, err)
		return
	}
	// Mutating container actions (start/stop/restart/remove) require a mutating role.
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify containers")
		return
	}
	if err := a.docker.Action(r.Context(), req.ID, req.Action); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Action == "remove" {
		a.evictCachedContainers(req.ID)
		a.triggerRuntimeCacheRefresh()
	} else {
		// Synchronously refresh so the next list request reflects the new state.
		a.refreshRuntimeCache(r.Context())
	}
	a.record(r, "container."+req.Action, req.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// containerOwnedBy reports whether the given user may touch a container. Admins
// own everything; others must own the matching container.
func (a *App) containerOwnedBy(ctx context.Context, u *store.User, id string) bool {
	if u.Role == "admin" {
		return true
	}
	items, err := a.docker.ListContainers(ctx, u.Username, false)
	if err != nil {
		return false
	}
	for _, c := range items {
		if strings.HasPrefix(c.ID, id) || c.ID == id {
			return true
		}
	}
	return false
}

// ownedContainerIDs returns the set of container IDs the user owns, with one
// Docker list call. Used by batch operations to authorize many IDs at once
// instead of calling containerOwnedBy (which lists per ID) in a loop. Admins
// receive nil, meaning "all IDs are owned"; callers must then nil-check.
func (a *App) ownedContainerIDs(ctx context.Context, u *store.User) (map[string]bool, error) {
	if u.Role == "admin" {
		return nil, nil
	}
	items, err := a.docker.ListContainers(ctx, u.Username, false)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(items))
	for _, c := range items {
		set[c.ID] = true
	}
	return set, nil
}

// idOwnedBy reports whether id (or a prefix of one) is in the owned set. When
// owned is nil (admin), everything is owned.
func idOwnedBy(owned map[string]bool, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if owned == nil {
		return true
	}
	if owned[id] {
		return true
	}
	// Prefix matches (the list endpoint returns full IDs; callers may send short).
	for full := range owned {
		if strings.HasPrefix(full, id) {
			return true
		}
	}
	return false
}

// containerBatch applies a single mutating action to many containers. The
// caller must pass a mutating role. Authorization is one list call per request
// (not per ID): we resolve the owned-ID set once and only act on members.
func (a *App) containerBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify containers")
		return
	}
	var req struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.IDs) == 0 || req.Action == "" {
		writeErr(w, http.StatusBadRequest, "ids and action are required")
		return
	}
	owned, err := a.ownedContainerIDs(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type batchFailure struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	var ok, failed []string
	var failures []batchFailure
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			failures = append(failures, batchFailure{ID: id, Error: "id is required"})
			failed = append(failed, id)
			continue
		}
		if !idOwnedBy(owned, id) {
			failures = append(failures, batchFailure{ID: id, Error: "container is not yours"})
			failed = append(failed, id)
			continue
		}
		if err := a.docker.Action(r.Context(), id, req.Action); err != nil {
			failures = append(failures, batchFailure{ID: id, Error: err.Error()})
			failed = append(failed, id)
			continue
		}
		ok = append(ok, id)
	}
	if req.Action == "remove" && len(ok) > 0 {
		a.evictCachedContainers(ok...)
		a.triggerRuntimeCacheRefresh()
	} else if len(ok) > 0 {
		// Synchronously refresh so the next list request reflects the new state.
		a.refreshRuntimeCache(r.Context())
	}
	if len(ok) > 0 {
		a.record(r, "container.batch."+req.Action, fmt.Sprintf("%d container(s)", len(ok)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "failed": failed, "failures": failures})
}

// inspectContainerRaw returns Docker's full JSON inspect document for a
// container. Read-only (no mutation gate) but still ownership-guarded.
func (a *App) inspectContainerRaw(w http.ResponseWriter, r *http.Request) {
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
	raw, err := a.docker.InspectRaw(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// containerDuplicate clones an existing container's configuration into a new
// (stopped) container under the same owner. The original is left untouched.
func (a *App) containerDuplicate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot create containers")
		return
	}
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "id and name are required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	id, err := a.docker.DuplicateContainer(r.Context(), req.ID, strings.TrimSpace(req.Name), u.Username, u.PortPrefix)
	if err == nil {
		a.record(r, "container.duplicate", req.Name)
	}
	respond(w, map[string]string{"id": id}, err)
}

// containerCommit captures a container's filesystem into a new image and
// registers it in the mudp catalog. Admin-only, matching the rest of the image
// lifecycle (pull/build/import are admin-only).
func (a *App) containerCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Committing creates a new managed image (a shared, host-wide resource), so
	// it is admin-only — matching the docstring and the frontend's isAdmin() gate.
	// Without this check any mutating role could commit any mudp-managed
	// container (CommitContainer only enforces managedGuard, not ownership).
	u := currentUser(r)
	if u == nil || roleRank(u.Role) < rankAdmin {
		writeErr(w, http.StatusForbidden, "admin role required to commit containers")
		return
	}
	var req struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Tag     string `json:"tag"`
		Comment string `json:"comment"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "id and name are required")
		return
	}
	// Ownership: admins own everything, but still enforce the managed/owned guard
	// so a stray ID can't commit a non-mudp container.
	if !a.containerOwnedBy(r.Context(), u, req.ID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	name := dockerx.PublicImageName(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "invalid image name")
		return
	}
	tag := strings.TrimSpace(req.Tag)
	if tag == "" {
		tag = "latest"
	}
	ref, err := a.docker.CommitContainer(r.Context(), req.ID, name, tag, req.Comment)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.SaveImage(name, ref, "committed from "+req.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "container.commit", name)
	writeJSON(w, http.StatusOK, map[string]string{"dockerRef": ref, "name": name})
}

// feishu returns an OAuth client built from the stored admin config, or nil when
// Feishu login is disabled or unconfigured.
func (a *App) feishu() (*auth.FeishuClient, error) {
	cfg, err := a.db.FeishuConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, nil
	}
	return auth.NewFeishuClient(cfg.AppID, cfg.AppSecret), nil
}

// feishuConfigPublic tells the login page whether to show the Feishu button.
// It never reveals the secret.
func (a *App) feishuConfigPublic(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.db.FeishuConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": cfg.Enabled && cfg.AppID != "" && cfg.AppSecret != ""})
}

// feishuSettings lets an admin read/write the Feishu OAuth configuration.
func (a *App) feishuSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.db.FeishuConfig()
		respond(w, cfg, err)
	case http.MethodPost:
		var req store.FeishuConfig
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// Treat a blank app secret on save as "keep existing".
		if strings.TrimSpace(req.AppSecret) == "" {
			existing, _ := a.db.FeishuConfig()
			req.AppSecret = existing.AppSecret
		}
		respond(w, map[string]bool{"ok": true}, a.db.SaveFeishuConfig(req))
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// feishuLogin redirects the user to Feishu's consent screen.
func (a *App) feishuLogin(w http.ResponseWriter, r *http.Request) {
	fc, err := a.feishu()
	if err != nil || fc == nil {
		writeErr(w, http.StatusBadRequest, "feishu login is not configured")
		return
	}
	redirect := feishuRedirectURL(r)
	state, err := a.feishuState(w)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, fc.AuthorizeURL(redirect, state), http.StatusFound)
}

// feishuCallback handles the OAuth redirect, exchanging the code for a token and
// then either logging in an existing user or creating a pending one.
func (a *App) feishuCallback(w http.ResponseWriter, r *http.Request) {
	fc, err := a.feishu()
	if err != nil || fc == nil {
		writeErr(w, http.StatusBadRequest, "feishu login is not configured")
		return
	}
	if err := a.feishuStateVerify(r); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid state: "+err.Error())
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeErr(w, http.StatusBadRequest, "missing code")
		return
	}
	accessToken, err := fc.ExchangeCode(r.Context(), code)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	fu, err := fc.UserInfo(r.Context(), accessToken)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	u, err := a.db.UserByFeishu(fu.OpenID)
	newUser := false
	if err != nil {
		// First login: create the user in the pending group.
		u, err = a.db.CreateFeishuUser(fu.OpenID, fu.Username(), fu.Name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		newUser = true
	}
	if newUser {
		a.notifyAdminsPendingUser(u)
	}
	if u.Disabled {
		writeErr(w, http.StatusForbidden, "user is disabled")
		return
	}
	now := time.Now().Format(time.RFC3339)
	_, _ = a.db.Exec(`update users set last_login_at=? where id=?`, now, u.ID)
	a.auth.Set(w, r, u.ID)
	if _, err := middleware.CSRFToken(w, r); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func feishuRedirectURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	return fmt.Sprintf("%s://%s/api/feishu/callback", scheme, host)
}

const feishuStateCookie = "mudp_feishu_state"

func (a *App) feishuState(w http.ResponseWriter) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := hex.EncodeToString(b)
	sig := feishuStateSig(a.cfg.SessionSecret, raw)
	value := raw + "." + sig
	http.SetCookie(w, &http.Cookie{
		Name: feishuStateCookie, Value: value, Path: "/",
		MaxAge: 600, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return raw, nil
}

func (a *App) feishuStateVerify(r *http.Request) error {
	c, err := r.Cookie(feishuStateCookie)
	if err != nil {
		return err
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return errors.New("malformed state cookie")
	}
	got := r.URL.Query().Get("state")
	if got == "" || got != parts[0] {
		return errors.New("state mismatch")
	}
	if !hmac.Equal([]byte(parts[1]), []byte(feishuStateSig(a.cfg.SessionSecret, parts[0]))) {
		return errors.New("state signature invalid")
	}
	return nil
}

func feishuStateSig(secret, state string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(state))
	return hex.EncodeToString(m.Sum(nil))
}

// setUserGroups lets an admin assign a user's group membership (used to approve
// Feishu users out of the pending group).
func (a *App) setUserGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		UserID   int64   `json:"userId"`
		GroupIDs []int64 `json:"groupIds"`
	}
	if err := decodeJSON(r, &req); err != nil || req.UserID == 0 {
		writeErr(w, http.StatusBadRequest, "userId is required")
		return
	}
	before, _ := a.db.UserByID(req.UserID)
	wasPending := before != nil && isPending(before)
	if err := a.db.SetUserGroups(req.UserID, req.GroupIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	after, _ := a.db.UserByID(req.UserID)
	if after != nil {
		a.record(r, "user.groups", after.Username)
		if wasPending && !isPending(after) {
			a.notifyUserApproved(after.ID, after.Groups)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) inspectContainer(w http.ResponseWriter, r *http.Request) {
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
	info, err := a.docker.Inspect(r.Context(), id)
	respond(w, info, err)
}

// containerTerminal upgrades to a raw TCP WebSocket-like connection and bridges
// it to an interactive exec session inside the container.
func (a *App) containerTerminal(w http.ResponseWriter, r *http.Request) {
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

	hj, ok := w.(http.Hijacker)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "hijacking not supported")
		return
	}
	conn, bufRW, err := hj.Hijack()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer conn.Close()

	// Perform the WebSocket handshake manually so we don't pull in a dependency.
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		writeWSMessage(conn, wsText, []byte("missing websocket key"))
		return
	}
	if err := wsHandshake(bufRW, key); err != nil {
		// The handshake failed; tell the client instead of dropping silently.
		_, _ = conn.Write([]byte("HTTP/1.1 500 Internal Server Error\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"))
		return
	}

	cols, rows := wsSize(r.URL.Query().Get("cols"), r.URL.Query().Get("rows"))
	exec, err := a.docker.ExecAttach(r.Context(), id, rows, cols)
	if err != nil {
		writeWSMessage(conn, wsText, []byte("Failed to open terminal: "+err.Error()))
		return
	}
	defer exec.Hijacked.Close()

	pumpTerminal(r.Context(), a.docker, conn, bufRW, exec)
}

func (a *App) usage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildUsage(r, a))
}

// containerUpdate edits a managed container's restart policy and/or attached
// networks after creation. Only a mutating role may call it; the caller must
// own the container (admins own everything).
func (a *App) containerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot modify containers")
		return
	}
	var req struct {
		ID            string   `json:"id"`
		RestartPolicy *string  `json:"restartPolicy"`
		Networks      []string `json:"networks"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	if err := a.docker.UpdateContainerSettings(r.Context(), req.ID, req.RestartPolicy, req.Networks); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "container.update", req.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusGatewayTimeout
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func normalizeEnv(in []string) []string {
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" && strings.Contains(item, "=") {
			out = append(out, item)
		}
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, ",", "\n")
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// shellSplit splits a command string into argv tokens, honoring single and
// double quotes and backslash escapes. It is intentionally a small subset of
// POSIX shell rules — enough for the create wizard's command/entrypoint fields.
// Returns nil for empty input so the image default is preserved.
func shellSplit(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			// Escape next char (inside double quotes, only honored before
			// $ ` " \ — but for our purposes pass it through literally).
			cur.WriteByte(s[i+1])
			i++
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case (c == ' ' || c == '\t' || c == '\n') && !inSingle && !inDouble:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		fmt.Printf("%s %s %s\n", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func parseID(s string) int64 {
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// --- minimal WebSocket helpers (client frames only; server frames are masked-optional per RFC 6455) ---

const (
	wsContinuation = 0x0
	wsText         = 0x1
	wsBinary       = 0x2
	wsClose        = 0x8
	wsPing         = 0x9
	wsPong         = 0xA
)

var wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsHandshake completes the RFC 6455 server opening handshake on a hijacked conn.
func wsHandshake(bufRW *bufio.ReadWriter, key string) error {
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := bufRW.WriteString(resp); err != nil {
		return err
	}
	return bufRW.Flush()
}

// wsSize parses the optional cols/rows query params for the terminal pty.
func wsSize(colsStr, rowsStr string) (uint, uint) {
	cols, _ := strconv.ParseUint(colsStr, 10, 32)
	rows, _ := strconv.ParseUint(rowsStr, 10, 32)
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	return uint(cols), uint(rows)
}

// writeWSMessage writes a single (unmasked) server-to-client text/binary frame.
func writeWSMessage(conn net.Conn, opcode byte, payload []byte) error {
	var header [2]byte
	header[0] = 0x80 | opcode // FIN bit set.
	masked := false
	header[1] = byte(maskedBit(masked))
	len := len(payload)
	switch {
	case len < 126:
		header[1] |= byte(len)
		if _, err := conn.Write(header[:]); err != nil {
			return err
		}
	case len < 65536:
		header[1] |= 126
		if _, err := conn.Write(header[:]); err != nil {
			return err
		}
		var ext [2]byte
		ext[0] = byte(len >> 8)
		ext[1] = byte(len)
		if _, err := conn.Write(ext[:]); err != nil {
			return err
		}
	default:
		header[1] |= 127
		if _, err := conn.Write(header[:]); err != nil {
			return err
		}
		var ext [8]byte
		for i := 0; i < 8; i++ {
			ext[7-i] = byte(len >> (8 * uint(i)))
		}
		if _, err := conn.Write(ext[:]); err != nil {
			return err
		}
	}
	_, err := conn.Write(payload)
	return err
}

func maskedBit(masked bool) int {
	if masked {
		return 0x80
	}
	return 0
}

// readWSFrame reads one client frame (handles fragmentation + masks). Returns
// the opcode of the frame and its reassembled payload.
func readWSFrame(r io.Reader) (opcode byte, payload []byte, err error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := int(header[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(ext[0])<<8 | int(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length64 := int64(0)
		for i := 0; i < 8; i++ {
			length64 = length64<<8 | int64(ext[i])
		}
		if length64 > int64(maxWSFrameSize) {
			return 0, nil, fmt.Errorf("websocket frame too large")
		}
		length = int(length64)
	}
	if length > maxWSFrameSize {
		return 0, nil, fmt.Errorf("websocket frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

// pumpTerminal bridges a WebSocket connection to an exec hijacked session.
// Messages use a tiny JSON protocol: {type:"stdin",data} | {type:"resize",cols,rows}
// outbound, and raw exec bytes are forwarded as text frames.
func pumpTerminal(ctx context.Context, dc *dockerx.Client, conn net.Conn, bufRW *bufio.ReadWriter, exec dockerx.ExecConn) {
	const idleTimeout = 5 * time.Minute
	closeBoth := func() {
		_ = conn.Close()
		_ = exec.Hijacked.Conn.Close()
	}
	done := make(chan struct{})
	defer close(done)
	defer closeBoth()

	// exec -> websocket
	go func() {
		defer func() { _ = writeWSMessage(conn, wsClose, nil) }()
		fw := &deadlineFrameWriter{conn: conn, timeout: 30 * time.Second}
		_, _ = io.Copy(fw, exec.Hijacked.Reader)
		select {
		case <-done:
		default:
		}
	}()

	// websocket -> exec
	for {
		if err := conn.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		opcode, payload, err := readWSFrame(bufRW.Reader)
		if err != nil {
			return
		}
		switch opcode {
		case wsPing:
			_ = writeWSMessage(conn, wsPong, payload)
			continue
		case wsClose:
			return
		}
		if len(payload) == 0 {
			continue
		}
		var msg struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Cols uint   `json:"cols"`
			Rows uint   `json:"rows"`
		}
		if opcode == wsText && json.Unmarshal(payload, &msg) == nil {
			switch msg.Type {
			case "resize":
				_ = dc.ResizeExec(ctx, exec.ExecID, msg.Rows, msg.Cols)
				continue
			case "stdin":
				_ = exec.Hijacked.Conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				_, _ = exec.Hijacked.Conn.Write([]byte(msg.Data))
				continue
			}
		}
		// Fall back: treat raw bytes as stdin (works with binary terminals too).
		_ = exec.Hijacked.Conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, _ = exec.Hijacked.Conn.Write(payload)
	}
}

// deadlineFrameWriter wraps a net.Conn and sets a write deadline before each
// WebSocket frame, preventing a slow/absent client from blocking the exec pump.
type deadlineFrameWriter struct {
	conn    net.Conn
	timeout time.Duration
}

func (f *deadlineFrameWriter) Write(p []byte) (int, error) {
	if err := f.conn.SetWriteDeadline(time.Now().Add(f.timeout)); err != nil {
		return 0, err
	}
	if err := writeWSMessage(f.conn, wsText, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// frameWriter adapts raw exec output into WebSocket text frames.
type frameWriter struct {
	conn net.Conn
}

func (f frameWriter) Write(p []byte) (int, error) {
	if err := writeWSMessage(f.conn, wsText, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
