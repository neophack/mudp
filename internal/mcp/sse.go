package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSESession is one client SSE connection. The GET /sse handler owns the
// outbound side (a channel the handler drains into the SSE stream); the POST
// /messages handler pushes inbound JSON-RPC requests through Process which
// routes the response back onto that same channel.
type SSESession struct {
	id          string
	containerID string      // the container this session is scoped to (for in-use lights)
	out         chan []byte // JSON-RPC responses, written to the SSE stream
	server      *Server
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	closeMu     sync.Mutex
}

// sessionHub tracks active SSE sessions by id so a POST /messages request can
// find the connection that a earlier GET /sse established.
type sessionHub struct {
	mu       sync.RWMutex
	sessions map[string]*SSESession
}

func newSessionHub() *sessionHub {
	return &sessionHub{sessions: map[string]*SSESession{}}
}

func (h *sessionHub) add(s *SSESession) {
	h.mu.Lock()
	h.sessions[s.id] = s
	h.mu.Unlock()
}

func (h *sessionHub) get(id string) (*SSESession, bool) {
	h.mu.RLock()
	s, ok := h.sessions[id]
	h.mu.RUnlock()
	return s, ok
}

func (h *sessionHub) remove(id string) {
	h.mu.Lock()
	if s, ok := h.sessions[id]; ok {
		s.close()
		delete(h.sessions, id)
	}
	h.mu.Unlock()
}

func (s *SSESession) close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.cancel()
}

func (s *SSESession) send(data []byte) bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.out <- data:
		return true
	default:
		// The SSE buffer is full; drop the client to avoid blocking the server.
		s.closed = true
		s.cancel()
		return false
	}
}

// newSessionID returns a short random hex id used as the SSE session id and the
// value returned to the client via the endpoint event.
func newSessionID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SSEHub is the shared hub for all SSE sessions served by the app. A single hub
// is created once (by the server) and shared across both the /sse and
// /messages handlers. Sessions are added when a client opens an SSE stream and
// removed when the stream closes or the client disconnects.
type SSEHub struct {
	hub *sessionHub
}

// NewSSEHub builds an empty hub. The caller drives session creation via
// OpenSession and message dispatch via ServeMessages.
func NewSSEHub() *SSEHub {
	return &SSEHub{hub: newSessionHub()}
}

// OpenSession starts a new SSE session bound to the given server (which is
// scoped to a specific container by the caller). containerID lets the hub
// answer "is anything connected to this container?" for the in-use indicator.
// The returned session must be removed via Close when the SSE stream ends.
func (h *SSEHub) OpenSession(server *Server, ctx context.Context, containerID string) *SSESession {
	innerCtx, cancel := context.WithCancel(ctx)
	session := &SSESession{
		id:          newSessionID(),
		containerID: containerID,
		out:         make(chan []byte, 32),
		server:      server,
		ctx:         innerCtx,
		cancel:      cancel,
	}
	h.hub.add(session)
	return session
}

// Close removes a session from the hub and signals its SSE loop to exit.
func (h *SSEHub) Close(session *SSESession) {
	h.hub.remove(session.id)
}

// ActiveForContainer reports how many SSE sessions are currently open against
// the given container. A non-zero result is what lights a token's "in use"
// indicator: an agent has an open SSE stream to that container right now.
func (h *SSEHub) ActiveForContainer(containerID string) int {
	if containerID == "" {
		return 0
	}
	h.hub.mu.RLock()
	defer h.hub.mu.RUnlock()
	n := 0
	for _, s := range h.hub.sessions {
		if s.containerID == containerID && !s.closed {
			n++
		}
	}
	return n
}

// ServeSSE drives the SSE write loop for a session that was opened by
// OpenSession. It blocks until the client disconnects or the context is
// cancelled. The messageEndpointBase is the URL prefix for POST requests
// (e.g. "/mcp/TOKEN/messages"); the session id is appended as a query param
// and announced to the client via the initial "endpoint" event.
func ServeSSE(w http.ResponseWriter, r *http.Request, session *SSESession, messageEndpointBase string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Tell the client where to POST requests. The session id is the glue
	// between this SSE stream and the matching POST /messages call.
	endpoint := messageEndpointBase + "?session=" + session.id
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
	flusher.Flush()

	// Keepalive ticker so reverse proxies don't idle-close the stream.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-session.ctx.Done():
			return
		case msg := <-session.out:
			// Each JSON-RPC response is a single "message" SSE event whose data
			// is the raw JSON-RPC envelope.
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			// SSE comment line as a heartbeat (no event name → clients ignore).
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// ServeMessages handles POST /mcp/{token}/messages?session=ID. It looks up the
// SSE session established by the earlier GET, runs the JSON-RPC request through
// that session's server, and writes the response onto the SSE stream.
func (h *SSEHub) ServeMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "missing session param", http.StatusBadRequest)
		return
	}
	session, ok := h.hub.get(sessionID)
	if !ok {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, _, hasBody := session.server.Handle(r.Context(), body)
	// The POST always gets a 202 (accepted) HTTP status. The actual JSON-RPC
	// response is delivered over the SSE stream, per the MCP SSE transport.
	if hasBody && resp != nil {
		session.send(resp)
	}
	w.WriteHeader(http.StatusAccepted)
	// Write a small JSON body so clients that read it don't choke on empty.
	_, _ = w.Write([]byte(`{"accepted":true}`))
}

// Ensure json is referenced (used by callers that build requests for testing).
var _ = json.RawMessage(nil)
