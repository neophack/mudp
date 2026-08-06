package server

import (
	"context"
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
	"mudp/internal/store"
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
	session := a.mcpHub.OpenSession(srv, r.Context(), tok.ContainerID)
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

// mcpStreamableHTTPLocal is the token-free counterpart to mcpStreamableHTTP:
// a caller already on the console's own network only needs the container id,
// no MCP token. Registered on the main listener only — the external
// (tunnel-facing) listener has no route for /mcp/local/... and 404s it, so
// this path never becomes reachable from the internet regardless of how the
// safe-network / remote-access settings are configured.
//
// POST /mcp/local/{containerId}
func (a *App) mcpStreamableHTTPLocal(w http.ResponseWriter, r *http.Request) {
	tok, ok := a.resolveMcpContainerDirect(w, r)
	if !ok {
		return
	}
	srv := mcp.NewContainerServer(r.Context(), a.docker, tok.ContainerID, tok.ContainerName)
	srv.SetAuditHook(a.mcpAuditHook(tok))
	srv.ServeHTTP(w, r)
}

// mcpSSELocal is the token-free counterpart to mcpSSE.
//
// GET /mcp/local/{containerId}/sse
func (a *App) mcpSSELocal(w http.ResponseWriter, r *http.Request) {
	tok, ok := a.resolveMcpContainerDirect(w, r)
	if !ok {
		return
	}
	srv := mcp.NewContainerServer(r.Context(), a.docker, tok.ContainerID, tok.ContainerName)
	srv.SetAuditHook(a.mcpAuditHook(tok))
	session := a.mcpHub.OpenSession(srv, r.Context(), tok.ContainerID)
	defer a.mcpHub.Close(session)
	base := "/mcp/local/" + chi.URLParam(r, "containerId") + "/messages"
	mcp.ServeSSE(w, r, session, base)
}

// mcpMessagesLocal is the token-free counterpart to mcpMessages.
//
// POST /mcp/local/{containerId}/messages?session=ID
func (a *App) mcpMessagesLocal(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.resolveMcpContainerDirect(w, r); !ok {
		return
	}
	a.mcpHub.ServeMessages(w, r)
}

// resolveMcpContainerDirect validates the {containerId} URL param and confirms
// the target is still a container mudp manages. Unlike resolveMcpContainer it
// checks no token at all: this route exists precisely so a caller already
// trusted enough to reach the console's own listener can skip minting one. It
// refuses outright on the external listener as a defense-in-depth check —
// remoteMCPRoutes never registers this handler, so isRemoteMCP should never be
// true here, but a route added carelessly in the future must not silently
// turn this into an unauthenticated internet-facing endpoint.
func (a *App) resolveMcpContainerDirect(w http.ResponseWriter, r *http.Request) (mcpTokenResolved, bool) {
	if isRemoteMCP(r) {
		writeErr(w, http.StatusNotFound, "not found")
		return mcpTokenResolved{}, false
	}
	containerID := strings.TrimSpace(chi.URLParam(r, "containerId"))
	if containerID == "" {
		writeErr(w, http.StatusBadRequest, "containerId required")
		return mcpTokenResolved{}, false
	}
	if owner := a.docker.ManagedOwner(r.Context(), containerID); owner == "" {
		writeErr(w, http.StatusNotFound, "container no longer managed")
		return mcpTokenResolved{}, false
	}
	name := a.docker.ContainerName(r.Context(), containerID)
	return mcpTokenResolved{ContainerID: containerID, ContainerName: name, Label: "local"}, true
}

// resolveMcpContainer validates the {token} URL param, loads the MCP token,
// confirms the target container is still managed by mudp, and stamps
// last-used. On failure it writes the HTTP error and returns ok=false.
func (a *App) resolveMcpContainer(w http.ResponseWriter, r *http.Request) (mcpTokenResolved, bool) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if token == "" {
		if isRemoteMCP(r) {
			a.recordMcpAttack(r, "no token supplied")
		}
		writeErr(w, http.StatusUnauthorized, "token required")
		return mcpTokenResolved{}, false
	}
	tok, err := a.resolveMCPToken(token)
	if err != nil {
		if isRemoteMCP(r) {
			a.recordMcpAttack(r, "invalid or expired token")
		}
		writeErr(w, http.StatusUnauthorized, "invalid or expired token")
		return mcpTokenResolved{}, false
	}
	if owner := a.docker.ManagedOwner(r.Context(), tok.ContainerID); owner == "" {
		if isRemoteMCP(r) {
			a.recordMcpAttack(r, "container no longer managed")
		}
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
		if isRemoteMCP(r) {
			a.recordMcpAttack(r, "token owner no longer permitted")
		}
		writeErr(w, http.StatusUnauthorized, "invalid or expired token")
		return mcpTokenResolved{}, false
	}
	// A token that works on the LAN does not automatically work from the
	// internet: over the external listener the container must also sit on the
	// administrator's safe network, and the request must also carry the
	// token's external key as an Authorization: Bearer header. That header is
	// a secret separate from the token embedded in this URL — a URL that
	// leaks through a tunnel's or proxy's access log cannot by itself
	// authenticate a remote request. See mcp_remote.go.
	if isRemoteMCP(r) {
		if ok, reason := a.remoteMCPAllowed(r.Context(), tok.ContainerID); !ok {
			a.recordMcpAttack(r, reason)
			writeErr(w, http.StatusForbidden, reason)
			return mcpTokenResolved{}, false
		}
		if tok.externalKeyHash == "" {
			a.recordMcpAttack(r, "no external key generated for token")
			writeErr(w, http.StatusForbidden, "external access requires generating an external key for this token")
			return mcpTokenResolved{}, false
		}
		if hdr := bearerTokenFromHeader(r); hdr == "" || sha256Hex(hdr) != tok.externalKeyHash {
			a.recordMcpAttack(r, "invalid or missing external key")
			writeErr(w, http.StatusUnauthorized, "invalid or missing external key")
			return mcpTokenResolved{}, false
		}
		// Capture the caller's origin so each tool call this request dispatches is
		// plotted on the Security map as a green "access" dot. Only remote
		// requests carry geo: a LAN caller is the operator, not an access worth
		// plotting.
		tok.client = a.mcpClientFromRequest(r)
	}
	_ = a.db.MCPTokenTouch(tok.ID)
	return tok, true
}

// resolveMCPToken hashes the cleartext token from the URL and looks it up.
func (a *App) resolveMCPToken(cleartext string) (mcpTokenResolved, error) {
	hash := sha256Hex(cleartext)
	tok, err := a.db.MCPTokenByHash(hash)
	if err != nil {
		return mcpTokenResolved{}, err
	}
	if tok.Expired() {
		return mcpTokenResolved{}, errors.New("token expired")
	}
	return mcpTokenResolved{ID: tok.ID, ContainerID: tok.ContainerID, ContainerName: tok.ContainerName, OwnerID: tok.OwnerID, Label: tok.Label, externalKeyHash: tok.ExternalKeyHash}, nil
}

type mcpTokenResolved struct {
	ID            int64
	ContainerID   string
	ContainerName string
	OwnerID       int64
	Label         string
	// externalKeyHash is the token's stored external-key hash, carried from
	// resolveMCPToken so resolveMcpContainer can verify the Authorization
	// header on a remote request without a second database round trip.
	externalKeyHash string
	// client is the resolved origin of a remote MCP request (IP + geo), captured
	// at resolve time and carried into the audit/usage hook so each tool call is
	// plotted on the Security map. Empty for LAN requests — only the external
	// listener populates it.
	client mcpClientInfo
}

// mcpClientInfo is the lightweight origin snapshot stored on a resolved token,
// reused for both access (green) and attack (yellow/red) map dots.
type mcpClientInfo struct {
	IP          string
	Country     string
	CountryCode string
	Region      string
	City        string
	Latitude    float64
	Longitude   float64
	ISP         string
	Timezone    string
	// SourceKind is "extranet" (public IP) or "intranet" (private/loopback),
	// shown in the Security page's source column so an admin can tell at a glance
	// whether a request reached the tunnel from the public internet or a LAN.
	SourceKind string
	// Device is the client class ("desktop"/"mobile"/"tablet"/"bot"), from
	// CF-Device-Type when behind a tunnel, else parsed from the User-Agent.
	Device string
}

// mcpAuditHook returns a callback that writes one audit entry per MCP tool
// invocation, attributing it to the token's owning user. MCP requests carry
// no browser session, so this is the only record of who (via which token) ran
// what tool against a container — without it a leaked/expired-soon token's
// actions are invisible beyond a last-used-at timestamp.
//
// It also writes a structured usage row (mcp_usage_logs) that powers the LOG
// dialog on each token; the audit_log row is kept for the cross-cutting
// Activity Log and for compatibility.
func (a *App) mcpAuditHook(tok mcpTokenResolved) func(toolName string, args json.RawMessage) {
	actor := "mcp-token#" + strconv.FormatInt(tok.ID, 10)
	if tok.ID == 0 {
		// Token-free local access carries no owning user or token row at all.
		actor = "mcp-local"
	} else if u, err := a.db.UserByID(tok.OwnerID); err == nil && u != nil {
		actor = targetName(u) + " (mcp:" + tok.Label + ")"
	}
	target := tok.ContainerName
	return func(toolName string, args json.RawMessage) {
		preview := string(args)
		if len(preview) > 300 {
			preview = preview[:300] + "…"
		}
		a.db.Audit(actor, "mcp.tool."+toolName, target+" "+preview)
		// Structured per-call record for the token's LOG dialog. Best-effort;
		// RecordMCPUsage never returns an error so a logging miss is silent.
		a.db.RecordMCPUsage(store.MCPUsageLog{
			TokenID:       tok.ID,
			OwnerID:       tok.OwnerID,
			ContainerID:   tok.ContainerID,
			ContainerName: tok.ContainerName,
			TokenLabel:    tok.Label,
			Tool:          toolName,
			ArgsPreview:   preview,
			// Carry the remote caller's origin so the Security map plots this
			// call as a green access dot. Empty for LAN calls (no geo captured).
			IP:          tok.client.IP,
			Country:     tok.client.Country,
			CountryCode: tok.client.CountryCode,
			Region:      tok.client.Region,
			City:        tok.client.City,
			Latitude:    tok.client.Latitude,
			Longitude:   tok.client.Longitude,
			ISP:         tok.client.ISP,
			Timezone:    tok.client.Timezone,
			SourceKind:  tok.client.SourceKind,
			Device:      tok.client.Device,
		})
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
// Each token is tagged with onSafeNetwork so the console can show an external
// link only when the container actually sits on the administrator's safe
// network — otherwise the link would point at a URL the safe-network gate
// rejects at runtime. The check is per-container, so several tokens on one
// container are inspected once.
//
// GET /api/mcp/tokens[?containerId=]
func (a *App) mcpTokenList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	containerID := strings.TrimSpace(r.URL.Query().Get("containerId"))
	var (
		tokens []store.MCPToken
		err    error
	)
	if containerID != "" {
		tokens, err = a.db.MCPTokensForContainer(u.ID, u.Role == "admin", containerID)
	} else {
		tokens, err = a.db.MCPTokensForUser(u.ID, u.Role == "admin")
	}
	if err != nil {
		respond(w, tokens, err)
		return
	}
	a.annotateSafeNetwork(r.Context(), tokens)
	a.annotateInUse(tokens)
	respond(w, tokens, nil)
}

// annotateSafeNetwork stamps each token's OnSafeNetwork when external MCP
// access is published. It inspects each distinct container once and caches the
// result, so a container carrying several tokens does one Docker call. When no
// safe network is configured every token keeps OnSafeNetwork=false, leaving the
// console to show only the LAN link.
func (a *App) annotateSafeNetwork(ctx context.Context, tokens []store.MCPToken) {
	cfg, err := a.db.MCPRemoteConfig()
	if err != nil || !cfg.Public() {
		return
	}
	reached := make(map[string]bool, len(tokens))
	for i := range tokens {
		cid := tokens[i].ContainerID
		if cid == "" {
			continue
		}
		on, ok := reached[cid]
		if !ok {
			on = a.containerOnSafeNetwork(ctx, cid, cfg)
			reached[cid] = on
		}
		tokens[i].OnSafeNetwork = on
	}
}

// annotateInUse stamps each token's InUse from the SSE hub's live session map.
// A token is "in use" when at least one MCP client holds an open SSE stream to
// its container. The hub lookup is O(sessions) and runs once per distinct
// container, so several tokens on one container do one pass. Streamable-HTTP
// clients have no persistent session and therefore never read as in use.
func (a *App) annotateInUse(tokens []store.MCPToken) {
	if a.mcpHub == nil {
		return
	}
	seen := make(map[string]bool, len(tokens))
	for i := range tokens {
		cid := tokens[i].ContainerID
		if cid == "" {
			continue
		}
		if _, ok := seen[cid]; !ok {
			seen[cid] = a.mcpHub.ActiveForContainer(cid) > 0
		}
		tokens[i].InUse = seen[cid]
	}
}

// mcpTokenUsage returns a token's recent tool-call history for the LOG dialog.
// Owners read their own tokens; admins read any. The owner scoping is enforced
// server-side (the filter carries OwnerID for non-admins) so a user cannot read
// another user's usage by guessing a token id.
//
// GET /api/mcp/tokens/{id}/usage
func (a *App) mcpTokenUsage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id := parseID(chi.URLParam(r, "id"))
	if id == 0 {
		writeErr(w, http.StatusBadRequest, "invalid token id")
		return
	}
	f := store.MCPUsageFilter{TokenID: id}
	if u.Role != "admin" {
		f.OwnerID = u.ID
	}
	entries, err := a.db.MCPUsageLogs(f)
	// A non-admin whose filter matched no rows (because the token is not theirs)
	// gets an empty list rather than a 403, so the existence of others' tokens
	// is not leaked.
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []store.MCPUsageLog{}
	}
	writeJSON(w, http.StatusOK, entries)
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

// mcpTokenRotate mints a fresh cleartext for an existing token and stores its
// hash in place of the old one, so the old cleartext stops authenticating the
// instant this returns — the container binding, label, and expiry are kept.
// Same ownership rule as delete: owners rotate their own tokens, admins rotate
// any.
//
// POST /api/mcp/tokens/{id}/rotate
func (a *App) mcpTokenRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot rotate MCP tokens")
		return
	}
	id := parseID(chi.URLParam(r, "id"))
	if id == 0 {
		writeErr(w, http.StatusBadRequest, "invalid token id")
		return
	}
	tok, err := a.db.MCPTokenByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	cleartext, hash, err := generateMCPToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.RotateMCPToken(u.ID, u.Role == "admin", id, cleartext, hash); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	a.record(r, "mcp.token.rotate", tok.ContainerName+"#"+tok.Label)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        id,
		"token":     cleartext,
		"label":     tok.Label,
		"expiresAt": tok.ExpiresAt,
	})
}

// mcpTokenRotateExternal mints a fresh external key for a token — the
// credential sent as an Authorization: Bearer header on the external (remote)
// MCP listener. Unlike mcpTokenRotate this never touches the token embedded
// in the token's /mcp/{token} URL, so LAN clients keep working unchanged; a
// token with no external key generated yet is simply refused on the remote
// listener (see resolveMcpContainer). Same ownership rule as mcpTokenRotate.
//
// POST /api/mcp/tokens/{id}/rotate-external
func (a *App) mcpTokenRotateExternal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := currentUser(r)
	if !canMutate(u) {
		writeErr(w, http.StatusForbidden, "read-only role cannot rotate MCP tokens")
		return
	}
	id := parseID(chi.URLParam(r, "id"))
	if id == 0 {
		writeErr(w, http.StatusBadRequest, "invalid token id")
		return
	}
	tok, err := a.db.MCPTokenByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	cleartext, hash, err := generateMCPToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.db.RotateMCPExternalKey(u.ID, u.Role == "admin", id, cleartext, hash); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	a.record(r, "mcp.token.rotateExternal", tok.ContainerName+"#"+tok.Label)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          id,
		"externalKey": cleartext,
		"label":       tok.Label,
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
