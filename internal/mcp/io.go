package mcp

import (
	"encoding/json"
	"io"
	"net/http"
)

// maxRequestBodyBytes caps how much a single JSON-RPC request may carry. It is
// larger than the file-size limits below because upload_file carries base64
// content (~1.37x the raw bytes) plus the JSON-RPC envelope.
const maxRequestBodyBytes = 64 << 20 // 64 MiB cap

// readBody reads and returns the request body, capped at a generous limit so a
// malicious client cannot exhaust server memory with an oversized request.
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
}

// parseArgs decodes the raw JSON arguments of a tools/call request into dst.
// An empty argument object is treated as an empty struct.
func parseArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// textResult is a convenience constructor for a single-text-content result.
func textResult(text string) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: text}}}
}

// errorResult builds an IsError result carrying the given message.
func errorResult(msg string) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: msg}}, IsError: true}
}

// jsonTextResult marshals v as pretty-printed JSON and wraps it in a text
// content block. Useful for tools that return structured data (inspect, logs).
func jsonTextResult(v any) (*CallResult, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return textResult(string(out)), nil
}

// Assert ServeHTTP satisfies http.Handler at compile time.
var _ http.Handler = (*Server)(nil)

// Ensure net/http is referenced (used by ServeHTTP signature above).
var _ = http.StatusOK
