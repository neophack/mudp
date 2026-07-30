package server

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
)

// ErrChecksumMismatch is returned by writeFileWithMD5 when the caller supplied an
// expected MD5 and the bytes actually written did not match it. A mismatch means
// the upload arrived corrupted (network truncation, proxy mangling, a wrong
// file) so the destination is removed to avoid leaving bad data on disk.
var ErrChecksumMismatch = errors.New("md5 checksum mismatch")

// uploadResult is the per-file outcome the upload handlers return to the client.
// Path is the user-relative path (netdisk) or base name (volumes) the client can
// key on; MD5 is the server-computed digest so the client can confirm integrity
// even when it did not send one; Error is empty on success.
type uploadResult struct {
	Path  string `json:"path"`
	MD5   string `json:"md5"`
	Error string `json:"error,omitempty"`
}

// writeFileWithMD5 writes the uploaded part src to dstPath (always from
// scratch — a browser resends a plain multipart part in full from byte 0, it
// cannot resume mid-stream, so appending to a same-named pre-existing file
// would splice unrelated bytes together) and computes the MD5 of the bytes it
// writes in the SAME io.Copy pass (via io.MultiWriter) so verification is
// essentially free. Real resumability for large files lives in the separate
// chunked protocol (chunkupload.go), which persists per-chunk state across
// requests instead of relying on a partial file's size.
//
// expectedMD5 is the client-supplied digest (""). When non-empty and it does not
// equal the digest of what was written, the file is removed and
// ErrChecksumMismatch is returned — the caller records the file as failed rather
// than trusting a corrupted upload. When empty, no comparison is done (legacy
// clients / Worker-unavailable browsers) but the digest is still returned.
//
// Returns the lowercase hex MD5 of the written content and any error.
func writeFileWithMD5(dstPath string, src multipart.File, fh *multipart.FileHeader, expectedMD5 string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return "", err
	}

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return "", err
	}

	hash := md5.New()
	mw := io.MultiWriter(dst, hash)
	_, copyErr := io.Copy(mw, src)
	_ = dst.Close()

	if copyErr != nil {
		// A partial/corrupt write is unusable; remove it so the next attempt
		// starts clean rather than resuming from garbage.
		_ = os.Remove(dstPath)
		return "", copyErr
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	if expectedMD5 != "" && !strings.EqualFold(sum, expectedMD5) {
		// Corrupted upload (or a wrong/renamed file): refuse it and leave no
		// bad file behind for a future attempt to trust.
		_ = os.Remove(dstPath)
		return sum, fmt.Errorf("%w: expected %s got %s", ErrChecksumMismatch, expectedMD5, sum)
	}
	return sum, nil
}
