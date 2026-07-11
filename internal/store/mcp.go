package store

import (
	"database/sql"
	"errors"
	"time"
)

// MCPToken is a per-container access token that lets an external MCP client
// (Claude Code, Codex, Kimi, …) connect to exactly one container. The
// cleartext token is returned only at creation time; the database stores its
// SHA-256 hash.
type MCPToken struct {
	ID            int64  `json:"id"`
	Token         string `json:"token,omitempty"`
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
	OwnerID       int64  `json:"ownerId"`
	Owner         string `json:"owner,omitempty"`
	Label         string `json:"label"`
	CreatedAt     string `json:"createdAt"`
	LastUsedAt    string `json:"lastUsedAt,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
}

// Expired reports whether the token has passed its expiry. An empty ExpiresAt
// means the token never expires.
func (t MCPToken) Expired() bool {
	if t.ExpiresAt == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(ts)
}

// CreateMCPToken persists a new token. cleartext is the token returned to the
// client; tokenHash is the hex SHA-256 of it (used for fast lookup on incoming
// requests). expiresAt is an RFC3339 timestamp or "" for no expiry.
func (db *DB) CreateMCPToken(ownerID int64, containerID, containerName, label, cleartext, tokenHash, expiresAt string) (int64, error) {
	res, err := db.Exec(`insert into mcp_tokens(token_hash, token_plaintext, container_id, container_name, owner_id, label, created_at, expires_at)
		values(?,?,?,?,?,?,?,?)`,
		tokenHash, cleartext, containerID, containerName, ownerID, label, time.Now().Format(time.RFC3339), expiresAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MCPTokenByHash looks up a token by its SHA-256 hash. Used by the MCP
// endpoint to authenticate an incoming request. Returns sql.ErrNoRows when no
// token matches.
func (db *DB) MCPTokenByHash(tokenHash string) (MCPToken, error) {
	var t MCPToken
	err := db.QueryRow(`select id, token_plaintext, container_id, container_name, owner_id, label, created_at, last_used_at, expires_at
		from mcp_tokens where token_hash=?`, tokenHash).Scan(
		&t.ID, &t.Token, &t.ContainerID, &t.ContainerName, &t.OwnerID, &t.Label, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	if err != nil {
		return MCPToken{}, err
	}
	return t, nil
}

// MCPTokensForUser lists tokens. Admins see every token; others see only their own.
func (db *DB) MCPTokensForUser(userID int64, admin bool) ([]MCPToken, error) {
	q := `select t.id, t.token_plaintext, t.container_id, t.container_name, t.owner_id, t.label, t.created_at, t.last_used_at, t.expires_at, coalesce(nullif(u.display_name,''), u.username)
		from mcp_tokens t left join users u on u.id = t.owner_id`
	args := []any{}
	if !admin {
		q += ` where t.owner_id=?`
		args = append(args, userID)
	}
	q += ` order by t.created_at desc`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMCPTokens(rows)
}

// MCPTokensForContainer lists tokens for a single container. Admins see all
// tokens for the container; others see only their own.
func (db *DB) MCPTokensForContainer(userID int64, admin bool, containerID string) ([]MCPToken, error) {
	q := `select t.id, t.token_plaintext, t.container_id, t.container_name, t.owner_id, t.label, t.created_at, t.last_used_at, t.expires_at, coalesce(nullif(u.display_name,''), u.username)
		from mcp_tokens t left join users u on u.id = t.owner_id
		where t.container_id=?`
	args := []any{containerID}
	if !admin {
		q += ` and t.owner_id=?`
		args = append(args, userID)
	}
	q += ` order by t.created_at desc`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMCPTokens(rows)
}

func scanMCPTokens(rows *sql.Rows) ([]MCPToken, error) {
	var out []MCPToken
	for rows.Next() {
		var t MCPToken
		if err := rows.Scan(&t.ID, &t.Token, &t.ContainerID, &t.ContainerName, &t.OwnerID, &t.Label, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.Owner); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteMCPToken removes a token. Non-admins may only delete their own tokens;
// attempting to delete another user's token returns an error.
func (db *DB) DeleteMCPToken(userID int64, admin bool, tokenID int64) error {
	if admin {
		_, err := db.Exec(`delete from mcp_tokens where id=?`, tokenID)
		return err
	}
	res, err := db.Exec(`delete from mcp_tokens where id=? and owner_id=?`, tokenID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("token not found")
	}
	return nil
}

// MCPTokenTouch marks a token as just used (best-effort, never fails the request).
func (db *DB) MCPTokenTouch(tokenID int64) error {
	_, err := db.Exec(`update mcp_tokens set last_used_at=? where id=?`, time.Now().Format(time.RFC3339), tokenID)
	return err
}
