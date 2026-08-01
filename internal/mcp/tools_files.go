package mcp

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"mudp/internal/dockerx"
)

// maxBinaryFileBytes caps the raw byte size of files moved through upload_file
// (after base64 decoding) and download_file (before base64 encoding). It keeps
// a single tool call from exhausting server memory. Larger transfers should go
// through the web file manager.
const maxBinaryFileBytes = 16 << 20 // 16 MiB raw

// Result caps keep a single glob/search call bounded when pointed at a large
// tree (CopyFromContainer returns the whole subtree).
const (
	maxGlobResults     = 500
	maxSearchResults   = 500
	maxSearchFileBytes = 5 << 20 // skip files larger than 5 MiB during search
)

// --- glob ---

type globArgs struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
}

func handleGlob(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p globArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	root := strings.TrimSpace(p.Path)
	if root == "" {
		root = "/"
	}
	pattern := strings.TrimSpace(p.Pattern)
	if pattern == "" {
		pattern = "*"
	}

	// CopyFromContainer returns the whole subtree as a tar. We walk every header
	// and match entry names against the pattern, so this works on stopped
	// containers and images without find/grep/shell.
	rc, stat, err := dc.ContainerFileDownload(ctx, id, root)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if !stat.Mode.IsDir() {
		// A single file: match its own name.
		if matched, _ := path.Match(pattern, stat.Name); matched {
			return jsonTextResult([]string{root})
		}
		return jsonTextResult([]string{})
	}

	matches := walkArchiveNames(rc, func(entry string) bool {
		matched, _ := path.Match(pattern, path.Base(entry))
		return matched
	})
	if len(matches) > maxGlobResults {
		matches = matches[:maxGlobResults]
	}
	return jsonTextResult(matches)
}

// walkArchiveNames reads a tar stream from rc and returns the container paths
// of every regular-file or directory entry whose name passes keep. The Docker
// archive's entry names are relative to the copied path; this function joins
// them back onto the root the caller copied from. Symlinks and hardlinks are
// skipped so an agent never follows a link planted inside the container.
func walkArchiveNames(rc io.Reader, keep func(entry string) bool) []string {
	tr := tar.NewReader(rc)
	var out []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		name = strings.TrimPrefix(name, "./")
		name = strings.TrimPrefix(name, "/")
		if name == "" || name == "." {
			continue
		}
		if keep(name) {
			out = append(out, "/"+name)
		}
	}
	return out
}

// --- search_files ---

type searchArgs struct {
	Path            string `json:"path"`
	Pattern         string `json:"pattern"`
	Include         string `json:"include"`
	CaseInsensitive bool   `json:"caseInsensitive"`
}

// searchHit is one matched line from search_files.
type searchHit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

func handleSearchFiles(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p searchArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	root := strings.TrimSpace(p.Path)
	if root == "" {
		root = "/"
	}
	pattern := strings.TrimSpace(p.Pattern)
	if pattern == "" {
		return errorResult("pattern is required"), nil
	}
	needle := pattern
	if p.CaseInsensitive {
		needle = strings.ToLower(pattern)
	}
	include := strings.TrimSpace(p.Include)

	rc, stat, err := dc.ContainerFileDownload(ctx, id, root)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var hits []searchHit
	if !stat.Mode.IsDir() {
		// Searching a single file.
		if include != "" {
			if matched, _ := path.Match(include, stat.Name); !matched {
				return jsonTextResult([]searchHit{})
			}
		}
		body, readErr := readArchiveFile(rc, stat.Name, maxSearchFileBytes)
		if readErr != nil {
			return nil, readErr
		}
		hits = searchLines(root, string(body), needle, p.CaseInsensitive)
	} else {
		hits, err = searchArchive(rc, needle, p.CaseInsensitive, include)
		if err != nil {
			return nil, err
		}
	}
	if len(hits) > maxSearchResults {
		hits = hits[:maxSearchResults]
	}
	return jsonTextResult(hits)
}

// searchArchive walks a tar stream and searches every regular file's body for
// needle. Symlinks/hardlinks are skipped. Files larger than maxSearchFileBytes
// are skipped to avoid blowing memory on large binaries.
func searchArchive(rc io.Reader, needle string, caseInsensitive bool, include string) ([]searchHit, error) {
	tr := tar.NewReader(rc)
	var all []searchHit
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		name = strings.TrimPrefix(name, "/")
		base := path.Base(strings.TrimSuffix(name, "/"))
		if include != "" {
			if matched, _ := path.Match(include, base); !matched {
				continue
			}
		}
		if hdr.Size > maxSearchFileBytes {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxSearchFileBytes+1))
		if err != nil {
			continue
		}
		filePath := "/" + name
		all = append(all, searchLines(filePath, string(body), needle, caseInsensitive)...)
	}
	return all, nil
}

// searchLines returns every line in content that contains needle. When
// caseInsensitive is true both sides are lowercased before comparing. The line
// number is 1-based and content is the matched line without its terminator.
func searchLines(filePath, content, needle string, caseInsensitive bool) []searchHit {
	if needle == "" {
		return nil
	}
	var hits []searchHit
	lineNo := 0
	start := 0
	for i := 0; i <= len(content); i++ {
		if i == len(content) || content[i] == '\n' {
			lineNo++
			line := content[start:i]
			probe := line
			if caseInsensitive {
				probe = strings.ToLower(line)
			}
			if strings.Contains(probe, needle) {
				hits = append(hits, searchHit{Path: filePath, Line: lineNo, Content: strings.TrimRight(line, "\r")})
			}
			start = i + 1
		}
	}
	return hits
}

// --- copy_file ---

type copyArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func handleCopyFile(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p copyArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(p.Source) == "" || strings.TrimSpace(p.Destination) == "" {
		return errorResult("source and destination are required"), nil
	}
	p.Source = path.Clean(p.Source)
	p.Destination = path.Clean(p.Destination)

	rc, stat, err := dc.ContainerFileDownload(ctx, id, p.Source)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	// CopyToContainer extracts entries relative to the destination directory.
	// We rewrite every entry name so its leading segment is the destination
	// basename, producing a faithful copy under Destination.
	destBase := path.Base(p.Destination)
	// When the destination is an existing directory, copy INTO it (like cp into
	// a dir), so the source keeps its own name.
	if dstat, derr := dc.ContainerFileStat(ctx, id, p.Destination); derr == nil && dstat.Mode.IsDir() {
		destBase = path.Base(p.Source)
		p.Destination = path.Join(p.Destination, destBase)
	}
	rewritten, err := rewriteArchive(rc, stat.Name, destBase)
	if err != nil {
		return nil, err
	}

	destDir := path.Dir(p.Destination)
	if err := dc.ContainerFileUpload(ctx, id, destDir, bytes.NewReader(rewritten)); err != nil {
		return nil, err
	}
	return textResult(fmt.Sprintf("copied %s to %s", p.Source, p.Destination)), nil
}

// rewriteArchive reads a tar stream whose root entry is srcName and produces a
// new in-memory tar where that root is renamed to destName, preserving the
// relative structure beneath it. Symlinks/hardlinks are dropped for safety.
// Directory entries are rewritten so intermediate folders are recreated at the
// destination.
func rewriteArchive(rc io.Reader, srcName, destName string) ([]byte, error) {
	srcPrefix := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(srcName, "./"), "/"), "/")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(hdr.Name, "./"), "/")
		rel := name
		if srcPrefix != "" && strings.HasPrefix(name, srcPrefix+"/") {
			rel = strings.TrimPrefix(name, srcPrefix+"/")
		} else if name == srcPrefix {
			rel = ""
		}
		newName := destName
		if rel != "" {
			newName = destName + "/" + rel
		}
		nh := *hdr
		nh.Name = newName
		// Overwrite ownership with the service uid/gid: copy_file re-uploads a
		// header copied from the container (typically root-owned), and when the
		// destination is under the bind-mounted /workspace that would leave
		// root-owned files on the host netdisk.
		nh.Uid = serviceUid
		nh.Gid = serviceGid
		if hdr.Typeflag == tar.TypeDir {
			nh.Name = strings.TrimSuffix(newName, "/") + "/"
		}
		if err := tw.WriteHeader(&nh); err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 {
			if _, err := io.Copy(tw, io.LimitReader(tr, hdr.Size)); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// --- upload_file (binary, base64-encoded) ---

type uploadArgs struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

func handleUploadFile(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p uploadArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return errorResult("path is required"), nil
	}
	if strings.TrimSpace(p.ContentBase64) == "" {
		return errorResult("content_base64 is required"), nil
	}
	p.Path = path.Clean(p.Path)

	// Decode in two stages so we can enforce the size limit without buffering
	// unbounded data into memory first.
	dec := base64.StdEncoding
	decodedLen := dec.DecodedLen(len(p.ContentBase64))
	if decodedLen > maxBinaryFileBytes {
		return errorResult(fmt.Sprintf("decoded file is %d bytes, exceeds %d byte limit", decodedLen, maxBinaryFileBytes)), nil
	}
	data := make([]byte, decodedLen)
	n, err := dec.Decode(data, []byte(p.ContentBase64))
	if err != nil {
		return errorResult("invalid base64 content: " + err.Error()), nil
	}
	data = data[:n]

	tarBytes := buildPathTar(p.Path, data)
	// Upload relative to "/" so the directory entries create the full path.
	if err := dc.ContainerFileUpload(ctx, id, "/", bytes.NewReader(tarBytes)); err != nil {
		return nil, err
	}
	return textResult(fmt.Sprintf("uploaded %d bytes to %s", n, p.Path)), nil
}

// --- download_file (binary, base64-encoded) ---

type downloadArgs struct {
	Path string `json:"path"`
}

func handleDownloadFile(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p downloadArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return errorResult("path is required"), nil
	}
	p.Path = path.Clean(p.Path)
	rc, stat, err := dc.ContainerFileDownload(ctx, id, p.Path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if stat.Mode.IsDir() {
		return errorResult("path is a directory; download_file only handles single files"), nil
	}
	data, err := readArchiveFile(rc, stat.Name, maxBinaryFileBytes)
	if err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return jsonTextResult(map[string]any{
		"path":   p.Path,
		"size":   len(data),
		"base64": encoded,
	})
}

// readArchiveFile reads the first regular-file entry named name from a tar
// stream (as returned by CopyFromContainer for a single file), capped at limit
// bytes.
func readArchiveFile(rc io.Reader, name string, limit int) ([]byte, error) {
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("file %q not found in archive", name)
			}
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size > int64(limit) {
			return nil, fmt.Errorf("file is %d bytes, exceeds %d byte limit", hdr.Size, limit)
		}
		return io.ReadAll(io.LimitReader(tr, int64(limit)+1))
	}
}
