// Package mcp implements a minimal MCP (Model Context Protocol) server that
// exposes container operations as tools over the Streamable HTTP transport.
//
// The protocol layer here is intentionally small: it speaks JSON-RPC 2.0 over a
// single POST endpoint and supports the subset of MCP methods that the major
// clients (Claude Code, Codex, Cursor, Kimi) use — initialize, tools/list, and
// tools/call. Tool definitions and their handlers are registered by the caller
// (see NewContainerServer), keeping the protocol core transport-agnostic.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// JSON-RPC 2.0 standard error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Content is a single piece of tool result content. MCP defines several content
// types (text, image, resource); only "text" is used here because container
// tool output (stdout, file contents, logs) is textual.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallResult is the result payload of a tools/call request.
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Tool is a named, callable MCP tool backed by a Go handler.
type Tool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema for the tool's arguments, serialized as
	// RawMessage so it is embedded verbatim in the tools/list response.
	InputSchema json.RawMessage
	// Handler executes the tool. It receives the raw arguments object and must
	// return a CallResult (never a transport-level error — tool failures are
	// reported as IsError=true content, per the MCP spec).
	Handler func(ctx context.Context, args json.RawMessage) (*CallResult, error)
}

// Server is an MCP server with a fixed set of registered tools.
type Server struct {
	name         string
	version      string
	tools        []Tool
	instructions string
}

// NewServer builds a server with the given identity and tool set.
func NewServer(name, version string, tools []Tool) *Server {
	return &Server{name: name, version: version, tools: tools}
}

// WithInstructions adds server-level guidance returned during initialize.
func (s *Server) WithInstructions(instructions string) *Server {
	s.instructions = instructions
	return s
}

// rpcRequest is the JSON-RPC 2.0 request envelope.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is the JSON-RPC 2.0 response envelope.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Handle parses a JSON-RPC request body and returns the response body. A nil
// response body signals a notification (a request without an id) that needs no
// reply. The HTTP status is always 200 for a well-formed request — errors are
// conveyed inside the JSON-RPC error object, per the spec.
func (s *Server) Handle(ctx context.Context, body []byte) ([]byte, int, bool) {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// Parse error: the request could not be decoded.
		resp := rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "Parse error"}}
		out, _ := json.Marshal(resp)
		return out, http.StatusOK, false
	}

	// A notification has no "id" (or a literal null). We still process it but
	// the caller does not need to write a body back.
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return s.errorResponse(req.ID, codeInvalidRequest, "jsonrpc must be \"2.0\""), http.StatusOK, !isNotification
	}

	var result any
	switch req.Method {
	case "initialize":
		result = s.handleInitialize(req.Params)
	case "notifications/initialized":
		// Client confirms initialization completed. No response needed.
		return nil, http.StatusOK, false
	case "tools/list":
		result = s.handleToolsList()
	case "tools/call":
		r, err := s.handleToolsCall(ctx, req.Params)
		if err != nil {
			return s.errorResponse(req.ID, codeInternalError, err.Error()), http.StatusOK, !isNotification
		}
		result = r
	case "ping":
		result = map[string]any{}
	default:
		return s.errorResponse(req.ID, codeMethodNotFound, "method not found: "+req.Method), http.StatusOK, !isNotification
	}

	if isNotification {
		return nil, http.StatusOK, false
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	out, err := json.Marshal(resp)
	if err != nil {
		return s.errorResponse(req.ID, codeInternalError, "marshal response: "+err.Error()), http.StatusOK, true
	}
	return out, http.StatusOK, true
}

func (s *Server) errorResponse(id json.RawMessage, code int, msg string) []byte {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
	out, _ := json.Marshal(resp)
	return out
}

// handleInitialize returns the server capabilities and protocol version agreed
// upon during the MCP handshake.
func (s *Server) handleInitialize(_ json.RawMessage) any {
	out := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities": map[string]any{
			"tools": map[string]any{
				"listChanged": false,
			},
		},
		"serverInfo": map[string]any{
			"name":    s.name,
			"version": s.version,
		},
	}
	if s.instructions != "" {
		out["instructions"] = s.instructions
	}
	return out
}

// toolListEntry is one element of the tools/list result.
type toolListEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (s *Server) handleToolsList() any {
	entries := make([]toolListEntry, 0, len(s.tools))
	for _, t := range s.tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		entries = append(entries, toolListEntry{
			Name: t.Name, Description: t.Description, InputSchema: schema,
		})
	}
	return map[string]any{"tools": entries}
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (*CallResult, error) {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid tools/call params: %w", err)
	}
	for _, t := range s.tools {
		if t.Name == p.Name {
			res, err := t.Handler(ctx, p.Arguments)
			if err != nil {
				// Tool execution failure: surface it as an MCP error result
				// (IsError) so the client sees the message rather than a
				// transport error.
				return &CallResult{
					Content: []Content{{Type: "text", Text: err.Error()}},
					IsError: true,
				}, nil
			}
			if res == nil {
				return &CallResult{Content: []Content{{Type: "text", Text: "(no output)"}}}, nil
			}
			return res, nil
		}
	}
	return &CallResult{
		Content: []Content{{Type: "text", Text: "unknown tool: " + p.Name}},
		IsError: true,
	}, nil
}

// ServeHTTP implements http.Handler for the Streamable HTTP transport. It
// accepts a single JSON-RPC request per POST and writes the response back
// synchronously. SSE streaming is not implemented; this covers the common
// request/response interaction model used by MCP clients.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, status, hasBody := s.Handle(r.Context(), body)
	if hasBody && resp != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(resp)
		return
	}
	// Notification: respond with 202 Accepted and no body.
	w.WriteHeader(http.StatusAccepted)
}
