package server

import (
	"archive/zip"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mudp/internal/store"
)

type fileItem struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func (a *App) userNetdiskRoot(u *store.User) (string, error) {
	root, err := a.db.NetdiskPathForUser(u.ID)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", fmt.Errorf("netdisk path is not configured for your group")
	}
	return a.ensureUserNetdisk(u, root)
}

func (a *App) ensureUserNetdisk(u *store.User, root string) (string, error) {
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", sanitizePathPart(u.Username), u.ID))
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	return dir, nil
}

func sanitizePathPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "user"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
}

func cleanUserPath(root, rel string) (string, string, error) {
	rel = strings.TrimPrefix(filepath.Clean("/"+rel), string(filepath.Separator))
	full := filepath.Join(root, rel)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(filepath.Separator)) {
		return "", "", fmt.Errorf("invalid path")
	}
	return fullAbs, rel, nil
}

func (a *App) netdiskList(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, rel, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items := make([]fileItem, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		p := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		if rel == "." {
			p = entry.Name()
		}
		items = append(items, fileItem{Name: entry.Name(), Path: p, Dir: entry.IsDir(), Size: info.Size(), ModTime: info.ModTime().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.ToSlash(rel), "items": items})
}

func (a *App) netdiskMkdir(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
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

func (a *App) netdiskDelete(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
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

func (a *App) netdiskRename(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
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

func (a *App) netdiskUpload(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.ParseMultipartForm(2 << 30); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, err := cleanUserPath(root, r.FormValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	files := r.MultipartForm.File["files"]
	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dstPath, _, err := cleanUserPath(dir, filepath.Base(fh.Filename))
		if err != nil {
			_ = src.Close()
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		offset := int64(0)
		if existing, err := os.Stat(dstPath); err == nil && existing.Size() < fh.Size {
			offset = existing.Size()
		}
		if seeker, ok := src.(io.Seeker); ok && offset > 0 {
			_, _ = seeker.Seek(offset, io.SeekStart)
		} else {
			offset = 0
		}
		dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY, 0640)
		if err != nil {
			_ = src.Close()
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		_, _ = dst.Seek(offset, io.SeekStart)
		_, err = io.Copy(dst, src)
		_ = dst.Close()
		_ = src.Close()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(files)})
}

func (a *App) netdiskDownload(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
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

func (a *App) netdiskQuota(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	used := dirSize(root)
	q := map[string]any{"usedBytes": used}
	if runtime.GOOS == "windows" {
		q["note"] = "free space is available in admin disk view on Windows"
	}
	writeJSON(w, http.StatusOK, q)
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (a *App) netdiskShareCreate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Paths     []string `json:"paths"`
		Name      string   `json:"name"`
		Permanent bool     `json:"permanent"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "paths are required")
		return
	}
	var clean []string
	for _, p := range req.Paths {
		full, rel, err := cleanUserPath(root, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := os.Stat(full); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		clean = append(clean, filepath.ToSlash(rel))
	}
	token, err := a.uniqueShareToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(clean[0])
	}
	expiresAt := ""
	if !req.Permanent {
		expiresAt = time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	}
	if err := a.db.CreateNetdiskShare(u.ID, token, name, clean, expiresAt, req.Permanent); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "url": "/pan/" + token})
}

func (a *App) netdiskShares(w http.ResponseWriter, r *http.Request) {
	items, err := a.db.NetdiskShares(currentUser(r).ID)
	respond(w, items, err)
}

func (a *App) netdiskShareDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token  string   `json:"token"`
		Tokens []string `json:"tokens"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Token != "" {
		req.Tokens = append(req.Tokens, req.Token)
	}
	if len(req.Tokens) == 0 {
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	}
	respond(w, map[string]bool{"ok": true}, a.db.DeleteNetdiskShares(req.Tokens, currentUser(r).ID, false))
}

func (a *App) netdiskSharePublic(w http.ResponseWriter, r *http.Request) {
	share, ownerRoot, err := a.resolveShare(r.URL.Query().Get("token"))
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	var items []fileItem
	for _, p := range share.Paths {
		full, rel, err := cleanUserPath(ownerRoot, p)
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		items = append(items, fileItem{Name: info.Name(), Path: filepath.ToSlash(rel), Dir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": share, "items": items, "readOnly": true})
}

func (a *App) netdiskSharePage(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if share, _, err := a.resolveShare(token); err != nil {
		expired := err == errShareExpired
		renderShareUnavailable(w, expired)
		return
	} else if share.Token == "" {
		renderShareUnavailable(w, false)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>MUDP Netdisk Share</title><style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;margin:0;background:#f4f5fa;color:#1f2330}
.wrap{max-width:920px;margin:32px auto;padding:0 18px}.card{background:#fff;border:1px solid #e6e8f0;border-radius:8px;box-shadow:0 1px 3px rgba(16,24,40,.06)}
.head{display:flex;justify-content:space-between;gap:12px;align-items:center;padding:16px;border-bottom:1px solid #e6e8f0}.body{padding:16px}
button,a.btn{display:inline-flex;align-items:center;min-height:32px;padding:0 12px;border-radius:7px;border:1px solid #e6e8f0;background:#fff;color:#1f2330;text-decoration:none;cursor:pointer}
button.primary{background:#3370ff;color:#fff;border-color:#3370ff}table{width:100%%;border-collapse:collapse}td,th{padding:10px;border-bottom:1px solid #eef0f6;text-align:left}.hint{color:#8a90a0}
</style></head><body><div class="wrap"><div class="card"><div class="head"><h2 id="title">Netdisk Share</h2><button class="primary" id="saveAll">Save to my netdisk</button></div><div class="body" id="body">Loading...</div></div></div>
<script>
const token=%q;
const ts=()=>Date.now();
function esc(s){return String(s||"").replace(/[&<>"']/g,m=>({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[m]))}
async function api(p,o){const r=await fetch(p,Object.assign({credentials:"same-origin",headers:{"Content-Type":"application/json"}},o||{}));const d=await r.json().catch(()=>({}));if(!r.ok)throw new Error(d.error||r.statusText);return d}
async function load(){try{const d=await api("/api/netdisk/share/public?token="+encodeURIComponent(token));document.getElementById("title").textContent=d.share.name+" (read only)";document.getElementById("body").innerHTML="<table><thead><tr><th>Name</th><th>Type</th><th>Size</th><th></th></tr></thead><tbody>"+d.items.map(f=>'<tr><td>'+esc(f.name)+'</td><td>'+(f.dir?'Folder':'File')+'</td><td>'+(f.dir?'-':f.size)+'</td><td><a class=\"btn\" href=\"/api/netdisk/share/download?token='+encodeURIComponent(token)+'&path='+encodeURIComponent(f.path)+'&ts='+ts()+'\">Download</a></td></tr>').join("")+"</tbody></table>";}catch(e){document.getElementById("body").innerHTML='<p class="hint">'+esc(e.message)+'</p>'}}
document.getElementById("saveAll").onclick=async()=>{try{const d=await api("/api/netdisk/share/save",{method:"POST",body:JSON.stringify({token})});alert("Saved "+d.count+" item(s)");}catch(e){alert(e.message+"。请先登录 MUDP 后再保存。")}}
load();
</script></body></html>`, token)
}

func (a *App) netdiskShareDownload(w http.ResponseWriter, r *http.Request) {
	share, ownerRoot, err := a.resolveShare(r.URL.Query().Get("token"))
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" && len(share.Paths) == 1 {
		reqPath = share.Paths[0]
	}
	if !shareContains(share.Paths, reqPath) {
		writeErr(w, http.StatusForbidden, "path is not in this share")
		return
	}
	full, _, err := cleanUserPath(ownerRoot, reqPath)
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
		serveZipDownload(w, full, info.Name()+".zip")
		return
	}
	serveFileDownload(w, r, full, info.Name())
}

func (a *App) netdiskShareSave(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	dstRoot, err := a.userNetdiskRoot(u)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Token string   `json:"token"`
		Paths []string `json:"paths"`
		To    string   `json:"to"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Token == "" {
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	}
	share, ownerRoot, err := a.resolveShare(req.Token)
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	if len(req.Paths) == 0 {
		req.Paths = share.Paths
	}
	dstDir, _, err := cleanUserPath(dstRoot, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(dstDir, 0750); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	count := 0
	for _, p := range req.Paths {
		if !shareContains(share.Paths, p) {
			continue
		}
		src, _, err := cleanUserPath(ownerRoot, p)
		if err != nil {
			continue
		}
		name := filepath.Base(src)
		if err := copyPath(src, filepath.Join(dstDir, name)); err == nil {
			count++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count})
}

func (a *App) resolveShare(token string) (store.NetdiskShare, string, error) {
	share, err := a.db.NetdiskShare(strings.TrimSpace(token))
	if err != nil {
		return share, "", err
	}
	if share.Expired {
		return share, "", errShareExpired
	}
	owner, err := a.db.UserByID(share.OwnerID)
	if err != nil {
		return share, "", err
	}
	root, err := a.userNetdiskRoot(owner)
	return share, root, err
}

var errShareExpired = fmt.Errorf("external link expired")

func (a *App) netdiskSharesAdmin(w http.ResponseWriter, r *http.Request) {
	items, err := a.db.AllNetdiskShares()
	respond(w, items, err)
}

func (a *App) netdiskSharesDeleteAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tokens []string `json:"tokens"`
		Token  string   `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Token != "" {
		req.Tokens = append(req.Tokens, req.Token)
	}
	if len(req.Tokens) == 0 {
		writeErr(w, http.StatusBadRequest, "tokens are required")
		return
	}
	respond(w, map[string]bool{"ok": true}, a.db.DeleteNetdiskShares(req.Tokens, 0, true))
}

func serveFileDownload(w http.ResponseWriter, r *http.Request, full, name string) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	http.ServeFile(w, r, full)
}

func serveZipDownload(w http.ResponseWriter, root, name string) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	zw := zip.NewWriter(w)
	defer zw.Close()
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fw, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, _ = io.Copy(fw, f)
		return nil
	})
}

func contentDisposition(name string) string {
	name = strings.ReplaceAll(filepath.Base(name), `"`, "")
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, asciiFilename(name), urlPathEscape(name))
}

func asciiFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 32 && r <= 126 && r != '"' && r != '\\' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "download"
	}
	return out
}

func urlPathEscape(s string) string {
	return url.PathEscape(s)
}

func (a *App) uniqueShareToken() (string, error) {
	for i := 0; i < 8; i++ {
		token, err := randomToken()
		if err != nil {
			return "", err
		}
		if _, err := a.db.NetdiskShare(token); err != nil {
			return token, nil
		}
	}
	return "", fmt.Errorf("could not allocate short link")
}

func randomToken() (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

func renderShareUnavailable(w http.ResponseWriter, expired bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	title := "Link unavailable"
	msg := "This external link does not exist or has been removed."
	if expired {
		title = "Link expired"
		msg = "This external link has expired. Please ask the owner to create a new one."
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;margin:0;background:#f4f5fa;color:#1f2330;display:grid;place-items:center;min-height:100vh}.card{width:min(420px,calc(100vw - 32px));background:#fff;border:1px solid #e6e8f0;border-radius:8px;padding:24px;box-shadow:0 1px 3px rgba(16,24,40,.06)}h1{font-size:22px;margin:0 0 8px}.hint{color:#8a90a0;line-height:1.7}</style></head><body><section class="card"><h1>%s</h1><p class="hint">%s</p></section></body></html>`, title, title, msg)
}

func shareContains(paths []string, req string) bool {
	req = filepath.ToSlash(strings.TrimPrefix(filepath.Clean("/"+req), "/"))
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimPrefix(filepath.Clean("/"+p), "/"))
		if req == p || strings.HasPrefix(req, p+"/") {
			return true
		}
	}
	return false
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, _ := filepath.Rel(src, path)
			target := filepath.Join(dst, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0750)
			}
			return copyFile(path, target)
		})
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
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
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
