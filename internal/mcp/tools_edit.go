package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"mudp/internal/dockerx"
)

// editRequest is a single precise text replacement inside edit_file.
type editRequest struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// editArgs are the arguments to the edit_file tool.
type editArgs struct {
	Path       string        `json:"path"`
	Edits      []editRequest `json:"edits"`
	ReplaceAll bool          `json:"replaceAll"`
}

// applyEdits performs a sequence of precise text replacements on content. Each
// edit's OldText must occur exactly once in the current content (after prior
// edits are applied); otherwise an error reports the conflict so the caller can
// adjust. This mirrors the semantics of a code-editor Edit: precise, verifiable
// changes rather than whole-file rewrites.
//
// When ReplaceAll is true, every occurrence of OldText is replaced instead of
// requiring uniqueness (still requires at least one match).
func applyEdits(content string, edits []editRequest, replaceAll bool) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("at least one edit is required")
	}
	out := content
	for i, e := range edits {
		if e.OldText == "" {
			return "", fmt.Errorf("edit %d: oldText is empty", i)
		}
		count := strings.Count(out, e.OldText)
		if count == 0 {
			return "", fmt.Errorf("edit %d: oldText not found in file", i)
		}
		if count > 1 && !replaceAll {
			return "", fmt.Errorf("edit %d: oldText matches %d times; make it unique or set replaceAll", i, count)
		}
		if replaceAll {
			out = strings.ReplaceAll(out, e.OldText, e.NewText)
			continue
		}
		out = strings.Replace(out, e.OldText, e.NewText, 1)
	}
	return out, nil
}

// handleEditFile reads a file, applies precise text edits, and writes it back.
// It is the targeted-edit counterpart to write_file: instead of overwriting the
// whole file, an agent changes only the lines it needs to, which is far safer
// for editing large source files. All file I/O goes through the Docker archive
// API, so it works on containers without a shell.
func handleEditFile(ctx context.Context, dc *dockerx.Client, id string, raw json.RawMessage) (*CallResult, error) {
	var p editArgs
	if err := parseArgs(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return errorResult("path is required"), nil
	}
	p.Path = path.Clean(p.Path)

	// Read the current contents (capped at maxReadFileBytes so editing truly
	// large files fails fast instead of exhausting memory).
	rc, stat, err := dc.ContainerFileDownload(ctx, id, p.Path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if stat.Mode.IsDir() {
		return errorResult("path is a directory; edit_file only edits files"), nil
	}
	data, err := readArchiveFile(rc, stat.Name, maxReadFileBytes)
	if err != nil {
		return nil, err
	}

	updated, err := applyEdits(string(data), p.Edits, p.ReplaceAll)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	tarBytes := buildPathTar(p.Path, []byte(updated))
	if err := dc.ContainerFileUpload(ctx, id, "/", bytes.NewReader(tarBytes)); err != nil {
		return nil, err
	}
	return textResult(fmt.Sprintf("applied %d edit(s) to %s", len(p.Edits), p.Path)), nil
}
