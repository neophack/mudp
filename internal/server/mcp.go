package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mudp/internal/mcp"
)

// mcpTokenLen is the byte length of the random cleartext token (32 bytes → 64
// hex chars). Long enough to resist brute force, short enough to paste.
const mcpTokenLen = 32

// mcpStreamableHTTP is the public MCP endpoint. A client posts JSON-RPC to
// /mcp/{token}; the token authenticates and scopes the request to a single
// container. This route is registered outside the CSRF-protected group because
// MCP clients authenticate with a bearer token, not a browser session.
//
// POST /mcp/{token}
func (a *App) mcpStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	tok, ok := a.resolveMcpContainer(w, r)
	if !ok {
		return
	}
	srv := mcp.NewContainerServer(r.Context(), a.docker, tok.ContainerID, tok.ContainerName)
	srv.SetAuditHook(a.mcpAuditHook(tok))
	srv.ServeHTTP(w, r)
}

// mcpSSE opens an SSE event stream for an MCP client. The client POSTs
// JSON-RPC requests to /mcp/{token}/messages; responses come back over this
// stream as "message" events.
//
// GET /mcp/{token}/sse
func (a *App) mcpSSE(w http.ResponseWriter, r *http.Request) {
	tok, ok := a.resolveMcpContainer(w, r)
	if !ok {
		return
	}
	srv := mcp.NewContainerServer(r.Context(), a.docker, tok.ContainerID, tok.ContainerName)
	srv.SetAuditHook(a.mcpAuditHook(tok))
	session := a.mcpHub.OpenSession(srv, r.Context())
	defer a.mcpHub.Close(session)
	base := "/mcp/" + chi.URLParam(r, "token") + "/messages"
	mcp.ServeSSE(w, r, session, base)
}

// mcpMessages receives a JSON-RPC request from an SSE MCP client and routes the
// response onto the matching SSE stream (found via the ?session= id).
//
// POST /mcp/{token}/messages?session=ID
func (a *App) mcpMessages(w http.ResponseWriter, r *http.Request) {
	// Authenticate via the URL token too, so a stray POST without a valid token
	// is rejected even if it happens to guess a session id.
	if _, ok := a.resolveMcpContainer(w, r); !ok {
		return
	}
	a.mcpHub.ServeMessages(w, r)
}

// resolveMcpContainer validates the {token} URL param, loads the MCP token,
// confirms the target container is still managed by mudp, and stamps
// last-used. On failure it writes the HTTP error and returns ok=false.
func (a *App) resolveMcpContainer(w http.ResponseWriter, r *http.Request) (mcpTokenResolved, bool) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "token required")
		return mcpTokenResolved{}, false
	}
	tok, err := a.resolveMCPToken(r, token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid or expired token")
		return mcpTokenResolved{}, false
	}
	if owner := a.docker.ManagedOwner(r.Context(), tok.ContainerID); owner == "" {
		writeErr(w, http.StatusNotFound, "container no longer managed")
		return mcpTokenResolved{}, false
	}
	// A token must not outlive its owner's access. Disabling or de-activating an
	// account revokes the browser session immediately (authMiddleware re-reads
	// the user on every request), but an MCP token carries no session, so
	// without this check a departed user's token would keep running commands in
	// the container indefinitely.
	owner, err := a.db.UserByID(tok.OwnerID)
	if err != nil || owner == nil || owner.Disabled || isPending(owner) || !canMutate(owner) {
		writeErr(w, http.StatusUnauthorized, "invalid or expired token")
		return mcpTokenResolved{}, false
	}
	// A token that works on the LAN does not automatically work from the
	// internet: over the external listener the container must also sit on the
	// administrator's safe network. See mcp_remote.go.
	if isRemoteMCP(r) {
		if ok, reason := a.remoteMCPAllowed(r.Context(), tok.ContainerID); !ok {
			writeErr(w, http.StatusForbidden, reason)
			return mcpTokenResolved{}, false
		}
	}
	_ = a.db.MCPTokenTouch(tok.ID)
	return tok, true
}

// resolveMCPToken hashes the cleartext token from the URL and looks it up.
// It also accepts the token via the Authorization: Bearer header for clients
// that prefer that transport; when both are present they must match.
func (a *App) resolveMCPToken(r *http.Request, cleartext string) (mcpTokenResolved, error) {
	if hdr := bearerTokenFromHeader(r); hdr != "" && hdr != cleartext {
		return mcpTokenResolved{}, errors.New("token mismatch")
	}
	hash := sha256Hex(cleartext)
	tok, err := a.db.MCPTokenByHash(hash)
	if err != nil {
		return mcpTokenResolved{}, err
	}
	if tok.Expired() {
		return mcpTokenResolved{}, errors.New("token expired")
	}
	return mcpTokenResolved{ID: tok.ID, ContainerID: tok.ContainerID, ContainerName: tok.ContainerName, OwnerID: tok.OwnerID, Label: tok.Label}, nil
}

type mcpTokenResolved struct {
	ID            int64
	ContainerID   string
	ContainerName string
	OwnerID       int64
	Label         string
}

// mcpAuditHook returns a callback that writes one audit entry per MCP tool
// invocation, attributing it to the token's owning user. MCP requests carry
// no browser session, so this is the only record of who (via which token) ran
// what tool against a container — without it a leaked/expired-soon token's
// actions are invisible beyond a last-used-at timestamp.
func (a *App) mcpAuditHook(tok mcpTokenResolved) func(toolName string, args json.RawMessage) {
	actor := "mcp-token#" + strconv.FormatInt(tok.ID, 10)
	if u, err := a.db.UserByID(tok.OwnerID); err == nil && u != nil {
		actor = targetName(u) + " (mcp:" + tok.Label + ")"
	}
	target := tok.ContainerName
	return func(toolName string, args json.RawMessage) {
		preview := string(args)
		if len(preview) > 300 {
			preview = preview[:300] + "…"
		}
		a.db.Audit(actor, "mcp.tool."+toolName, target+" "+preview)
	}
}

// bearerTokenFromHeader extracts the token from an Authorization: Bearer <t>
// header. Returns "" when the header is absent or not a bearer token.
func bearerTokenFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// generateMCPToken returns a random hex token and its SHA-256 hash. The
// cleartext is returned to the user once at creation; only the hash is stored.
func generateMCPToken() (cleartext, hash string, err error) {
	b := make([]byte, mcpTokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	cleartext = hex.EncodeToString(b)
	hash = sha256Hex(cleartext)
	return cleartext, hash, nil
}

// --- management API (session-authenticated, CSRF-protected) ---

// mcpTokenList returns the caller's MCP tokens (admin sees all). An optional
// containerId query param narrows the result to one container.
//
// GET /api/mcp/tokens[?containerId=]
func (a *App) mcpTokenList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	containerID := strings.TrimSpace(r.URL.Query().Get("containerId"))
	var (
		tokens any
		err    error
	)
	if containerID != "" {
		tokens, err = a.db.MCPTokensForContainer(u.ID, u.Role == "admin", containerID)
	} else {
		tokens, err = a.db.MCPTokensForUser(u.ID, u.Role == "admin")
	}
	respond(w, tokens, err)
}

// mcpTokenCreate issues a new MCP token for a container the caller owns.
//
// POST /api/mcp/tokens  { containerId, label, expiresInHours? }
func (a *App) mcpTokenCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot create MCP tokens")
		return
	}
	var req struct {
		ContainerID    string `json:"containerId"`
		Label          string `json:"label"`
		ExpiresInHours int    `json:"expiresInHours"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.ContainerID = strings.TrimSpace(req.ContainerID)
	if req.ContainerID == "" {
		writeErr(w, http.StatusBadRequest, "containerId is required")
		return
	}
	if !a.containerOwnedBy(r.Context(), u, req.ContainerID) {
		writeErr(w, http.StatusForbidden, "container is not yours")
		return
	}
	// Cap the label length so the UI table stays readable.
	if len(req.Label) > 64 {
		req.Label = req.Label[:64]
	}
	cleartext, hash, err := generateMCPToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var expiresAt string
	if req.ExpiresInHours > 0 {
		expiresAt = time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour).Format(time.RFC3339)
	}
	containerName := a.docker.ContainerName(r.Context(), req.ContainerID)
	id, err := a.db.CreateMCPToken(u.ID, req.ContainerID, containerName, req.Label, cleartext, hash, expiresAt)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "mcp.token.create", containerName+"#"+req.Label)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          id,
		"token":       cleartext,
		"containerId": req.ContainerID,
		"label":       req.Label,
		"expiresAt":   expiresAt,
	})
}

// mcpTokenDelete revokes a token. Owners may delete their own tokens; admins
// may delete any.
//
// DELETE /api/mcp/tokens/{id}
func (a *App) mcpTokenDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	id := parseID(chi.URLParam(r, "id"))
	if id == 0 {
		writeErr(w, http.StatusBadRequest, "invalid token id")
		return
	}
	// Capture the token's container/label before deletion so the audit entry
	// says what was revoked (matching the mcp.token.create target format).
	target := ""
	if t, err := a.db.MCPTokenByID(id); err == nil {
		target = t.ContainerName + "#" + t.Label
	}
	if err := a.db.DeleteMCPToken(u.ID, u.Role == "admin", id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	a.record(r, "mcp.token.delete", target)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
