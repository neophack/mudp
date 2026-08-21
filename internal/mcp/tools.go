package mcp

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/kballard/go-shellquote"
	"mudp/internal/dockerx"
)

// serviceUid/serviceGid are the mudp process's own uid/gid, captured once.
// MCP write tools stamp these onto every tar header they build, so that when
// Docker extracts the archive into the bind-mounted /workspace (the host
// netdisk), the files/dirs land owned by the mudp service user rather than
// root. Without this, write_file/edit_file/upload_file/copy_file leave
// root-owned files on the host (the headers default to 0), which breaks panel
// netdisk ops and quotas. On Windows these report -1, but Windows builds never
// serve a Linux bind-mount, so the values are moot there; we clamp -1 to 0 to
// keep the tar header encodable (USTAR cannot encode negative uid/gid).
func nonNegativeUidGid() (int, int) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 {
		uid = 0
	}
	if gid < 0 {
		gid = 0
	}
	return uid, gid
}

var (
	serviceUid, serviceGid = nonNegativeUidGid()
)

// maxReadFileBytes caps how much a single read_file call returns. Larger files
// should be fetched via the web file manager or split into ranges.
const maxReadFileBytes = 10 << 20 // 10 MiB

// NewContainerServer builds an MCP server that exposes operations on a single
// container. Every tool is scoped to containerID via closure capture, so an MCP
// client can never reach a different container through this server.
func NewContainerServer(ctx context.Context, dc *dockerx.Client, containerID, containerName string) *Server {
	labelContext := "Container labels: unavailable until get_info is called."
	if info, err := dc.Inspect(ctx, containerID); err == nil {
		labelContext = labelPrompt(info)
	}
	tools := []Tool{
		{
			Name:        "exec_command",
			Description: labelContext + " Run a shell command inside the container and return stdout, stderr, and the exit code. The command runs non-interactively.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "The shell command to execute, e.g. \"ls -la /workspace\" or \"python train.py\"." },
    "workdir": { "type": "string", "description": "Optional working directory inside the container (absolute path). Defaults to the container's working dir." },
    "timeout": { "type": "integer", "description": "Timeout in seconds. Default 120, max 600.", "default": 120, "minimum": 1, "maximum": 600 }
  },
  "required": ["command"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleExec(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "read_file",
			Description: "Read the text contents of a single file inside the container. Returns up to 10 MiB. Use for source code, configs, and logs.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute path of the file inside the container, e.g. \"/workspace/main.py\"." }
  },
  "required": ["path"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleReadFile(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "write_file",
			Description: "Create or overwrite a file inside the container. Parent directories are created automatically. Use for writing code, configs, and scripts.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute path of the file to write, e.g. \"/workspace/app.py\"." },
    "content": { "type": "string", "description": "The full text content to write. Overwrites the file if it already exists." }
  },
  "required": ["path", "content"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleWriteFile(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "list_files",
			Description: "List the entries (files and directories) in a directory inside the container.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute directory path to list, e.g. \"/workspace\". Defaults to \"/\".", "default": "/" }
  }
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleListFiles(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "get_logs",
			Description: "Fetch recent container logs (stdout + stderr). The container does not need to be running.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "tail": { "type": "integer", "description": "Number of recent lines to return. Default 200.", "default": 200, "minimum": 1, "maximum": 5000 }
  }
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleGetLogs(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "get_info",
			Description: labelContext + " Return the container's inspect details: state, image, IP, ports, environment, labels, GPU settings, mounts, and networks. Read-only. Use labels for runtime/capability metadata.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Handler: func(ctx context.Context, _ json.RawMessage) (*CallResult, error) {
				info, err := dc.Inspect(ctx, containerID)
				if err != nil {
					return nil, err
				}
				return jsonTextResult(info)
			},
		},
		{
			Name:        "start",
			Description: "Start the container if it is stopped.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Handler: func(ctx context.Context, _ json.RawMessage) (*CallResult, error) {
				if err := dc.Action(ctx, containerID, "start"); err != nil {
					return nil, err
				}
				return textResult("container started"), nil
			},
		},
		{
			Name:        "stop",
			Description: "Stop the container gracefully (SIGTERM, then SIGKILL after grace period).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Handler: func(ctx context.Context, _ json.RawMessage) (*CallResult, error) {
				if err := dc.Action(ctx, containerID, "stop"); err != nil {
					return nil, err
				}
				return textResult("container stopped"), nil
			},
		},
		{
			Name:        "restart",
			Description: "Restart the container (stop then start).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Handler: func(ctx context.Context, _ json.RawMessage) (*CallResult, error) {
				if err := dc.Action(ctx, containerID, "restart"); err != nil {
					return nil, err
				}
				return textResult("container restarted"), nil
			},
		},
		{
			Name:        "edit_file",
			Description: "Make precise text edits to an existing file. Each edit's oldText must match exactly once in the file (unless replaceAll is set). Prefer this over write_file when changing a few lines in a large file, because it avoids rewriting the whole file and makes changes verifiable.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute path of the file to edit." },
    "edits": {
      "type": "array",
      "description": "Ordered list of text replacements to apply.",
      "items": {
        "type": "object",
        "properties": {
          "oldText": { "type": "string", "description": "The exact text to find. Must be unique unless replaceAll is set." },
          "newText": { "type": "string", "description": "The replacement text." }
        },
        "required": ["oldText", "newText"]
      }
    },
    "replaceAll": { "type": "boolean", "description": "If true, replace every occurrence of each oldText instead of requiring a unique match.", "default": false }
  },
  "required": ["path", "edits"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleEditFile(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "copy_file",
			Description: "Copy a file or directory tree to another location inside the container. Works via the Docker archive API, so it needs no shell or coreutils inside the container. If the destination is an existing directory, the source is copied into it.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "source": { "type": "string", "description": "Absolute path of the file or directory to copy." },
    "destination": { "type": "string", "description": "Absolute destination path." }
  },
  "required": ["source", "destination"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleCopyFile(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "glob",
			Description: "Find files and directories by name pattern, starting from a directory. Returns a JSON array of matching container paths. Implemented by walking the Docker archive, so it works on containers without find or a shell (including stopped containers).",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute directory to search from. Defaults to /.", "default": "/" },
    "pattern": { "type": "string", "description": "Glob pattern, e.g. \"*.go\" or \"test_*\". Defaults to *.", "default": "*" }
  }
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleGlob(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "search_files",
			Description: "Search for a text pattern across files under a directory. Returns matching lines with their file and line number. Implemented by reading file contents from the Docker archive, so it works on containers without grep or a shell (including stopped containers).",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute directory to search in. Defaults to /.", "default": "/" },
    "pattern": { "type": "string", "description": "The text or regex pattern to search for." },
    "include": { "type": "string", "description": "Optional filename glob to restrict the search to, e.g. \"*.py\"." },
    "caseInsensitive": { "type": "boolean", "description": "Match regardless of case.", "default": false }
  },
  "required": ["pattern"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleSearchFiles(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "upload_file",
			Description: "Create or overwrite a file from base64-encoded content. Supports binary files (images, archives, compiled assets) that write_file cannot handle. Parent directories are created automatically. Prefer write_file for plain text.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute path of the file to write, e.g. \"/workspace/logo.png\"." },
    "content_base64": { "type": "string", "description": "The file contents as standard base64 (no data: prefix). Decoded size must be at most 16 MiB." }
  },
  "required": ["path", "content_base64"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleUploadFile(ctx, dc, containerID, args)
			},
		},
		{
			Name:        "download_file",
			Description: "Read a single file as base64-encoded content, including binary files (images, archives). Returns an object with path, size, and base64. Prefer read_file for plain text.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Absolute path of the file inside the container." }
  },
  "required": ["path"]
}`),
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return handleDownloadFile(ctx, dc, containerID, args)
			},
		},
	}
	name := "mudp-container"
	if containerName != "" {
		name = "mudp-" + containerName
	}
	return NewServer(name, "1.0.0", tools).WithInstructions(labelContext + " This MCP server is scoped to exactly one container. File tools (read_file, write_file, edit_file, list_files, copy_file, glob, search_files, upload_file, download_file) work through the Docker archive API and need no shell or coreutils inside the container — they run on stopped containers too. exec_command requires the container to be running and to have a shell. Use get_info for the full label set before making runtime-specific changes.")
}

// --- tool handlers ---

type execArgs struct {
	Command string `json:"command"`
	Workdir string `json:"workdir"`
	Timeout int    `json:"timeout"`
}

func handleExec(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p execArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(p.Command) == "" {
		return errorResult("command is required"), nil
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 600 {
		timeout = 600
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Run via /bin/sh -c so pipes, redirects, && chains, and env vars work as
	// the agent expects. If a workdir was supplied, cd into it first.
	cmd := p.Command
	shell := "/bin/sh"
	// Prefer bash when available for history/autocomplete niceties, but sh is a
	// safe universal default present even on distroless-ish images.
	if _, err := dc.Exec(ctx, id, []string{"test", "-x", "/bin/bash"}, "", nil); err == nil {
		shell = "/bin/bash"
	}
	if strings.TrimSpace(p.Workdir) != "" {
		cmd = fmt.Sprintf("cd %s && %s", shellquote.Join(p.Workdir), p.Command)
	}
	result, err := dc.Exec(ctx, id, []string{shell, "-c", cmd}, "", nil)
	if err != nil {
		// The exec ran but the command exited non-zero: return what we have so
		// the agent can see the error message. A transport error (container
		// stopped, timeout) is surfaced separately.
		if result.ExitCode != 0 || result.Stdout != "" || result.Stderr != "" {
			return execResultText(result), nil
		}
		return nil, err
	}
	return execResultText(result), nil
}

func execResultText(r dockerx.ExecResult) *CallResult {
	var b strings.Builder
	if r.Stdout != "" {
		b.WriteString(r.Stdout)
	}
	if r.Stderr != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[stderr]\n")
		b.WriteString(r.Stderr)
	}
	fmt.Fprintf(&b, "\n[exit code] %d", r.ExitCode)
	return textResult(strings.TrimSpace(b.String()))
}

type readArgs struct {
	Path string `json:"path"`
}

func handleReadFile(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p readArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return errorResult("path is required"), nil
	}
	rc, stat, err := dc.ContainerFileDownload(ctx, id, p.Path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if stat.Mode.IsDir() {
		return errorResult("path is a directory; use list_files instead"), nil
	}
	data, err := readArchiveFile(rc, stat.Name, maxReadFileBytes)
	if err != nil {
		return nil, err
	}
	return textResult(string(data)), nil
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func handleWriteFile(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p writeArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return errorResult("path is required"), nil
	}
	p.Path = path.Clean(p.Path)

	// buildPathTar emits directory entries for every parent segment followed by
	// the file entry, so CopyToContainer creates the full path on its own — no
	// mkdir/shell needed. This works on distroless and scratch images.
	tarBytes := buildPathTar(p.Path, []byte(p.Content))
	if err := dc.ContainerFileUpload(ctx, id, "/", bytes.NewReader(tarBytes)); err != nil {
		return nil, err
	}
	return textResult(fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path)), nil
}

type listArgs struct {
	Path string `json:"path"`
}

func handleListFiles(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p listArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	target := strings.TrimSpace(p.Path)
	if target == "" {
		target = "/"
	}
	entries, err := dc.ContainerFileList(ctx, id, target)
	if err != nil {
		return nil, err
	}
	return jsonTextResult(entries)
}

type logsArgs struct {
	Tail int `json:"tail"`
}

func handleGetLogs(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p logsArgs
	_ = parseArgs(raw, &p)
	tail := p.Tail
	if tail <= 0 {
		tail = 200
	}
	if tail > 5000 {
		tail = 5000
	}
	logs, err := dc.Logs(ctx, id, tail)
	if err != nil {
		return nil, err
	}
	return textResult(logs), nil
}

func labelPrompt(info dockerx.InspectInfo) string {
	parts := []string{}
	if info.Name != "" {
		parts = append(parts, "container="+strings.TrimPrefix(info.Name, "/"))
	}
	image := info.ImageName
	if image == "" {
		image = info.Image
	}
	if image != "" {
		parts = append(parts, "image="+image)
	}
	if info.State != "" {
		parts = append(parts, "state="+info.State)
	}
	labels := formatLabels(info.Labels)
	if labels == "" {
		labels = "none"
	}
	parts = append(parts, "labels="+labels)
	return "Container metadata from Docker labels: " + strings.Join(parts, "; ")
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if isInternalLabel(k) {
			continue
		}
		v := labels[k]
		if len(v) > 80 {
			v = v[:77] + "..."
		}
		out = append(out, k+"="+v)
	}
	return strings.Join(out, ", ")
}

func isInternalLabel(key string) bool {
	return strings.HasPrefix(key, "mudp.") ||
		strings.HasPrefix(key, "com.docker.") ||
		strings.HasPrefix(key, "desktop.docker.io/") ||
		strings.HasPrefix(key, "org.opencontainers.image.")
}

// buildPathTar constructs an in-memory tar that, when uploaded via
// CopyToContainer to "/", reproduces the directory tree leading to fullPath and
// writes content at that path. Every ancestor directory becomes a TypeDir entry
// so the full path is created without relying on a shell or mkdir inside the
// container. The path is normalized and a leading slash is stripped so entries
// are relative to the upload root.
func buildPathTar(fullPath string, content []byte) []byte {
	cleaned := path.Clean(fullPath)
	cleaned = strings.TrimPrefix(cleaned, "/")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	segments := strings.Split(cleaned, "/")
	// Emit each ancestor directory so intermediate folders exist at the dest.
	// Headers carry the service uid/gid so files created under the bind-mounted
	// /workspace land owned by the mudp user on the host, not root.
	for i := 1; i < len(segments); i++ {
		dirName := strings.Join(segments[:i], "/") + "/"
		_ = tw.WriteHeader(&tar.Header{Name: dirName, Mode: 0755, Typeflag: tar.TypeDir, Uid: serviceUid, Gid: serviceGid})
	}
	// The file entry's Name is its full path relative to the upload root; using
	// just the basename would drop every parent segment and land the file at "/".
	_ = tw.WriteHeader(&tar.Header{Name: cleaned, Mode: 0644, Size: int64(len(content)), Uid: serviceUid, Gid: serviceGid})
	_, _ = tw.Write(content)
	_ = tw.Close()
	return buf.Bytes()
}

// shellQuote wraps a path in single quotes for safe inclusion in a sh -c
// command string, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
