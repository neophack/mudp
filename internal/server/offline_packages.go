package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mudp/internal/bootstrap"
	"mudp/internal/store"
)

const maxOfflinePackageBytes int64 = 4 << 30

func (a *App) offlinePackageRoot() (string, error) {
	root := strings.TrimSpace(a.cfg.OfflinePackageDir)
	if root == "" {
		root = "offline-packages"
	}
	if !filepath.IsAbs(root) {
		root = filepath.Clean(root)
	}
	if err := os.MkdirAll(root, 0750); err != nil {
		return "", err
	}
	return root, nil
}

func (a *App) offlinePackages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := a.db.OfflinePackages()
	respond(w, items, err)
}

func (a *App) offlinePackageUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	root, err := a.offlinePackageRoot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOfflinePackageBytes)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	service := normalizeOfflineService(r.FormValue("service"))
	if service == "" {
		writeErr(w, http.StatusBadRequest, "service must be all, ssh, or vscode")
		return
	}
	imageName := strings.TrimSpace(r.FormValue("imageName"))
	imageRef := strings.TrimSpace(r.FormValue("imageRef"))
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSpace(header.Filename)
	}
	if name == "" {
		name = "offline-package"
	}

	filename := uniqueOfflineFilename(root, header.Filename)
	dstPath := filepath.Join(root, filename)
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hash := sha256.New()
	size, copyErr := io.Copy(dst, io.TeeReader(file, hash))
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		writeErr(w, http.StatusBadRequest, copyErr.Error())
		return
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		writeErr(w, http.StatusInternalServerError, closeErr.Error())
		return
	}

	p := store.OfflinePackage{
		Name:        name,
		Filename:    filename,
		Service:     service,
		ImageName:   imageName,
		ImageRef:    imageRef,
		OS:          strings.TrimSpace(r.FormValue("os")),
		Arch:        strings.TrimSpace(r.FormValue("arch")),
		Size:        size,
		SHA256:      hex.EncodeToString(hash.Sum(nil)),
		Description: strings.TrimSpace(r.FormValue("description")),
	}
	id, err := a.db.SaveOfflinePackage(p)
	if err != nil {
		_ = os.Remove(dstPath)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "offline-package.upload", name)
	p.ID = id
	writeJSON(w, http.StatusOK, p)
}

func (a *App) offlinePackageDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	p, err := a.db.OfflinePackage(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "offline package not found")
		return
	}
	root, err := a.offlinePackageRoot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	serveFileDownload(w, r, filepath.Join(root, p.Filename), p.Filename)
}

func (a *App) offlinePackageDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID <= 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	p, err := a.db.DeleteOfflinePackage(req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "offline package not found")
		return
	}
	if root, err := a.offlinePackageRoot(); err == nil {
		_ = os.Remove(filepath.Join(root, p.Filename))
	}
	a.record(r, "offline-package.delete", p.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) offlineBuildScript(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.saveOfflineBuildScript(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition("build-mudp-offline-package.sh"))
	// Return the admin-customized script if one is saved, otherwise the default.
	cfg, err := a.db.ScriptSettings()
	if err == nil && strings.TrimSpace(cfg.BuildScript) != "" {
		_, _ = w.Write([]byte(cfg.BuildScript))
	} else {
		_, _ = w.Write([]byte(buildOfflinePackageScript()))
	}
}

func (a *App) saveOfflineBuildScript(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BuildScript string `json:"buildScript"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := a.db.ScriptSettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg.BuildScript = req.BuildScript
	if err := a.db.SaveScriptSettings(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "offline.build-script.save", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// offlinePkgBuildStream runs the offline package build script on the server
// (requires Docker + internet on the server side) and streams progress via SSE.
// On success the resulting .run file is saved and registered as an offline package.
// POST /api/scripts/offline/build/stream  body: { service, os, arch, imageName, imageRef }
func (a *App) offlinePkgBuildStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Service   string `json:"service"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
		ImageName string `json:"imageName"`
		ImageRef  string `json:"imageRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Service = normalizeOfflineService(req.Service)
	if req.Service == "" {
		writeErr(w, http.StatusBadRequest, "service must be all, ssh, or vscode")
		return
	}
	req.OS = sanitizePathPart(strings.ToLower(strings.TrimSpace(req.OS)))
	if req.OS == "" {
		req.OS = "ubuntu"
	}
	req.Arch = sanitizePathPart(strings.ToLower(strings.TrimSpace(req.Arch)))
	if req.Arch == "" {
		req.Arch = "amd64"
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

	// Load the build script (custom or default).
	scripts, _ := a.db.ScriptSettings()
	scriptContent := strings.TrimSpace(scripts.BuildScript)
	if scriptContent == "" {
		scriptContent = buildOfflinePackageScript()
	}

	send("progress", map[string]string{"message": fmt.Sprintf("Building %s offline package for %s/%s…", req.Service, req.OS, req.Arch)})

	// Write script to a temp file.
	sf, err := os.CreateTemp("", "mudp-build-*.sh")
	if err != nil {
		send("error", map[string]string{"message": "create temp script: " + err.Error()})
		return
	}
	defer os.Remove(sf.Name())
	if _, err := sf.WriteString(scriptContent); err != nil {
		sf.Close()
		send("error", map[string]string{"message": "write temp script: " + err.Error()})
		return
	}
	sf.Close()
	if err := os.Chmod(sf.Name(), 0755); err != nil {
		send("error", map[string]string{"message": "chmod temp script: " + err.Error()})
		return
	}

	// Temp output directory.
	outDir, err := os.MkdirTemp("", "mudp-offline-pkg-*")
	if err != nil {
		send("error", map[string]string{"message": "create temp dir: " + err.Error()})
		return
	}
	defer os.RemoveAll(outDir)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	go func() { <-r.Context().Done(); cancel() }()
	sseKeepalive(ctx, send)

	cmd := exec.CommandContext(ctx, "sh", sf.Name(), req.Service, req.OS, req.Arch, "run", outDir)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		send("error", map[string]string{"message": "start build: " + err.Error()})
		return
	}
	// Stream each line of script output to the client.
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			send("progress", map[string]string{"message": sc.Text()})
		}
	}()
	cmdErr := cmd.Wait()
	pw.Close()
	<-done

	if cmdErr != nil {
		send("error", map[string]string{"message": "build script failed: " + cmdErr.Error()})
		return
	}

	// Find the generated .run (or .tar.gz) file.
	entries, err := os.ReadDir(outDir)
	if err != nil {
		send("error", map[string]string{"message": "read output dir: " + err.Error()})
		return
	}
	var outFile string
	for _, e := range entries {
		if !e.IsDir() {
			outFile = filepath.Join(outDir, e.Name())
			break
		}
	}
	if outFile == "" {
		send("error", map[string]string{"message": "build script produced no output file"})
		return
	}

	// Copy into the offline-packages dir and register.
	root, err := a.offlinePackageRoot()
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	filename := uniqueOfflineFilename(root, filepath.Base(outFile))
	dstPath := filepath.Join(root, filename)

	src, err := os.Open(outFile)
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		src.Close()
		send("error", map[string]string{"message": err.Error()})
		return
	}
	hash := sha256.New()
	size, copyErr := io.Copy(dst, io.TeeReader(src, hash))
	src.Close()
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(dstPath)
		msg := "save package file"
		if copyErr != nil {
			msg += ": " + copyErr.Error()
		}
		send("error", map[string]string{"message": msg})
		return
	}

	pkgName := fmt.Sprintf("mudp-offline-%s-%s-%s", req.Service, req.OS, req.Arch)
	p := store.OfflinePackage{
		Name:      pkgName,
		Filename:  filename,
		Service:   req.Service,
		ImageName: strings.TrimSpace(req.ImageName),
		ImageRef:  strings.TrimSpace(req.ImageRef),
		OS:        req.OS,
		Arch:      req.Arch,
		Size:      size,
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
	}
	id, err := a.db.SaveOfflinePackage(p)
	if err != nil {
		os.Remove(dstPath)
		send("error", map[string]string{"message": "save to database: " + err.Error()})
		return
	}
	a.record(r, "offline-package.build", pkgName)

	send("done", map[string]string{"message": "Package saved: " + pkgName, "id": fmt.Sprintf("%d", id), "name": pkgName})
}

func (a *App) offlineBootstrapPackages(imageName, imageRef string, ssh, vscode bool) []bootstrap.OfflinePackage {
	services := []string{}
	if ssh {
		services = append(services, "ssh")
	}
	if vscode {
		services = append(services, "vscode")
	}
	if len(services) == 0 {
		return nil
	}
	root, err := a.offlinePackageRoot()
	if err != nil {
		return nil
	}
	seen := map[int64]bool{}
	var out []bootstrap.OfflinePackage
	for _, service := range services {
		items, err := a.db.OfflinePackagesForImage(imageName, imageRef, service)
		if err != nil {
			continue
		}
		for _, item := range items {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			body, err := os.ReadFile(filepath.Join(root, item.Filename))
			if err != nil {
				continue
			}
			mode := int64(0644)
			if strings.HasSuffix(item.Filename, ".sh") || strings.HasSuffix(item.Filename, ".run") {
				mode = 0755
			}
			out = append(out, bootstrap.OfflinePackage{Name: item.Filename, Body: body, Mode: mode})
		}
	}
	return out
}

func normalizeOfflineService(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return "all"
	case "ssh":
		return "ssh"
	case "vscode", "vs code", "code":
		return "vscode"
	default:
		return ""
	}
}

func uniqueOfflineFilename(root, original string) string {
	ext := filepath.Ext(original)
	base := strings.TrimSuffix(filepath.Base(original), ext)
	base = sanitizePathPart(base)
	if base == "" || base == "." {
		base = "package"
	}
	if ext == "" {
		ext = ".pkg"
	}
	stamp := time.Now().UTC().Format("20060102T150405")
	name := fmt.Sprintf("%s-%s%s", base, stamp, ext)
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(root, name)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%s-%d%s", base, stamp, i, ext)
	}
}

func buildOfflinePackageScript() string {
	return `#!/bin/sh
# MUDP Offline Bootstrap Package Builder
# ========================================
# Run this script on a machine WITH internet access to create an offline
# bootstrap package. Upload the resulting archive to:
#   MUDP → Bootstrap → Offline Packages
#
# Usage:
#   ./build-mudp-offline-package.sh [service] [os] [arch] [format] [output_dir]
#
#   service:    ssh | vscode | all       (default: all)
#   os:         ubuntu | debian | alpine | centos | rhel | fedora | openeuler
#               (default: ubuntu)
#   arch:       amd64 | arm64 | armv7   (default: current machine arch)
#   format:     tar.gz | run            (default: tar.gz)
#               run = self-extracting shell script (single file, no tar needed)
#   output_dir: directory to write package to  (default: current dir)
#
# Requirements: curl is required; Docker is optional but recommended for
#   downloading target-OS packages without needing the target OS installed.
#
# Examples:
#   # SSH package for Ubuntu amd64 as self-extracting .run
#   ./build-mudp-offline-package.sh ssh ubuntu amd64 run ./packages
#
#   # VS Code package for Ubuntu arm64
#   ./build-mudp-offline-package.sh vscode ubuntu arm64 tar.gz ./packages
#
#   # All-in-one package for Alpine
#   ./build-mudp-offline-package.sh all alpine amd64 run ./packages

set -eu

SERVICE="${1:-all}"
OS_TYPE="${2:-ubuntu}"
ARCH="${3:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/;s/armv7l/armv7/')}"
FORMAT="${4:-tar.gz}"
OUT_DIR="${5:-.}"

have_cmd() { command -v "$1" >/dev/null 2>&1; }

NAME="mudp-offline-${SERVICE}-${OS_TYPE}-${ARCH}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PKG="$WORK/pkg"
mkdir -p "$OUT_DIR" "$PKG/deb" "$PKG/rpm" "$PKG/apk"

echo "=== MUDP Offline Package Builder ==="
echo "Service: $SERVICE | OS: $OS_TYPE | Arch: $ARCH | Format: $FORMAT"
echo ""

# ---- Helper: Docker platform string ----
docker_platform() {
  case "$ARCH" in
    amd64)  echo "linux/amd64" ;;
    arm64)  echo "linux/arm64" ;;
    armv7)  echo "linux/arm/v7" ;;
    *)      echo "linux/$ARCH" ;;
  esac
}

# ---- Download SSH packages ----
download_ssh() {
  echo "[ssh] Downloading SSH packages for ${OS_TYPE}/${ARCH}..."
  case "$OS_TYPE" in
    debian|ubuntu)
      if have_cmd docker; then
        IMG="ubuntu:22.04"; [ "$OS_TYPE" = "debian" ] && IMG="debian:12"
        docker run --rm --platform="$(docker_platform)" \
          -v "$PKG/deb:/out" "$IMG" sh -c \
          'cd /out && apt-get update -qq 2>/dev/null && apt-get download openssh-server openssh-sftp-server 2>/dev/null || true' \
          2>/dev/null || true
        COUNT="$(ls "$PKG/deb/"*.deb 2>/dev/null | wc -l | tr -d ' ')"
        [ "$COUNT" -gt 0 ] && echo "[ssh] Downloaded $COUNT .deb packages" || echo "[ssh] Warning: no .deb packages downloaded"
      else
        echo "[ssh] Docker not available. Run on a ${OS_TYPE} host: apt-get download openssh-server"
        echo "[ssh]   Then copy *.deb into the deb/ folder next to install.sh"
      fi
      ;;
    alpine)
      if have_cmd docker; then
        PLAT="$(docker_platform)"
        docker run --rm --platform="$PLAT" \
          -v "$PKG/apk:/out" alpine:3 sh -c \
          'apk fetch -o /out openssh 2>/dev/null || true' 2>/dev/null || true
        COUNT="$(ls "$PKG/apk/"*.apk 2>/dev/null | wc -l | tr -d ' ')"
        [ "$COUNT" -gt 0 ] && echo "[ssh] Downloaded $COUNT Alpine packages" || echo "[ssh] Warning: no .apk packages downloaded"
      else
        echo "[ssh] Docker not available. Run on Alpine: apk fetch openssh"
        echo "[ssh]   Then copy *.apk into the apk/ folder"
      fi
      ;;
    centos|rhel|openeuler)
      if have_cmd docker; then
        IMG="rockylinux:9"; [ "$OS_TYPE" = "openeuler" ] && IMG="openeuler/openeuler:22.03"
        docker run --rm --platform="$(docker_platform)" \
          -v "$PKG/rpm:/out" "$IMG" sh -c \
          'dnf download --destdir=/out --resolve openssh-server openssh-clients 2>/dev/null || \
           yum install --downloadonly --downloaddir=/out openssh-server openssh-clients 2>/dev/null || true' \
          2>/dev/null || true
        COUNT="$(ls "$PKG/rpm/"*.rpm 2>/dev/null | wc -l | tr -d ' ')"
        [ "$COUNT" -gt 0 ] && echo "[ssh] Downloaded $COUNT RPM packages" || echo "[ssh] Warning: no .rpm packages downloaded"
      else
        echo "[ssh] Docker not available. Run on a ${OS_TYPE} host:"
        echo "[ssh]   dnf download --resolve openssh-server openssh-clients"
        echo "[ssh]   Then copy *.rpm into the rpm/ folder"
      fi
      ;;
    fedora)
      if have_cmd docker; then
        docker run --rm --platform="$(docker_platform)" \
          -v "$PKG/rpm:/out" fedora:latest sh -c \
          'dnf download --destdir=/out --resolve openssh-server 2>/dev/null || true' \
          2>/dev/null || true
      fi
      ;;
  esac
}

# ---- Download code-server ----
download_vscode() {
  echo "[vscode] Fetching latest code-server version..."
  CS_VERSION=""
  if have_cmd curl; then
    CS_VERSION="$(curl -fsSL --max-time 10 \
      'https://api.github.com/repos/coder/code-server/releases/latest' 2>/dev/null \
      | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/' | head -1)"
  fi
  [ -z "$CS_VERSION" ] && CS_VERSION="4.96.4"
  echo "[vscode] code-server version: $CS_VERSION"

  case "$OS_TYPE" in
    debian|ubuntu)
      CS_ARCH="$ARCH"; [ "$ARCH" = "armv7" ] && CS_ARCH="armv7l"
      URL="https://github.com/coder/code-server/releases/download/v${CS_VERSION}/code-server_${CS_VERSION}_${CS_ARCH}.deb"
      echo "[vscode] Downloading $URL"
      have_cmd curl && curl -fL --max-time 300 "$URL" -o "$PKG/deb/code-server.deb" 2>/dev/null || \
        echo "[vscode] Warning: could not download code-server .deb — add it manually"
      ;;
    alpine)
      CS_ARCH="$ARCH"; [ "$ARCH" = "armv7" ] && CS_ARCH="armv7l"
      URL="https://github.com/coder/code-server/releases/download/v${CS_VERSION}/code-server-${CS_VERSION}-linux-${CS_ARCH}.tar.gz"
      echo "[vscode] Downloading $URL"
      have_cmd curl && curl -fL --max-time 300 "$URL" -o "$PKG/code-server.tar.gz" 2>/dev/null || \
        echo "[vscode] Warning: could not download code-server tar.gz — add it manually"
      ;;
    centos|rhel|fedora|openeuler)
      CS_ARCH="$ARCH"; [ "$ARCH" = "armv7" ] && CS_ARCH="armv7l"
      URL="https://github.com/coder/code-server/releases/download/v${CS_VERSION}/code-server-${CS_VERSION}-${CS_ARCH}.rpm"
      echo "[vscode] Downloading $URL"
      have_cmd curl && curl -fL --max-time 300 "$URL" -o "$PKG/rpm/code-server.rpm" 2>/dev/null || \
        echo "[vscode] Warning: could not download code-server RPM — add it manually"
      ;;
  esac
}

# ---- Generate install.sh ----
generate_install_sh() {
  cat > "$PKG/install.sh" <<'INSTALL_EOF'
#!/bin/sh
set -eu
service="${1:-all}"
here="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

have_cmd() { command -v "$1" >/dev/null 2>&1; }

install_deb() {
  ls "$here"/deb/*.deb >/dev/null 2>&1 || return 1
  if have_cmd dpkg; then
    dpkg -i "$here"/deb/*.deb 2>/dev/null || true
    have_cmd apt-get && apt-get install -f -y 2>/dev/null || true
    return 0
  fi
  return 1
}

install_rpm() {
  ls "$here"/rpm/*.rpm >/dev/null 2>&1 || return 1
  if have_cmd dnf;  then dnf  install -y "$here"/rpm/*.rpm 2>/dev/null && return 0; fi
  if have_cmd yum;  then yum  install -y "$here"/rpm/*.rpm 2>/dev/null && return 0; fi
  if have_cmd rpm;  then rpm  -Uvh --replacepkgs "$here"/rpm/*.rpm 2>/dev/null && return 0; fi
  return 1
}

install_apk() {
  ls "$here"/apk/*.apk >/dev/null 2>&1 || return 1
  have_cmd apk && apk add --allow-untrusted "$here"/apk/*.apk 2>/dev/null && return 0
  return 1
}

install_code_server() {
  have_cmd code-server && return 0
  # pre-downloaded .deb / .rpm
  install_deb || install_rpm || true
  have_cmd code-server && return 0
  # pre-downloaded tarball
  if [ -f "$here/code-server.tar.gz" ]; then
    tar -C /usr/local -xzf "$here/code-server.tar.gz" 2>/dev/null || true
    for bin in /usr/local/*/bin/code-server /usr/local/bin/code-server; do
      [ -x "$bin" ] && ln -sf "$bin" /usr/local/bin/code-server && return 0
    done
  fi
  return 1
}

case "$service" in
  ssh)
    install_deb || install_rpm || install_apk || true
    ;;
  vscode)
    install_code_server || install_deb || install_rpm || install_apk || true
    ;;
  *)
    install_deb || install_rpm || install_apk || true
    install_code_server || true
    ;;
esac
INSTALL_EOF
  chmod +x "$PKG/install.sh"

  cat > "$PKG/README.txt" <<EOF
MUDP Offline Bootstrap Package
================================
Service  : $SERVICE
OS       : $OS_TYPE
Arch     : $ARCH
Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)

Add offline dependencies beside install.sh before packaging:
  deb/*.deb              → Debian/Ubuntu packages
  rpm/*.rpm              → RHEL/CentOS/Fedora/openEuler packages
  apk/*.apk              → Alpine packages
  code-server.tar.gz     → VS Code Server tarball (non-deb/rpm)
  code-server-install.sh → Custom VS Code installer script

The install.sh will be called with the service name (ssh/vscode/all)
during container startup. Return 0 on success.
EOF
}

# ---- Package as .run self-extracting script ----
package_run() {
  PAYLOAD="$WORK/payload.tar.gz"
  tar -C "$PKG" -czf "$PAYLOAD" .
  OUT_FILE="$OUT_DIR/${NAME}.run"
  cat > "$OUT_FILE" <<'RUN_HEADER'
#!/bin/sh
# MUDP offline bootstrap package (self-extracting)
set -eu
SKIP=$(awk '/^__ARCHIVE_BELOW__/{print NR+1; exit 0}' "$0")
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
tail -n +"$SKIP" "$0" | base64 -d | tar -C "$tmpdir" -xz
chmod +x "$tmpdir/install.sh" 2>/dev/null || true
"$tmpdir/install.sh" "${1:-all}"
exit 0
__ARCHIVE_BELOW__
RUN_HEADER
  base64 "$PAYLOAD" >> "$OUT_FILE"
  chmod +x "$OUT_FILE"
  echo ""
  echo "Created: $OUT_FILE"
  echo "Upload this file to: MUDP → Bootstrap → Offline Packages"
  echo "  Service: $SERVICE | OS: $OS_TYPE | Arch: $ARCH | Format: .run"
}

# ---- Package as .tar.gz ----
package_targz() {
  OUT_FILE="$OUT_DIR/${NAME}.tar.gz"
  tar -C "$PKG" -czf "$OUT_FILE" .
  echo ""
  echo "Created: $OUT_FILE"
  echo "Upload this file to: MUDP → Bootstrap → Offline Packages"
  echo "  Service: $SERVICE | OS: $OS_TYPE | Arch: $ARCH | Format: .tar.gz"
}

# ---- Main ----
case "$SERVICE" in
  ssh)    download_ssh ;;
  vscode) download_vscode ;;
  all)    download_ssh; download_vscode ;;
  *)      echo "Unknown service: $SERVICE"; exit 1 ;;
esac

generate_install_sh

echo ""
echo "[package] Assembling ${FORMAT} package..."
case "$FORMAT" in
  run)    package_run ;;
  tar.gz) package_targz ;;
  *)      echo "Unknown format: $FORMAT"; exit 1 ;;
esac
`
}
