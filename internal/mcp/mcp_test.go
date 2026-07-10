package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// newTestServer builds a minimal server with one echo-style tool for testing
// the JSON-RPC dispatch without touching Docker.
func newTestServer() *Server {
	return NewServer("test-mcp", "0.1.0", []Tool{
		{
			Name:        "echo",
			Description: "echoes back the message",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]}`),
			Handler: func(_ context.Context, args json.RawMessage) (*CallResult, error) {
				var p struct {
					Msg string `json:"msg"`
				}
				_ = json.Unmarshal(args, &p)
				if p.Msg == "boom" {
					return nil, &testErr{msg: "forced failure"}
				}
				return textResult("echo: " + p.Msg), nil
			},
		},
	})
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

func call(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	body := buildRequest("1", method, params)
	resp, status, hasBody := s.Handle(context.Background(), body)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if !hasBody {
		t.Fatal("expected a response body for a request with an id")
	}
	var m map[string]any
	if err := json.Unmarshal(resp, &m); err != nil {
		t.Fatalf("unmarshal response: %v\n%s", err, resp)
	}
	return m
}

func buildRequest(id, method string, params any) []byte {
	var raw json.RawMessage
	if params != nil {
		b, _ := json.Marshal(params)
		raw = b
	}
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = raw
	}
	b, _ := json.Marshal(req)
	return b
}

func TestInitialize(t *testing.T) {
	s := newTestServer()
	m := call(t, s, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
	})
	if m["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v", m["jsonrpc"])
	}
	result, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", m)
	}
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info == nil || info["name"] != "test-mcp" {
		t.Errorf("serverInfo = %v", info)
	}
}

func TestToolsList(t *testing.T) {
	s := newTestServer()
	m := call(t, s, "tools/list", nil)
	result, _ := m["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "echo" {
		t.Errorf("tool name = %v", tool["name"])
	}
	if _, ok := tool["inputSchema"].(map[string]any); !ok {
		t.Errorf("inputSchema not an object: %T", tool["inputSchema"])
	}
}

func TestToolsCallSuccess(t *testing.T) {
	s := newTestServer()
	m := call(t, s, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"msg": "hello"},
	})
	result, _ := m["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || !strings.Contains(block["text"].(string), "echo: hello") {
		t.Errorf("unexpected content: %v", block)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Error("expected isError=false for a successful call")
	}
}

func TestToolsCallHandlerError(t *testing.T) {
	s := newTestServer()
	// The echo tool returns a Go error when msg=="boom". The MCP layer should
	// translate that into an IsError result (not a transport error).
	m := call(t, s, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"msg": "boom"},
	})
	result, _ := m["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true for a handler error")
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	s := newTestServer()
	m := call(t, s, "tools/call", map[string]any{"name": "nope"})
	result, _ := m["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true for an unknown tool")
	}
}

func TestMethodNotFound(t *testing.T) {
	s := newTestServer()
	m := call(t, s, "resources/list", nil)
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %v", m)
	}
	if code, _ := errObj["code"].(float64); int(code) != codeMethodNotFound {
		t.Errorf("error code = %v, want %d", errObj["code"], codeMethodNotFound)
	}
}

func TestNotificationNoBody(t *testing.T) {
	s := newTestServer()
	// notifications/initialized has no id → no response body expected.
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	resp, status, hasBody := s.Handle(context.Background(), body)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if hasBody {
		t.Fatalf("notification should not produce a body, got %s", resp)
	}
}

func TestParseError(t *testing.T) {
	s := newTestServer()
	// Invalid JSON should produce a parse-error response, not a panic.
	resp, status, _ := s.Handle(context.Background(), []byte("{not json"))
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	var m map[string]any
	if err := json.Unmarshal(resp, &m); err != nil {
		t.Fatalf("parse-error response is not valid JSON: %v", err)
	}
	errObj, _ := m["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != codeParseError {
		t.Errorf("error code = %v, want %d", errObj["code"], codeParseError)
	}
}
