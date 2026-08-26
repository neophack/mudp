package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"mudp/internal/auth"
)

type DB struct {
	*sql.DB
}

type User struct {
	ID                    int64    `json:"id"`
	Username              string   `json:"username"`
	DisplayName           string   `json:"displayName,omitempty"`
	Role                  string   `json:"role"`
	Groups                []string `json:"groups,omitempty"`
	PortPrefix            int      `json:"portPrefix"`
	CreatedAt             string   `json:"createdAt"`
	LastLoginAt           *string  `json:"lastLoginAt,omitempty"`
	Disabled              bool     `json:"disabled"`
	ContainerCap          int      `json:"containerCap"`
	NetdiskQuotaBytes     int64    `json:"netdiskQuotaBytes"`
	FeishuOpenID          string   `json:"feishuOpenId,omitempty"`
	FeishuAvatar          string   `json:"feishuAvatar,omitempty"`
	FeishuEmail           string   `json:"feishuEmail,omitempty"`
	FeishuEnterpriseEmail string   `json:"feishuEnterpriseEmail,omitempty"`
	FeishuMobile          string   `json:"feishuMobile,omitempty"`
	FeishuTenantKey       string   `json:"feishuTenantKey,omitempty"`
	FeishuTenantName      string   `json:"feishuTenantName,omitempty"`
	FeishuDepartment      string   `json:"feishuDepartment,omitempty"`
	Comment               string   `json:"comment,omitempty"`
	Language              string   `json:"language,omitempty"`
	// PinyinName is derived from the Feishu profile name: Han characters are
	// converted to tone-free pinyin, non-Han characters (e.g. a Latin name or
	// initials) pass through unchanged, and all spaces are stripped. Empty for
	// users without a Feishu profile. See Pinyinize.
	PinyinName string `json:"pinyinName,omitempty"`
	// SharedDiskReadWrite is the user's own self-service preference for their
	// shared-disk (共享盘) subfolder: false (the default) keeps it read-only
	// wherever it is mounted, true makes it writable — in every container
	// that mounts the shared disk, whoever creates it, not just the owner's
	// own. New containers pick this up at creation time; already-running
	// containers keep whatever mount they were created with. An admin's own
	// container always gets read-write on every folder regardless of this
	// setting — see sharedDiskMountsFor.
	SharedDiskReadWrite bool `json:"sharedDiskReadWrite"`
}

// ValidRole reports whether r is one of the supported RBAC roles.
func ValidRole(r string) bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleHelpdesk, RoleReadonly, RoleUser:
		return true
	}
	return false
}

// MinPasswordLength is the floor for any password the server hashes. The login
// endpoint is rate limited per client address, but that only slows an online
// attack — it does nothing for a credential that is guessable in a handful of
// tries. Raising this floor is safe; lowering it weakens every account.
const MinPasswordLength = 10

// ValidatePassword rejects a password that is too weak to sit on an
// internet-facing login form. It is enforced in the store rather than in each
// handler so no future call site can bypass it.
func ValidatePassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	return nil
}

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleHelpdesk = "helpdesk"
	RoleReadonly = "readonly"
	RoleUser     = "user"
)

// DefaultUserCapacity is the maximum number of user accounts allowed when the
// administrator has not configured a different limit.
const DefaultUserCapacity = 50

// ErrUserCapacityFull is returned by CreateUser and CreateFeishuUserWithProfile
// when the number of existing users has already reached the configured limit.
var ErrUserCapacityFull = errors.New("user capacity full")

// PendingGroup is the name of the holding group new Feishu users land in until
// an admin assigns them to a real group.
const PendingGroup = "pending"

// DefaultUserGroup is the default business group used for approved local users
// and as the approval target for Feishu sign-ups.
const DefaultUserGroup = "users"

type Group struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	NetdiskPath    string `json:"netdiskPath,omitempty"`
	BackupPath     string `json:"backupPath,omitempty"`
	SharedDiskPath string `json:"sharedDiskPath,omitempty"`
	Language       string `json:"language,omitempty"`
}

// Notification is an in-app message delivered to a single user.
type Notification struct {
	ID        int64          `json:"id"`
	UserID    int64          `json:"userId"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data"`
	Read      bool           `json:"read"`
	CreatedAt string         `json:"createdAt"`
}

const (
	NotificationPendingUser     = "pending_user"
	NotificationUserApproved    = "user_approved"
	NotificationUserDeactivated = "user_deactivated"
	NotificationSystemAlert     = "system_alert"
	NotificationProcessFinished = "process_finished"
)

type Image struct {
	ID          int64        `json:"id"`
	DisplayName string       `json:"name"`
	DockerRef   string       `json:"dockerRef"`
	SourceRef   string       `json:"sourceRef"`
	Groups      []string     `json:"groups,omitempty"`
	CreatedAt   string       `json:"createdAt"`
	Preset      *ImagePreset `json:"preset,omitempty"`
}

type ResourceSample struct {
	ID          int64   `json:"id,omitempty"`
	UserID      int64   `json:"userId"`
	Username    string  `json:"username"`
	ContainerID string  `json:"containerId"`
	Container   string  `json:"container"`
	CPUPercent  float64 `json:"cpuPct"`
	MemoryMB    float64 `json:"memMb"`
	DiskMB      float64 `json:"diskMb"`
	GPUPercent  float64 `json:"gpuPct"`
	CreatedAt   string  `json:"createdAt"`
}

type NetdiskShare struct {
	Token        string   `json:"token"`
	OwnerID      int64    `json:"ownerId"`
	Owner        string   `json:"owner,omitempty"`
	Name         string   `json:"name"`
	Paths        []string `json:"paths"`
	CreatedAt    string   `json:"createdAt"`
	ExpiresAt    string   `json:"expiresAt,omitempty"`
	Permanent    bool     `json:"permanent"`
	Expired      bool     `json:"expired"`
	PasswordHash string   `json:"-"`
	HasPassword  bool     `json:"hasPassword"`
	// Password holds the cleartext extraction code so the owner (or an admin)
	// can view it again later, mirroring mcp_tokens.token_plaintext. It is only
	// populated by the owner/admin list queries, never on the public share path.
	Password string `json:"password,omitempty"`
}

// defaultMaxOpenConns is the connection-pool cap Open() applies and Migrate()
// restores after temporarily narrowing the pool to a single connection.
const defaultMaxOpenConns = 8

func Open(path string) (*DB, error) {
	// Per-connection pragmas must go in the DSN: database/sql pools up to
	// MaxOpenConns connections and a one-shot Exec would apply them only to
	// whichever connection ran it. modernc.org/sqlite applies each _pragma to
	// every connection it opens. Without this, foreign_keys (and with it
	// `on delete cascade`) silently stays off on most pooled connections.
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	dsn := path + sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(1000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(defaultMaxOpenConns)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &DB{DB: db}, nil
}

// execIgnoring runs stmt and returns nil when the error message contains any
// of the ignored fragments. It is used for idempotent schema migrations where
// the target object may already exist.
func execIgnoring(db executor, stmt string, ignore ...string) error {
	_, err := db.Exec(stmt)
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, frag := range ignore {
		if strings.Contains(msg, frag) {
			return nil
		}
	}
	return err
}

// Common SQLite error message fragments treated as idempotent during migration.
const (
	sqliteDuplicateColumn = "duplicate column"
	sqliteDuplicateIndex  = "index .* already exists"
)

// schemaVersion is bumped whenever a new migration is added. New databases are
// created directly at this version; existing databases are migrated forward.
const schemaVersion = 45

// executor is implemented by both *sql.DB and *sql.Tx.
type executor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// migration is a single schema change.
type migration struct {
	version int
	name    string
	apply   func(executor) error
}

var migrations = []migration{
	{1, "create initial tables", migrateCreateInitialTables},
	{2, "add users.feishu_open_id", migrateAddFeishuOpenID},
	{3, "add users.port_prefix", migrateAddPortPrefix},
	{4, "add groups.netdisk_path", migrateAddGroupNetdiskPath},
	{5, "add netdisk_shares.expires_at", migrateAddShareExpiresAt},
	{6, "add netdisk_shares.permanent", migrateAddSharePermanent},
	{7, "add users.comment", migrateAddUserComment},
	{8, "add images.preset_json", migrateAddImagePreset},
	{9, "create indexes", migrateCreateIndexes},
	{10, "widen users.role constraint", migrateWidenRoleConstraint},
	{11, "create schema_version table", migrateCreateSchemaVersion},
	{12, "add users.netdisk_quota_bytes", migrateAddNetdiskQuotaBytes},
	{13, "add netdisk_shares.password_hash", migrateAddSharePasswordHash},
	{14, "create mcp_tokens", migrateCreateMCPTokens},
	{15, "add mcp_tokens.token_plaintext", migrateAddMCPTokenPlaintext},
	{16, "add unique port_prefix index", migrateAddPortPrefixUniqueIndex},
	{17, "add users.display_name", migrateAddUserDisplayName},
	{18, "create notifications table", migrateCreateNotifications},
	{19, "delete orphaned mcp_tokens", migrateDeleteOrphanedMCPTokens},
	{20, "add netdisk_shares.password", migrateAddSharePassword},
	{21, "add groups.backup_path", migrateAddGroupBackupPath},
	{22, "create backup_schedule", migrateCreateBackupSchedule},
	{23, "add users.language", migrateAddUserLanguage},
	{24, "add groups.language", migrateAddGroupLanguage},
	{25, "revoke derivable feishu passwords", migrateRevokeFeishuDerivedPasswords},
	{26, "create group_networks", migrateCreateGroupNetworks},
	{27, "create port_forwards", migrateCreatePortForwards},
	{28, "create access_logs", migrateCreateAccessLogs},
	{29, "extend access_logs (vpn/client signals)", migrateExtendAccessLogs},
	{30, "create mcp_usage_logs", migrateCreateMCPUsageLogs},
	{31, "create mcp_attack_logs", migrateCreateMCPAttackLogs},
	{32, "widen mcp_usage_logs (geo/client)", migrateWidenMCPUsageLogs},
	{33, "add port_forwards.require_login", migrateAddPortForwardRequireLogin},
	{34, "add images.env_seq", migrateAddImageEnvSeq},
	{35, "extend access_logs (public_ip)", migrateExtendAccessLogsPublicIP},
	{36, "widen mcp_logs (timezone/source_kind)", migrateWidenMCPLogs},
	{37, "add users.feishu_profile_columns", migrateAddFeishuProfileColumns},
	{38, "add users.pinyin_name", migrateAddUserPinyinName},
	{39, "add groups.shared_disk_path", migrateAddGroupSharedDiskPath},
	{40, "add users.shared_disk_read_write", migrateAddUserSharedDiskReadWrite},
	{41, "add mcp_tokens external key columns", migrateAddMCPTokenExternalKey},
	{42, "create process_watches", migrateCreateProcessWatches},
	{43, "create error_events", migrateCreateErrorEvents},
	{44, "drop users.feishu_webhook", migrateDropUserFeishuWebhook},
	{45, "create feishu_messages", migrateCreateFeishuMessages},
}

// AllowedTenantKey returns the configured Feishu tenant key that users must
// belong to in order to log in or sign up via Feishu SSO. An empty value means
// any tenant is allowed.
func (db *DB) AllowedTenantKey() (string, error) {
	return db.Setting("allowed_tenant_key")
}

// SaveAllowedTenantKey persists the Feishu tenant key restriction. An empty
// value clears the restriction.
func (db *DB) SaveAllowedTenantKey(tenantKey string) error {
	return db.SaveSetting("allowed_tenant_key", strings.TrimSpace(tenantKey))
}

// migrateRevokeFeishuDerivedPasswords clears password hashes that were derived
// from a user's Feishu open ID. Those accounts could be taken over through the
// ordinary password form by anyone who knew the open ID — which is recoverable
// from the username, since the username is the open ID with its separators
// stripped. Each row is checked against its own open ID rather than blanket
// cleared, so a password an administrator deliberately set for an SSO user is
// left alone.
func migrateRevokeFeishuDerivedPasswords(db executor) error {
	rows, err := db.Query(`select id, feishu_open_id, password_hash from users
		where feishu_open_id is not null and feishu_open_id != '' and password_hash != ''`)
	if err != nil {
		return err
	}
	type row struct {
		id     int64
		openID string
		hash   string
	}
	var affected []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.openID, &r.hash); err != nil {
			rows.Close()
			return err
		}
		affected = append(affected, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range affected {
		if !isUsablePasswordHash(r.hash) {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(r.hash), []byte(r.openID)) != nil {
			continue // a real, administrator-set password: keep it
		}
		if _, err := db.Exec(`update users set password_hash=? where id=?`, NoPasswordSentinel, r.id); err != nil {
			return err
		}
	}
	return nil
}

func migrateCreateInitialTables(db executor) error {
	stmts := []string{
		`create table if not exists users (
			id integer primary key autoincrement,
			username text not null unique,
			password_hash text not null,
			role text not null,
			disabled integer not null default 0,
			container_cap integer not null default 10,
			netdisk_quota_bytes integer not null default 0,
			port_prefix integer not null default 0,
			created_at text not null,
			last_login_at text,
			feishu_open_id text default '',
			feishu_avatar text default '',
			feishu_email text default '',
			feishu_enterprise_email text default '',
			feishu_mobile text default '',
			feishu_tenant_key text default '',
			feishu_tenant_name text default '',
			feishu_department text default '',
			comment text default '',
			display_name text default ''
		)`,
		`create table if not exists groups (
		id integer primary key autoincrement,
		name text not null unique,
		netdisk_path text not null default '',
		backup_path text not null default ''
	)`,
		`create table if not exists user_groups (
			user_id integer not null references users(id) on delete cascade,
			group_id integer not null references groups(id) on delete cascade,
			primary key (user_id, group_id)
		)`,
		`create table if not exists images (
			id integer primary key autoincrement,
			display_name text not null unique,
			docker_ref text not null unique,
			source_ref text not null,
			created_at text not null,
			preset_json text not null default ''
		)`,
		`create table if not exists group_images (
			group_id integer not null references groups(id) on delete cascade,
			image_id integer not null references images(id) on delete cascade,
			primary key (group_id, image_id)
		)`,
		`create table if not exists audit_logs (
			id integer primary key autoincrement,
			actor text not null,
			action text not null,
			target text not null,
			created_at text not null
		)`,
		`create table if not exists stacks (
			id integer primary key autoincrement,
			name text not null,
			owner_id integer not null references users(id) on delete cascade,
			compose_yaml text not null,
			env_json text not null default '{}',
			project_name text not null,
			created_at text not null,
			updated_at text not null,
			unique (owner_id, name)
		)`,
		`create table if not exists settings (
			key text primary key,
			value text not null
		)`,
		`create table if not exists resource_samples (
			id integer primary key autoincrement,
			user_id integer not null,
			username text not null,
			container_id text not null,
			container_name text not null,
			cpu_pct real not null default 0,
			mem_mb real not null default 0,
			disk_mb real not null default 0,
			gpu_pct real not null default 0,
			created_at text not null
		)`,
		`create table if not exists netdisk_shares (
			token text primary key,
			owner_id integer not null references users(id) on delete cascade,
			name text not null,
			paths_json text not null,
			created_at text not null,
			expires_at text not null default '',
			permanent integer not null default 0,
			password_hash text not null default '',
			password text not null default ''
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func migrateAddFeishuOpenID(db executor) error {
	return execIgnoring(db, `alter table users add column feishu_open_id text default ''`, sqliteDuplicateColumn)
}

func migrateAddPortPrefix(db executor) error {
	return execIgnoring(db, `alter table users add column port_prefix integer not null default 0`, sqliteDuplicateColumn)
}

func migrateAddGroupNetdiskPath(db executor) error {
	return execIgnoring(db, `alter table groups add column netdisk_path text not null default ''`, sqliteDuplicateColumn)
}

func migrateAddGroupBackupPath(db executor) error {
	return execIgnoring(db, `alter table groups add column backup_path text not null default ''`, sqliteDuplicateColumn)
}

func migrateAddGroupSharedDiskPath(db executor) error {
	return execIgnoring(db, `alter table groups add column shared_disk_path text not null default ''`, sqliteDuplicateColumn)
}

func migrateAddUserSharedDiskReadWrite(db executor) error {
	return execIgnoring(db, `alter table users add column shared_disk_read_write integer not null default 0`, sqliteDuplicateColumn)
}

// migrateCreateBackupSchedule creates the single-row backup schedule config
// table used by the daily scheduled-backup ticker. A row is seeded on first use.
func migrateCreateBackupSchedule(db executor) error {
	_, err := db.Exec(`create table if not exists backup_schedule (
		id integer primary key check (id = 1),
		hour integer not null default 2,
		minute integer not null default 0,
		enabled integer not null default 0,
		last_run_at text not null default ''
	)`)
	if err != nil {
		return err
	}
	// Seed the single config row if it doesn't exist.
	_, err = db.Exec(`insert or ignore into backup_schedule(id, hour, minute, enabled, last_run_at) values (1, 2, 0, 0, '')`)
	return err
}

func migrateAddUserLanguage(db executor) error {
	return execIgnoring(db, `alter table users add column language text default ''`, sqliteDuplicateColumn)
}

func migrateAddGroupLanguage(db executor) error {
	return execIgnoring(db, `alter table groups add column language text default ''`, sqliteDuplicateColumn)
}

func migrateAddShareExpiresAt(db executor) error {
	return execIgnoring(db, `alter table netdisk_shares add column expires_at text not null default ''`, sqliteDuplicateColumn)
}

func migrateAddSharePermanent(db executor) error {
	return execIgnoring(db, `alter table netdisk_shares add column permanent integer not null default 0`, sqliteDuplicateColumn)
}

func migrateAddUserComment(db executor) error {
	return execIgnoring(db, `alter table users add column comment text default ''`, sqliteDuplicateColumn)
}

func migrateAddUserDisplayName(db executor) error {
	if err := execIgnoring(db, `alter table users add column display_name text default ''`, sqliteDuplicateColumn); err != nil {
		return err
	}
	// Existing Feishu users kept their Chinese name in comment; copy it to the
	// new display_name column so the UI shows it immediately after upgrade.
	_, err := db.Exec(`update users set display_name=comment where feishu_open_id != '' and display_name = '' and comment != ''`)
	return err
}

func migrateCreateNotifications(db executor) error {
	stmts := []string{
		`create table if not exists notifications (
			id integer primary key autoincrement,
			user_id integer not null references users(id) on delete cascade,
			type text not null,
			title text not null,
			message text not null,
			data_json text not null default '{}',
			read integer not null default 0,
			created_at text not null
		)`,
		`create index if not exists idx_notifications_user_read on notifications(user_id, read, created_at desc)`,
		`create index if not exists idx_notifications_created on notifications(created_at desc)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func migrateAddNetdiskQuotaBytes(db executor) error {
	return execIgnoring(db, `alter table users add column netdisk_quota_bytes integer not null default 0`, sqliteDuplicateColumn)
}

func migrateAddSharePasswordHash(db executor) error {
	return execIgnoring(db, `alter table netdisk_shares add column password_hash text not null default ''`, sqliteDuplicateColumn)
}

// migrateAddSharePassword stores the cleartext extraction code alongside its
// bcrypt hash so the share owner (or an admin) can view the code again later;
// password_hash remains for verifying incoming requests.
func migrateAddSharePassword(db executor) error {
	return execIgnoring(db, `alter table netdisk_shares add column password text not null default ''`, sqliteDuplicateColumn)
}

func migrateAddImagePreset(db executor) error {
	return execIgnoring(db, `alter table images add column preset_json text not null default ''`, sqliteDuplicateColumn)
}

// migrateAddImageEnvSeq adds the per-image counter backing {{sequence}} preset env
// placeholders, so each container built from an image can get the next number.
func migrateAddImageEnvSeq(db executor) error {
	return execIgnoring(db, `alter table images add column env_seq integer not null default 0`, sqliteDuplicateColumn)
}

// migrateCreateMCPTokens creates the table backing per-container MCP access
// tokens. The cleartext token is stored (token_plaintext) so an admin or owner
// can view the connection config again later; token_hash remains for fast
// lookup on incoming requests.
func migrateCreateMCPTokens(db executor) error {
	stmts := []string{
		`create table if not exists mcp_tokens (
			id integer primary key autoincrement,
			token_hash text not null unique,
			token_plaintext text not null default '',
			container_id text not null,
			container_name text not null,
			owner_id integer not null references users(id) on delete cascade,
			label text not null default '',
			created_at text not null,
			last_used_at text not null default '',
			expires_at text not null default ''
		)`,
		`create index if not exists idx_mcp_tokens_container on mcp_tokens(container_id)`,
		`create index if not exists idx_mcp_tokens_owner on mcp_tokens(owner_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateAddMCPTokenPlaintext adds the cleartext column to databases created
// before it existed. Existing rows get an empty string (the token shown only at
// creation was never persisted); new tokens store the cleartext.
func migrateAddMCPTokenPlaintext(db executor) error {
	return execIgnoring(db, `alter table mcp_tokens add column token_plaintext text not null default ''`, sqliteDuplicateColumn)
}

// migrateAddMCPTokenExternalKey adds the credential sent as the
// Authorization: Bearer header on the external (remote) MCP listener. It is
// a separate secret from token_hash/token_plaintext — the one embedded in the
// /mcp/{token} URL — so a URL that leaks through a tunnel's access log cannot
// by itself authenticate a remote request, and rotating it never disturbs the
// URL that LAN clients already have configured. Empty until a user generates
// one for a token.
func migrateAddMCPTokenExternalKey(db executor) error {
	if err := execIgnoring(db, `alter table mcp_tokens add column external_key_hash text not null default ''`, sqliteDuplicateColumn); err != nil {
		return err
	}
	return execIgnoring(db, `alter table mcp_tokens add column external_key_plaintext text not null default ''`, sqliteDuplicateColumn)
}

// migrateDeleteOrphanedMCPTokens removes tokens whose owning user no longer
// exists. Such orphans accumulated while foreign_keys was only enabled on one
// pooled connection (so `on delete cascade` did not fire); they also crash the
// token list query, which LEFT JOINs users for the owner name.
func migrateDeleteOrphanedMCPTokens(db executor) error {
	_, err := db.Exec(`delete from mcp_tokens where owner_id not in (select id from users)`)
	return err
}

// migrateAddPortPrefixUniqueIndex adds a partial unique index so that two users
// cannot be assigned the same non-zero port prefix. This closes a race where
// concurrent CreateUser calls read the same max(port_prefix) and both insert
// the next value.
func migrateAddPortPrefixUniqueIndex(db executor) error {
	return execIgnoring(db, `create unique index if not exists idx_users_port_prefix on users(port_prefix) where port_prefix > 0`, sqliteDuplicateIndex)
}

func migrateCreateIndexes(db executor) error {
	stmts := []string{
		`create unique index if not exists idx_users_feishu_open_id on users(feishu_open_id) where feishu_open_id != ''`,
		`create index if not exists idx_audit_created on audit_logs(created_at desc)`,
		`create index if not exists idx_resource_samples_created on resource_samples(created_at desc)`,
		`create index if not exists idx_resource_samples_user_created on resource_samples(user_id, created_at desc)`,
		`create index if not exists idx_netdisk_shares_owner on netdisk_shares(owner_id, created_at desc)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateWidenRoleConstraint relaxes an older users.role CHECK that only allowed
// ('admin','user'). It is a no-op when the constraint is already permissive or
// absent. The rebuild preserves every column and row, including auto-increment
// continuity, by deriving the column list from the current table schema.
// migrateAddFeishuProfileColumns extends the users table with the extra profile
// fields fetched from Feishu OIDC so the dashboard can show a rich identity card.
func migrateAddFeishuProfileColumns(db executor) error {
	stmts := []string{
		`alter table users add column feishu_avatar text default ''`,
		`alter table users add column feishu_email text default ''`,
		`alter table users add column feishu_enterprise_email text default ''`,
		`alter table users add column feishu_mobile text default ''`,
		`alter table users add column feishu_tenant_key text default ''`,
		`alter table users add column feishu_tenant_name text default ''`,
		`alter table users add column feishu_department text default ''`,
	}
	for _, stmt := range stmts {
		if err := execIgnoring(db, stmt, sqliteDuplicateColumn); err != nil {
			return err
		}
	}
	return nil
}

// migrateAddUserPinyinName adds the pinyin_name column and backfills it for
// existing Feishu users from their current display name, using the same
// Pinyinize conversion applied on every future Feishu login.
func migrateAddUserPinyinName(db executor) error {
	if err := execIgnoring(db, `alter table users add column pinyin_name text default ''`, sqliteDuplicateColumn); err != nil {
		return err
	}
	rows, err := db.Query(`select id, display_name from users where feishu_open_id != '' and display_name != ''`)
	if err != nil {
		return err
	}
	type feishuNameRow struct {
		id   int64
		name string
	}
	var toUpdate []feishuNameRow
	for rows.Next() {
		var r feishuNameRow
		if err := rows.Scan(&r.id, &r.name); err != nil {
			rows.Close()
			return err
		}
		toUpdate = append(toUpdate, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, r := range toUpdate {
		if _, err := db.Exec(`update users set pinyin_name=? where id=?`, Pinyinize(r.name), r.id); err != nil {
			return err
		}
	}
	return nil
}

func migrateWidenRoleConstraint(db executor) error {
	var tableSQL string
	err := db.QueryRow(`select sql from sqlite_master where type='table' and name='users'`).Scan(&tableSQL)
	if err != nil {
		return err
	}
	// Only rebuild when the old restrictive CHECK is still present.
	if !strings.Contains(tableSQL, "check(role in ('admin','user'))") {
		return nil
	}
	// Build the new table definition from the current schema columns so that no
	// column can be accidentally dropped or re-created with a different default.
	rows, err := db.Query(`PRAGMA table_info(users)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name, typ string
		var cid, notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		col := name + " " + typ
		if notnull != 0 {
			col += " not null"
		}
		if dflt.Valid {
			col += " default " + dflt.String
		}
		if pk != 0 {
			col += " primary key autoincrement"
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("unable to read users table schema")
	}
	createStmt := "create table users_new (" + strings.Join(cols, ", ") + ")"
	steps := []string{
		`alter table users rename to users_old`,
		createStmt,
		`insert into users_new select * from users_old`,
		`drop table users_old`,
		`alter table users_new rename to users`,
	}
	for _, s := range steps {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrateCreateSchemaVersion(db executor) error {
	_, err := db.Exec(`create table if not exists schema_version (
		version integer primary key,
		migrated_at text not null
	)`)
	return err
}

// currentSchemaVersion returns the highest recorded migration version, or 0.
func (db *DB) currentSchemaVersion() (int, error) {
	var n int
	// The table may not exist on very old DBs before this migration system was
	// introduced; treat that as version 0 and let all migrations run.
	err := db.QueryRow(`select version from schema_version order by version desc limit 1`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// Migrate applies all pending schema migrations and ensures the admin account
// and pending group exist.
func (db *DB) Migrate(adminUser, adminPassword string) error {
	// Bootstrap: ensure the version tracking table exists before we query it.
	if _, err := db.Exec(`create table if not exists schema_version (
		version integer primary key,
		migrated_at text not null
	)`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	current, err := db.currentSchemaVersion()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	// Some migrations rebuild the users table via rename/drop. SQLite rewrites
	// foreign-key references to follow the rename, so after the rebuild tables
	// like mcp_tokens can end up pointing at the dropped users_old and any
	// later FK check fails with "no such table". foreign_keys is a
	// per-connection pragma and has no effect inside a transaction, so disable
	// it for the whole loop and pin the pool to one connection to guarantee the
	// pragma covers every statement a migration runs. Both are restored after.
	// In production migrations always run in order (10 before 14), so the FK
	// never actually dangles; this guard makes re-running older migrations safe.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`pragma foreign_keys = off`); err != nil {
		return fmt.Errorf("disable foreign_keys for migrations: %w", err)
	}
	migrateErr := db.runMigrations(current)
	if _, ferr := db.Exec(`pragma foreign_keys = on`); ferr != nil {
		// Re-enabling FK enforcement is best-effort; don't mask a real migrate
		// error with it.
		if migrateErr == nil {
			migrateErr = fmt.Errorf("re-enable foreign_keys: %w", ferr)
		}
	}
	db.SetMaxOpenConns(defaultMaxOpenConns)
	if migrateErr != nil {
		return migrateErr
	}

	// Only auto-create an admin when an explicit password is supplied. When the
	// password is empty the first-run setup wizard is responsible for creating
	// the administrator account.
	if adminPassword != "" {
		var n int
		if err := db.QueryRow(`select count(*) from users where role='admin'`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if err := db.CreateUser(adminUser, adminPassword, "admin", nil, 50, 0); err != nil {
				// Only reachable on a fresh install (an existing database already
				// has an admin and skips this branch), so naming the variable is
				// the fastest route to a fix.
				return fmt.Errorf("create bootstrap admin from MUDP_ADMIN_PASSWORD: %w", err)
			}
		}
	}
	return db.EnsureDefaultGroups()
}

// runMigrations applies every migration whose version is above current, each in
// its own transaction. The caller owns the foreign_keys pragma.
func (db *DB) runMigrations(current int) error {
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if err := m.apply(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d %q: %w", m.version, m.name, err)
		}
		if err := db.recordSchemaVersionTx(tx, m.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema version %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

// recordSchemaVersionTx records a migration version inside a transaction.
func (db *DB) recordSchemaVersionTx(tx *sql.Tx, version int) error {
	_, err := tx.Exec(`insert into schema_version(version, migrated_at) values(?, ?)
		on conflict(version) do update set migrated_at=excluded.migrated_at`,
		version, time.Now().Format(time.RFC3339))
	return err
}

func (db *DB) nextPortPrefix(tx *sql.Tx) (int, error) {
	var maxPrefix sql.NullInt64
	err := tx.QueryRow(`select max(port_prefix) from users`).Scan(&maxPrefix)
	if err != nil {
		return 0, err
	}
	next := 101
	if maxPrefix.Valid && int(maxPrefix.Int64) >= next {
		next = int(maxPrefix.Int64) + 1
	}
	if next > 655 {
		return 0, errors.New("no port prefix available (all slots 101-655 are taken)")
	}
	return next, nil
}

func (db *DB) CreateUser(username, password, role string, groupIDs []int64, containerCap int, quotaBytes int64) error {
	if containerCap <= 0 {
		containerCap = 10
	}
	if quotaBytes < 0 {
		quotaBytes = 0
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	// Retry if another concurrent CreateUser claims the same port prefix. The
	// unique partial index on port_prefix (where > 0) makes the conflict safe
	// to detect and retry. Other constraint failures (e.g. duplicate username)
	// are returned immediately so callers see the real cause.
	for i := 0; i < 10; i++ {
		err := db.createUserTx(username, string(hash), role, groupIDs, containerCap, quotaBytes)
		if err == nil {
			return nil
		}
		if !isPortPrefixConflict(err) {
			return err
		}
	}
	return errors.New("could not allocate a unique port prefix")
}

func (db *DB) createUserTx(username, passwordHash, role string, groupIDs []int64, containerCap int, quotaBytes int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := db.checkUserCapacity(tx); err != nil {
		return err
	}
	prefix, err := db.nextPortPrefix(tx)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`insert into users(username,password_hash,role,container_cap,netdisk_quota_bytes,port_prefix,created_at) values(?,?,?,?,?,?,?)`,
		username, passwordHash, role, containerCap, quotaBytes, prefix, time.Now().Format(time.RFC3339))
	if err != nil {
		return err
	}
	uid, _ := res.LastInsertId()
	gids := groupIDs
	if len(gids) == 0 {
		defaultGID, err := defaultUserGroupIDTx(tx)
		if err != nil {
			return err
		}
		gids = []int64{defaultGID}
	}
	for _, gid := range gids {
		if _, err := tx.Exec(`insert or ignore into user_groups(user_id,group_id) values(?,?)`, uid, gid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NoPasswordSentinel marks an account that has no password and may only
// authenticate through SSO. It is deliberately not a valid bcrypt hash, so a
// comparison against it can never succeed by accident; Authenticate also
// rejects it explicitly rather than relying on that.
const NoPasswordSentinel = "!"

// dummyPasswordHash is compared against on every login path that doesn't
// reach a real bcrypt check (unknown username, disabled account), so the
// response time for those cases matches a genuine wrong-password attempt.
// Without it, an attacker can enumerate valid usernames by timing logins:
// bcrypt costs tens of milliseconds, an early sql.ErrNoRows return costs
// microseconds.
var dummyPasswordHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("mudp-constant-time-auth-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return h
}()

// isUsablePasswordHash reports whether a stored hash can authenticate a
// password login. Anything that is not a bcrypt digest — the sentinel, an empty
// column, legacy junk — fails closed.
func isUsablePasswordHash(hash string) bool {
	return strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")
}

func (db *DB) Authenticate(username, password string) (*User, error) {
	var u User
	var hash string
	var disabled, sharedRW int
	err := db.QueryRow(`select id,username,display_name,password_hash,role,disabled,container_cap,netdisk_quota_bytes,port_prefix,created_at,last_login_at,feishu_open_id,feishu_avatar,feishu_email,feishu_enterprise_email,feishu_mobile,feishu_tenant_key,feishu_tenant_name,feishu_department,comment,pinyin_name,shared_disk_read_write from users where username=?`, username).
		Scan(&u.ID, &u.Username, &u.DisplayName, &hash, &u.Role, &disabled, &u.ContainerCap, &u.NetdiskQuotaBytes, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID, &u.FeishuAvatar, &u.FeishuEmail, &u.FeishuEnterpriseEmail, &u.FeishuMobile, &u.FeishuTenantKey, &u.FeishuTenantName, &u.FeishuDepartment, &u.Comment, &u.PinyinName, &sharedRW)
	u.SharedDiskReadWrite = sharedRW != 0
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		return nil, errors.New("invalid username or password")
	}
	if err != nil {
		return nil, err
	}
	if disabled != 0 {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		return nil, errors.New("user is disabled")
	}
	// SSO-only accounts carry no usable password. Burn the same bcrypt time as a
	// real attempt so the response does not reveal which accounts these are.
	if !isUsablePasswordHash(hash) {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		return nil, errors.New("invalid username or password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, errors.New("invalid username or password")
	}
	now := time.Now().Format(time.RFC3339)
	_, _ = db.Exec(`update users set last_login_at=? where id=?`, now, u.ID)
	u.Disabled = false
	u.Groups = db.UserGroupNames(u.ID)
	return &u, nil
}

func (db *DB) UserByID(id int64) (*User, error) {
	var u User
	var disabled, sharedRW int
	err := db.QueryRow(`select id,username,display_name,role,disabled,container_cap,netdisk_quota_bytes,port_prefix,created_at,last_login_at,feishu_open_id,feishu_avatar,feishu_email,feishu_enterprise_email,feishu_mobile,feishu_tenant_key,feishu_tenant_name,feishu_department,comment,pinyin_name,shared_disk_read_write from users where id=?`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &disabled, &u.ContainerCap, &u.NetdiskQuotaBytes, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID, &u.FeishuAvatar, &u.FeishuEmail, &u.FeishuEnterpriseEmail, &u.FeishuMobile, &u.FeishuTenantKey, &u.FeishuTenantName, &u.FeishuDepartment, &u.Comment, &u.PinyinName, &sharedRW)
	u.SharedDiskReadWrite = sharedRW != 0
	if err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	u.Groups = db.UserGroupNames(u.ID)
	return &u, nil
}

func (db *DB) UserByUsername(username string) (*User, error) {
	var id int64
	if err := db.QueryRow(`select id from users where username=?`, username).Scan(&id); err != nil {
		return nil, err
	}
	return db.UserByID(id)
}

// userCount returns the number of rows in the users table using the provided
// executor so the count can be evaluated inside a transaction.
func (db *DB) userCount(e executor) (int, error) {
	var n int
	if err := e.QueryRow(`select count(*) from users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// UserCount returns the total number of user accounts (including pending and
// disabled users).
func (db *DB) UserCount() (int, error) {
	return db.userCount(db)
}

// UserCapacityLimit returns the configured user capacity, defaulting to
// DefaultUserCapacity when no value has been stored or the stored value is
// invalid.
func (db *DB) UserCapacityLimit() (int, error) {
	return userCapacityLimitTx(db)
}

// userCapacityLimitTx is UserCapacityLimit evaluated against the given
// executor; see checkUserCapacity.
func userCapacityLimitTx(e executor) (int, error) {
	raw, err := getSettingTx(e, "user_capacity_limit")
	if err != nil {
		return DefaultUserCapacity, err
	}
	if raw == "" {
		return DefaultUserCapacity, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return DefaultUserCapacity, nil
	}
	return n, nil
}

// SaveUserCapacityLimit persists the maximum number of user accounts. Values
// below 1 are rejected.
func (db *DB) SaveUserCapacityLimit(limit int) error {
	if limit < 1 {
		return errors.New("capacity must be at least 1")
	}
	return db.setSetting("user_capacity_limit", strconv.Itoa(limit))
}

// checkUserCapacity returns ErrUserCapacityFull when the number of users has
// reached the configured limit. It is evaluated inside the provided
// transaction: both the limit lookup and the count must go through tx, not
// db, or they would each need a second pooled connection while the caller's
// transaction is still holding the first — a guaranteed deadlock once the
// pool is sized down to the transaction's own connection.
func (db *DB) checkUserCapacity(tx executor) error {
	limit, err := userCapacityLimitTx(tx)
	if err != nil {
		return err
	}
	count, err := db.userCount(tx)
	if err != nil {
		return err
	}
	if count >= limit {
		return ErrUserCapacityFull
	}
	return nil
}

func (db *DB) Users() ([]User, error) {
	rows, err := db.Query(`select id,username,display_name,role,disabled,container_cap,netdisk_quota_bytes,port_prefix,created_at,last_login_at,feishu_open_id,feishu_avatar,feishu_email,feishu_enterprise_email,feishu_mobile,feishu_tenant_key,feishu_tenant_name,feishu_department,comment,pinyin_name,shared_disk_read_write from users order by username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var disabled, sharedRW int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &disabled, &u.ContainerCap, &u.NetdiskQuotaBytes, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID, &u.FeishuAvatar, &u.FeishuEmail, &u.FeishuEnterpriseEmail, &u.FeishuMobile, &u.FeishuTenantKey, &u.FeishuTenantName, &u.FeishuDepartment, &u.Comment, &u.PinyinName, &sharedRW); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		u.SharedDiskReadWrite = sharedRW != 0
		u.Groups = db.UserGroupNames(u.ID)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (db *DB) Groups() ([]Group, error) {
	rows, err := db.Query(`select id,name,netdisk_path,backup_path,shared_disk_path,language from groups order by name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.NetdiskPath, &g.BackupPath, &g.SharedDiskPath, &g.Language); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func (db *DB) CreateGroup(name string) error {
	_, err := db.Exec(`insert into groups(name) values(?)`, name)
	return err
}

func (db *DB) UpdateGroupNetdiskPath(groupID int64, path string) error {
	_, err := db.Exec(`update groups set netdisk_path=? where id=?`, strings.TrimSpace(path), groupID)
	return err
}

func (db *DB) UpdateGroupBackupPath(groupID int64, path string) error {
	_, err := db.Exec(`update groups set backup_path=? where id=?`, strings.TrimSpace(path), groupID)
	return err
}

// UpdateGroupSharedDiskPath sets a group's shared-disk root, mirroring
// UpdateGroupNetdiskPath/UpdateGroupBackupPath.
func (db *DB) UpdateGroupSharedDiskPath(groupID int64, path string) error {
	_, err := db.Exec(`update groups set shared_disk_path=? where id=?`, strings.TrimSpace(path), groupID)
	return err
}

func (db *DB) UpdateGroupLanguage(groupID int64, language string) error {
	_, err := db.Exec(`update groups set language=? where id=?`, language, groupID)
	return err
}

func (db *DB) NetdiskPathForUser(userID int64) (string, error) {
	rows, err := db.Query(`select g.netdisk_path from groups g join user_groups ug on ug.group_id=g.id
		where ug.user_id=? and g.netdisk_path != '' order by g.id limit 1`, userID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return "", err
		}
		return strings.TrimSpace(path), nil
	}
	return "", nil
}

// BackupPathForUser resolves the configured backup disk root for a user's
// primary group, mirroring NetdiskPathForUser.
func (db *DB) BackupPathForUser(userID int64) (string, error) {
	rows, err := db.Query(`select g.backup_path from groups g join user_groups ug on ug.group_id=g.id
		where ug.user_id=? and g.backup_path != '' order by g.id limit 1`, userID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return "", err
		}
		return strings.TrimSpace(path), nil
	}
	return "", nil
}

// SharedDiskGroupForUser resolves the configured shared-disk root for a
// user's primary group, mirroring NetdiskPathForUser/BackupPathForUser, and
// returns the owning group's id alongside it so callers can enumerate that
// group's members (SharedDiskGroupMembers) without resolving twice. A zero id
// and empty path mean no group the user belongs to has a shared disk.
func (db *DB) SharedDiskGroupForUser(userID int64) (int64, string, error) {
	rows, err := db.Query(`select g.id, g.shared_disk_path from groups g join user_groups ug on ug.group_id=g.id
		where ug.user_id=? and g.shared_disk_path != '' order by g.id limit 1`, userID)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	if rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return 0, "", err
		}
		return id, strings.TrimSpace(path), nil
	}
	return 0, "", nil
}

// AnySharedDiskGroup returns the first group that has a shared disk
// configured, regardless of who belongs to it. Only admins resolve through
// this (see sharedDiskGroup): they are already allowed to manage every
// folder in a pool, so an admin who happens not to be a member of the group
// that owns the shared disk should still reach it rather than being told it
// isn't configured.
func (db *DB) AnySharedDiskGroup() (int64, string, error) {
	rows, err := db.Query(`select id, shared_disk_path from groups where shared_disk_path != '' order by id limit 1`)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	if rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return 0, "", err
		}
		return id, strings.TrimSpace(path), nil
	}
	return 0, "", nil
}

// BackupSchedule is the single-row daily backup schedule config.
type BackupSchedule struct {
	Hour      int    `json:"hour"`
	Minute    int    `json:"minute"`
	Enabled   bool   `json:"enabled"`
	LastRunAt string `json:"lastRunAt"`
}

func (db *DB) BackupScheduleGet() (BackupSchedule, error) {
	var s BackupSchedule
	var enabled int
	err := db.QueryRow(`select hour, minute, enabled, last_run_at from backup_schedule where id=1`).
		Scan(&s.Hour, &s.Minute, &enabled, &s.LastRunAt)
	if err != nil {
		return s, err
	}
	s.Enabled = enabled != 0
	return s, nil
}

func (db *DB) BackupScheduleSet(hour, minute int, enabled bool) error {
	if hour < 0 || hour > 23 {
		hour = 2
	}
	if minute < 0 || minute > 59 {
		minute = 0
	}
	e := 0
	if enabled {
		e = 1
	}
	_, err := db.Exec(`update backup_schedule set hour=?, minute=?, enabled=? where id=1`, hour, minute, e)
	return err
}

// BackupScheduleMarkRun records that the daily backup ran at the given RFC3339
// timestamp, so the ticker doesn't fire twice in the same minute.
func (db *DB) BackupScheduleMarkRun(ts string) error {
	_, err := db.Exec(`update backup_schedule set last_run_at=? where id=1`, ts)
	return err
}

func (db *DB) SaveImage(displayName, dockerRef, sourceRef string) error {
	_, err := db.Exec(`insert into images(display_name,docker_ref,source_ref,created_at) values(?,?,?,?)
		on conflict(display_name) do update set docker_ref=excluded.docker_ref, source_ref=excluded.source_ref`,
		displayName, dockerRef, sourceRef, time.Now().Format(time.RFC3339))
	return err
}

// RenameImage updates a catalog row's display name and docker ref in place, keyed
// by id (unlike SaveImage, which upserts by display_name and so would leave an
// orphaned row behind on a rename). The preset, group links, source ref, and
// created_at are all preserved. A UNIQUE constraint violation (the new name or
// ref colliding with another row) surfaces as the underlying error so callers
// can reject the rename.
func (db *DB) RenameImage(id int64, displayName, dockerRef string) error {
	res, err := db.Exec(`update images set display_name=?, docker_ref=? where id=?`, displayName, dockerRef, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("image %d not found", id)
	}
	return nil
}

func (db *DB) ImagesForUser(userID int64, admin bool) ([]Image, error) {
	q := `select distinct i.id,i.display_name,i.docker_ref,i.source_ref,i.created_at,i.preset_json
		from images i order by i.display_name`
	args := []any{}
	if !admin {
		// Images with no group assignment are public (visible to every activated
		// user). Images assigned to one or more groups are only visible to members
		// of those groups.
		q = `select distinct i.id,i.display_name,i.docker_ref,i.source_ref,i.created_at,i.preset_json
			from images i
			where not exists (select 1 from group_images gi where gi.image_id=i.id)
			   or exists (
				   select 1 from group_images gi
				   join user_groups ug on ug.group_id=gi.group_id
				   where gi.image_id=i.id and ug.user_id=?
			   )
			order by i.display_name`
		args = append(args, userID)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var imgs []Image
	for rows.Next() {
		var img Image
		var presetJSON string
		if err := rows.Scan(&img.ID, &img.DisplayName, &img.DockerRef, &img.SourceRef, &img.CreatedAt, &presetJSON); err != nil {
			return nil, err
		}
		if p, err := DecodePreset(presetJSON); err == nil {
			img.Preset = p
		}
		img.Groups = db.ImageGroupNames(img.ID)
		imgs = append(imgs, img)
	}
	return imgs, rows.Err()
}

// ImageByID returns the image with the given id, or an error when absent.
func (db *DB) ImageByID(id int64) (Image, error) {
	var img Image
	var presetJSON string
	err := db.QueryRow(`select id,display_name,docker_ref,source_ref,created_at,preset_json from images where id=?`, id).
		Scan(&img.ID, &img.DisplayName, &img.DockerRef, &img.SourceRef, &img.CreatedAt, &presetJSON)
	if err != nil {
		return Image{}, err
	}
	if p, err := DecodePreset(presetJSON); err == nil {
		img.Preset = p
	}
	img.Groups = db.ImageGroupNames(img.ID)
	return img, nil
}

func (db *DB) SetImageGroups(imageID int64, groupIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`delete from group_images where image_id=?`, imageID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(`insert or ignore into group_images(group_id,image_id) values(?,?)`, gid, imageID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) DeleteImage(imageID int64) error {
	_, err := db.Exec(`delete from images where id=?`, imageID)
	return err
}

// SetImagePreset stores the admin-defined default configuration for an image. A nil
// or empty preset clears the column so the image has no defaults.
func (db *DB) SetImagePreset(imageID int64, preset *ImagePreset) error {
	encoded, err := EncodePreset(preset)
	if err != nil {
		return err
	}
	res, err := db.Exec(`update images set preset_json=? where id=?`, encoded, imageID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("image %d not found", imageID)
	}
	return nil
}

// NextImageEnvSeq atomically increments and returns the image's env-var sequence
// counter, resolving a {{sequence}} preset placeholder to the next number (e.g.
// USER_CODE=0007) each time a container is built from the image.
func (db *DB) NextImageEnvSeq(imageID int64) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`update images set env_seq = env_seq + 1 where id=?`, imageID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("image %d not found", imageID)
	}
	var seq int64
	if err := tx.QueryRow(`select env_seq from images where id=?`, imageID).Scan(&seq); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func (db *DB) UserGroupNames(userID int64) []string {
	rows, err := db.Query(`select g.name from groups g join user_groups ug on ug.group_id=g.id where ug.user_id=? order by g.name`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func (db *DB) ImageGroupNames(imageID int64) []string {
	rows, err := db.Query(`select g.name from groups g join group_images gi on gi.group_id=g.id where gi.image_id=? order by g.name`, imageID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func (db *DB) ImageByDisplayNameForUser(displayName string, userID int64, admin bool) (Image, error) {
	imgs, err := db.ImagesForUser(userID, admin)
	if err != nil {
		return Image{}, err
	}
	for _, img := range imgs {
		if img.DisplayName == displayName {
			return img, nil
		}
	}
	return Image{}, fmt.Errorf("image %q is not visible", displayName)
}

// ImageByIDForUser is ImageByID narrowed to the same group-visibility rule as
// ImagesForUser: an image assigned to one or more groups is only visible to
// members of those groups (admins see everything). Any endpoint reachable by
// a non-admin that accepts a caller-supplied image id must resolve through
// this instead of the unguarded ImageByID.
func (db *DB) ImageByIDForUser(imageID, userID int64, admin bool) (Image, error) {
	imgs, err := db.ImagesForUser(userID, admin)
	if err != nil {
		return Image{}, err
	}
	for _, img := range imgs {
		if img.ID == imageID {
			return img, nil
		}
	}
	return Image{}, fmt.Errorf("image %d is not visible", imageID)
}

// EnsurePendingGroup creates the holding group used for new Feishu users if it
// does not yet exist. Safe to call repeatedly.
func (db *DB) EnsurePendingGroup() error {
	_, err := db.Exec(`insert or ignore into groups(name) values(?)`, PendingGroup)
	return err
}

// PendingGroupID returns the id of the pending group, creating it if needed.
func (db *DB) PendingGroupID() (int64, error) {
	if err := db.EnsurePendingGroup(); err != nil {
		return 0, err
	}
	var id int64
	err := db.QueryRow(`select id from groups where name=?`, PendingGroup).Scan(&id)
	return id, err
}

// EnsureDefaultGroups ensures both the pending holding group and the default
// business group exist. Safe to call repeatedly.
func (db *DB) EnsureDefaultGroups() error {
	_, err := db.Exec(`insert or ignore into groups(name) values(?), (?)`, PendingGroup, DefaultUserGroup)
	return err
}

// DefaultUserGroupID returns the id of the default users group, creating it if needed.
func (db *DB) DefaultUserGroupID() (int64, error) {
	if err := db.EnsureDefaultGroups(); err != nil {
		return 0, err
	}
	var id int64
	err := db.QueryRow(`select id from groups where name=?`, DefaultUserGroup).Scan(&id)
	return id, err
}

// UserByFeishu looks up a user by their Feishu open_id.
func (db *DB) UserByFeishu(openID string) (*User, error) {
	if openID == "" {
		return nil, sql.ErrNoRows
	}
	var u User
	var disabled, sharedRW int
	err := db.QueryRow(`select id,username,display_name,role,disabled,container_cap,port_prefix,created_at,last_login_at,feishu_open_id,feishu_avatar,feishu_email,feishu_enterprise_email,feishu_mobile,feishu_tenant_key,feishu_tenant_name,feishu_department,comment,pinyin_name,shared_disk_read_write from users where feishu_open_id=?`, openID).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &disabled, &u.ContainerCap, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID, &u.FeishuAvatar, &u.FeishuEmail, &u.FeishuEnterpriseEmail, &u.FeishuMobile, &u.FeishuTenantKey, &u.FeishuTenantName, &u.FeishuDepartment, &u.Comment, &u.PinyinName, &sharedRW)
	if err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	u.SharedDiskReadWrite = sharedRW != 0
	u.Groups = db.UserGroupNames(u.ID)
	return &u, nil
}

// CreateFeishuUserWithProfile registers a new user from a Feishu login using the
// full profile returned by Feishu. The user is placed in the pending group.
func (db *DB) CreateFeishuUserWithProfile(fu auth.FeishuUser) (*User, error) {
	if fu.OpenID == "" {
		return nil, errors.New("feishu open_id is required")
	}
	username := fu.Username()
	base := username
	for i := 0; i < 10; i++ {
		if i > 0 {
			username = fmt.Sprintf("%s-%d", base, i)
		}
		u, err := db.createFeishuUserWithUsernameTx(fu, username)
		if err == nil {
			return u, nil
		}
		if !isUsernameConflict(err) {
			return nil, err
		}
	}
	return nil, errors.New("could not allocate a unique username")
}

func (db *DB) createFeishuUserWithUsernameTx(fu auth.FeishuUser, username string) (*User, error) {
	// SSO accounts get no password at all. Hashing the open ID (as this once
	// did) made the password derivable from the username: Username() is the
	// open ID with its non-alphanumerics stripped, so "ou_a1b2" becomes the
	// account "oua1b2" whose password is "ou_a1b2". An open ID is an
	// identifier, not a secret — every app in the tenant sees it — so anyone
	// who knew one could log in through the ordinary password form and bypass
	// OAuth entirely.
	hash := NoPasswordSentinel
	tx, beginErr := db.Begin()
	if beginErr != nil {
		return nil, beginErr
	}
	defer tx.Rollback()
	if err := db.checkUserCapacity(tx); err != nil {
		return nil, err
	}
	var existing int
	if err := tx.QueryRow(`select count(*) from users where username=?`, username).Scan(&existing); err != nil {
		return nil, err
	}
	if existing != 0 {
		return nil, errors.New("duplicate username")
	}
	prefix, err := db.nextPortPrefix(tx)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(fu.Name)
	if displayName == "" {
		displayName = fu.Username()
	}
	pinyinName := Pinyinize(displayName)
	res, err := tx.Exec(`insert into users(username,password_hash,role,container_cap,port_prefix,created_at,feishu_open_id,feishu_avatar,feishu_email,feishu_enterprise_email,feishu_mobile,feishu_tenant_key,feishu_tenant_name,feishu_department,comment,display_name,pinyin_name) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		username, hash, "user", 10, prefix, time.Now().Format(time.RFC3339), fu.OpenID, fu.AvatarURL, fu.Email, fu.EnterpriseEmail, fu.Mobile, fu.TenantKey, fu.TenantName, fu.DepartmentName, fu.Comment, displayName, pinyinName)
	if err != nil {
		return nil, err
	}
	uid, _ := res.LastInsertId()
	pendingID, err := pendingGroupIDTx(tx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`insert or ignore into user_groups(user_id,group_id) values(?,?)`, uid, pendingID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.UserByID(uid)
}

// UpdateFeishuProfile refreshes the Feishu profile fields for an existing user.
// It is called on every Feishu login so the dashboard card stays current.
func (db *DB) UpdateFeishuProfile(id int64, fu auth.FeishuUser) error {
	if fu.OpenID == "" {
		return errors.New("feishu open_id is required")
	}
	displayName := strings.TrimSpace(fu.Name)
	if displayName == "" {
		displayName = fu.Username()
	}
	pinyinName := Pinyinize(displayName)
	_, err := db.Exec(`update users set feishu_open_id=?, feishu_avatar=?, feishu_email=?, feishu_enterprise_email=?, feishu_mobile=?, feishu_tenant_key=?, feishu_tenant_name=?, feishu_department=?, display_name=?, comment=?, pinyin_name=? where id=?`,
		fu.OpenID, fu.AvatarURL, fu.Email, fu.EnterpriseEmail, fu.Mobile, fu.TenantKey, fu.TenantName, fu.DepartmentName, displayName, fu.Comment, pinyinName, id)
	return err
}

func isPortPrefixConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed: users.port_prefix")
}

func isUsernameConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate username") ||
		strings.Contains(msg, "UNIQUE constraint failed: users.username")
}

func pendingGroupIDTx(tx *sql.Tx) (int64, error) {
	if _, err := tx.Exec(`insert or ignore into groups(name) values(?)`, PendingGroup); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(`select id from groups where name=?`, PendingGroup).Scan(&id)
	return id, err
}

func defaultUserGroupIDTx(tx *sql.Tx) (int64, error) {
	if _, err := tx.Exec(`insert or ignore into groups(name) values(?)`, DefaultUserGroup); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(`select id from groups where name=?`, DefaultUserGroup).Scan(&id)
	return id, err
}

// SetUserGroups replaces a user's group membership. Used by admins to approve
// Feishu users (remove from pending, add to real groups).
func (db *DB) SetUserGroups(userID int64, groupIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`delete from user_groups where user_id=?`, userID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(`insert or ignore into user_groups(user_id,group_id) values(?,?)`, userID, gid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FeishuConfig holds the OAuth credentials stored in settings (admin-managed).
type FeishuConfig struct {
	AppID     string `json:"appId"`
	AppSecret string `json:"appSecret"`
	Enabled   bool   `json:"enabled"`
}

func (db *DB) FeishuConfig() (FeishuConfig, error) {
	cfg := FeishuConfig{}
	rows, err := db.Query(`select key, value from settings where key in ('feishu_app_id','feishu_app_secret','feishu_enabled')`)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return cfg, err
		}
		switch key {
		case "feishu_app_id":
			cfg.AppID = value
		case "feishu_app_secret":
			cfg.AppSecret = value
		case "feishu_enabled":
			cfg.Enabled = value == "1" || value == "true"
		}
	}
	return cfg, rows.Err()
}

func (db *DB) SaveFeishuConfig(cfg FeishuConfig) error {
	enabled := "0"
	if cfg.Enabled {
		enabled = "1"
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	items := map[string]string{
		"feishu_app_id":     cfg.AppID,
		"feishu_app_secret": cfg.AppSecret,
		"feishu_enabled":    enabled,
	}
	for key, value := range items {
		if _, err := tx.Exec(`insert into settings(key, value) values(?, ?)
			on conflict(key) do update set value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------- Audit log ----------------

// AuditEntry is one recorded management action.
type AuditEntry struct {
	ID        int64  `json:"id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	CreatedAt string `json:"createdAt"`
}

// Audit records a management action. It never returns an error: audit logging
// is best-effort and must not derail the request it observes.
func (db *DB) Audit(actor, action, target string) {
	if actor == "" {
		actor = "system"
	}
	if action == "" {
		action = "action"
	}
	if target == "" {
		target = "-"
	}
	_, _ = db.Exec(`insert into audit_logs(actor, action, target, created_at) values(?,?,?,?)`,
		actor, action, target, time.Now().Format(time.RFC3339))
}

// PruneAuditLogs deletes audit entries older than the given time.
func (db *DB) PruneAuditLogs(before time.Time) error {
	_, err := db.Exec(`delete from audit_logs where created_at < ?`, before.Format(time.RFC3339))
	return err
}

// PruneResourceSamples deletes resource samples older than the given time.
func (db *DB) PruneResourceSamples(before time.Time) error {
	_, err := db.Exec(`delete from resource_samples where created_at < ?`, before.Format(time.RFC3339))
	return err
}

// Checkpoint runs a WAL truncate checkpoint to bound the WAL file size.
func (db *DB) Checkpoint() error {
	_, err := db.Exec(`pragma wal_checkpoint(TRUNCATE)`)
	return err
}

// AuditFilter narrows AuditList results. Empty fields match everything.
type AuditFilter struct {
	Actor  string
	Action string
	Target string
	Limit  int
}

// ---------------- Notifications ----------------

// CreateNotification persists a single notification.
func (db *DB) CreateNotification(n *Notification) error {
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	raw, err := json.Marshal(n.Data)
	if err != nil {
		return err
	}
	if n.CreatedAt == "" {
		n.CreatedAt = time.Now().Format(time.RFC3339)
	}
	res, err := db.Exec(`insert into notifications(user_id, type, title, message, data_json, read, created_at)
		values(?,?,?,?,?,?,?)`, n.UserID, n.Type, n.Title, n.Message, string(raw), 0, n.CreatedAt)
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	return nil
}

// NotificationsForUser returns the most recent notifications for a user.
func (db *DB) NotificationsForUser(userID int64, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.Query(`select id, user_id, type, title, message, data_json, read, created_at
		from notifications where user_id=? order by created_at desc limit ?`, userID, limit)
	if err != nil {
		return []Notification{}, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	unread := 0
	for rows.Next() {
		var n Notification
		var raw string
		var read int
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Message, &raw, &read, &n.CreatedAt); err != nil {
			return nil, 0, err
		}
		n.Read = read != 0
		_ = json.Unmarshal([]byte(raw), &n.Data)
		if !n.Read {
			unread++
		}
		out = append(out, n)
	}
	return out, unread, rows.Err()
}

// MarkNotificationsRead marks the given notification ids as read for a user.
func (db *DB) MarkNotificationsRead(userID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`update notifications set read=1 where user_id=? and id in (%s)`, strings.Join(placeholders, ","))
	_, err := db.Exec(q, args...)
	return err
}

// MarkAllNotificationsRead marks all notifications for a user as read.
func (db *DB) MarkAllNotificationsRead(userID int64) error {
	_, err := db.Exec(`update notifications set read=1 where user_id=?`, userID)
	return err
}

// DeleteNotifications deletes selected notifications for a user.
func (db *DB) DeleteNotifications(userID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`delete from notifications where user_id=? and id in (%s)`, strings.Join(placeholders, ","))
	_, err := db.Exec(q, args...)
	return err
}

// DeleteAllNotifications deletes all notifications for a user.
func (db *DB) DeleteAllNotifications(userID int64) error {
	_, err := db.Exec(`delete from notifications where user_id=?`, userID)
	return err
}

// NotifyAdmins creates a notification for every admin user.
func (db *DB) NotifyAdmins(n Notification) error {
	admins, err := db.Users()
	if err != nil {
		return err
	}
	for _, u := range admins {
		if u.Role != RoleAdmin {
			continue
		}
		clone := n
		clone.UserID = u.ID
		if err := db.CreateNotification(&clone); err != nil {
			return err
		}
	}
	return nil
}

// NotifyUser creates a notification for a single user.
func (db *DB) NotifyUser(userID int64, n Notification) error {
	n.UserID = userID
	return db.CreateNotification(&n)
}

// AuditList returns the most recent audit entries matching the filter.
func (db *DB) AuditList(f AuditFilter) ([]AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	q := `select id, actor, action, target, created_at from audit_logs`
	var (
		clauses []string
		args    []any
	)
	if f.Actor != "" {
		clauses = append(clauses, "actor = ?")
		args = append(args, f.Actor)
	}
	if f.Action != "" {
		clauses = append(clauses, "action = ?")
		args = append(args, f.Action)
	}
	if f.Target != "" {
		clauses = append(clauses, "target like ?")
		args = append(args, "%"+f.Target+"%")
	}
	if len(clauses) > 0 {
		q += " where " + strings.Join(clauses, " and ")
	}
	q += " order by id desc limit ?"
	args = append(args, f.Limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------- User admin updates ----------------

// UpdateUser mutates selected user fields. Zero-valued strings/ints are ignored
// (password/role left untouched); pass a non-empty value to change it.
func (db *DB) UpdateUser(id int64, password, role string, containerCap int, netdiskQuotaBytes *int64, disabled *bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if password != "" {
		// An empty password means "leave unchanged"; anything else is a real
		// credential and must clear the same bar as a new account.
		if err := ValidatePassword(password); err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`update users set password_hash=? where id=?`, string(hash), id); err != nil {
			return err
		}
	}
	if role != "" {
		if _, err := tx.Exec(`update users set role=? where id=?`, role, id); err != nil {
			return err
		}
	}
	if containerCap > 0 {
		if _, err := tx.Exec(`update users set container_cap=? where id=?`, containerCap, id); err != nil {
			return err
		}
	}
	if netdiskQuotaBytes != nil {
		if _, err := tx.Exec(`update users set netdisk_quota_bytes=? where id=?`, *netdiskQuotaBytes, id); err != nil {
			return err
		}
	}
	if disabled != nil {
		v := 0
		if *disabled {
			v = 1
		}
		if _, err := tx.Exec(`update users set disabled=? where id=?`, v, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) UpdateUserPortPrefix(id int64, prefix int) error {
	if prefix < 0 || prefix > 655 {
		return errors.New("port prefix must be between 0 and 655")
	}
	_, err := db.Exec(`update users set port_prefix=? where id=?`, prefix, id)
	return err
}

// UpdateUserLanguage updates the language preference for a user
func (db *DB) UpdateUserLanguage(id int64, language string) error {
	_, err := db.Exec(`update users set language=? where id=?`, language, id)
	return err
}

// UpdateUserSharedDiskReadWrite sets a user's self-service preference for
// their own shared-disk (共享盘) subfolder: whether it is mounted read-write
// or read-only wherever the shared disk is bound — in anyone's container,
// not just their own. Takes effect for containers created from this point
// on; already-running containers keep the mount they were created with.
func (db *DB) UpdateUserSharedDiskReadWrite(id int64, readWrite bool) error {
	v := 0
	if readWrite {
		v = 1
	}
	_, err := db.Exec(`update users set shared_disk_read_write=? where id=?`, v, id)
	return err
}

// SharedDiskGroupMembers returns every user in groupID — i.e. everyone whose
// subfolder can appear in that group's pool — with just enough fields
// populated (ID, Username, DisplayName, Role, SharedDiskReadWrite) to compute
// each member's folder name and read-write preference for mount building.
// Pass the id resolved by SharedDiskGroupForUser/AnySharedDiskGroup so the
// members match the pool actually being mounted. See sharedDiskMountsFor.
func (db *DB) SharedDiskGroupMembers(groupID int64) ([]User, error) {
	rows, err := db.Query(`select u2.id, u2.username, u2.display_name, u2.role, u2.shared_disk_read_write
		from users u2
		join user_groups ug2 on ug2.user_id = u2.id
		where ug2.group_id = ?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []User
	for rows.Next() {
		var m User
		var rw int
		if err := rows.Scan(&m.ID, &m.Username, &m.DisplayName, &m.Role, &rw); err != nil {
			return nil, err
		}
		m.SharedDiskReadWrite = rw != 0
		members = append(members, m)
	}
	return members, rows.Err()
}

// UpdateDefaultLanguageForNewUsers updates the language for users who don't have one set
func (db *DB) UpdateDefaultLanguageForNewUsers(language string) error {
	_, err := db.Exec(`update users set language=? where language='' or language is null`, language)
	return err
}

func (db *DB) SaveResourceSamples(samples []ResourceSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`insert into resource_samples(user_id, username, container_id, container_name, cpu_pct, mem_mb, disk_mb, gpu_pct, created_at)
		values(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range samples {
		if s.CreatedAt == "" {
			s.CreatedAt = time.Now().Format(time.RFC3339)
		}
		if _, err := stmt.Exec(s.UserID, s.Username, s.ContainerID, s.Container, s.CPUPercent, s.MemoryMB, s.DiskMB, s.GPUPercent, s.CreatedAt); err != nil {
			return err
		}
	}
	cutoff := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	if _, err := tx.Exec(`delete from resource_samples where created_at < ?`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) ResourceSamples(userID int64, admin bool, since time.Time) ([]ResourceSample, error) {
	q := `select id, user_id, username, container_id, container_name, cpu_pct, mem_mb, disk_mb, gpu_pct, created_at from resource_samples where created_at >= ?`
	args := []any{since.Format(time.RFC3339)}
	if !admin {
		q += ` and user_id=?`
		args = append(args, userID)
	}
	q += ` order by created_at asc`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResourceSample
	for rows.Next() {
		var s ResourceSample
		if err := rows.Scan(&s.ID, &s.UserID, &s.Username, &s.ContainerID, &s.Container, &s.CPUPercent, &s.MemoryMB, &s.DiskMB, &s.GPUPercent, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (db *DB) CreateNetdiskShare(ownerID int64, token, name string, paths []string, expiresAt string, permanent bool, passwordHash, password string) error {
	raw, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	permanentInt := 0
	if permanent {
		permanentInt = 1
	}
	_, err = db.Exec(`insert into netdisk_shares(token, owner_id, name, paths_json, created_at, expires_at, permanent, password_hash, password) values(?,?,?,?,?,?,?,?,?)`,
		token, ownerID, strings.TrimSpace(name), string(raw), time.Now().Format(time.RFC3339), expiresAt, permanentInt, passwordHash, password)
	return err
}

func (db *DB) NetdiskShare(token string) (NetdiskShare, error) {
	var s NetdiskShare
	var raw string
	var permanent int
	err := db.QueryRow(`select token, owner_id, name, paths_json, created_at, expires_at, permanent, password_hash from netdisk_shares where token=?`, token).
		Scan(&s.Token, &s.OwnerID, &s.Name, &raw, &s.CreatedAt, &s.ExpiresAt, &permanent, &s.PasswordHash)
	if err != nil {
		return s, err
	}
	_ = json.Unmarshal([]byte(raw), &s.Paths)
	s.Permanent = permanent != 0
	s.HasPassword = s.PasswordHash != ""
	s.Expired = netdiskShareExpired(s)
	if owner, err := db.usernameByID(s.OwnerID); err == nil {
		s.Owner = owner
	}
	return s, nil
}

func (db *DB) NetdiskShares(ownerID int64) ([]NetdiskShare, error) {
	rows, err := db.Query(`select token, owner_id, name, paths_json, created_at, expires_at, permanent, password_hash, password from netdisk_shares where owner_id=? order by created_at desc`, ownerID)
	return scanNetdiskShares(rows, err)
}

func (db *DB) AllNetdiskShares() ([]NetdiskShare, error) {
	rows, err := db.Query(`select token, owner_id, name, paths_json, created_at, expires_at, permanent, password_hash, password from netdisk_shares order by created_at desc`)
	items, err := scanNetdiskShares(rows, err)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if owner, err := db.usernameByID(items[i].OwnerID); err == nil {
			items[i].Owner = owner
		}
	}
	return items, nil
}

func scanNetdiskShares(rows *sql.Rows, err error) ([]NetdiskShare, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetdiskShare
	for rows.Next() {
		var s NetdiskShare
		var raw string
		var permanent int
		if err := rows.Scan(&s.Token, &s.OwnerID, &s.Name, &raw, &s.CreatedAt, &s.ExpiresAt, &permanent, &s.PasswordHash, &s.Password); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &s.Paths)
		s.Permanent = permanent != 0
		s.HasPassword = s.PasswordHash != ""
		s.Expired = netdiskShareExpired(s)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (db *DB) DeleteNetdiskShare(ownerID int64, token string) error {
	_, err := db.Exec(`delete from netdisk_shares where owner_id=? and token=?`, ownerID, token)
	return err
}

func (db *DB) DeleteNetdiskShares(tokens []string, ownerID int64, admin bool) error {
	if len(tokens) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, token := range tokens {
		if admin {
			if _, err := tx.Exec(`delete from netdisk_shares where token=?`, token); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`delete from netdisk_shares where owner_id=? and token=?`, ownerID, token); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func netdiskShareExpired(s NetdiskShare) bool {
	if s.Permanent || s.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(t)
}

// DeleteUser removes a user. The foreign-key cascade drops their group and
// stack rows. Admins cannot delete themselves here; that guard lives in the
// handler so the store stays free of policy.
func (db *DB) DeleteUser(id int64) error {
	_, err := db.Exec(`delete from users where id=?`, id)
	return err
}

// DeactivateUser returns an account to the pending group. Existing resources
// (containers, stacks, shares) are preserved. The user can still log in so the
// UI can show the "waiting for admin approval" page; access to business
// endpoints is gated by activatedMiddleware / isPending, not by the disabled
// flag. Use UpdateUser(..., disabled=true) for a hard admin lockout.
func (db *DB) DeactivateUser(id int64) error {
	tx, beginErr := db.Begin()
	if beginErr != nil {
		return beginErr
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`delete from user_groups where user_id=?`, id); err != nil {
		return err
	}
	pendingID, err := pendingGroupIDTx(tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`insert or ignore into user_groups(user_id,group_id) values(?,?)`, id, pendingID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------- Stacks (Compose) ----------------

// Stack is a stored Compose project owned by a user.
type Stack struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	OwnerID     int64  `json:"ownerId"`
	Owner       string `json:"owner,omitempty"`
	ComposeYAML string `json:"composeYaml"`
	EnvJSON     string `json:"envJson"`
	ProjectName string `json:"projectName"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// CreateStack persists a new Compose stack. ProjectName is the docker compose
// -p value (namespaced under mudp-).
func (db *DB) CreateStack(ownerID int64, name, composeYAML, envJSON, projectName string) (int64, error) {
	now := time.Now().Format(time.RFC3339)
	res, err := db.Exec(`insert into stacks(name, owner_id, compose_yaml, env_json, project_name, created_at, updated_at)
		values(?,?,?,?,?,?,?)`,
		name, ownerID, composeYAML, envJSON, projectName, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateStack replaces a stack's compose body and env. Owner and project stay.
func (db *DB) UpdateStack(id int64, composeYAML, envJSON string) error {
	_, err := db.Exec(`update stacks set compose_yaml=?, env_json=?, updated_at=? where id=?`,
		composeYAML, envJSON, time.Now().Format(time.RFC3339), id)
	return err
}

// StackByID loads a single stack.
func (db *DB) StackByID(id int64) (Stack, error) {
	var s Stack
	err := db.QueryRow(`select id, name, owner_id, compose_yaml, env_json, project_name, created_at, updated_at from stacks where id=?`, id).
		Scan(&s.ID, &s.Name, &s.OwnerID, &s.ComposeYAML, &s.EnvJSON, &s.ProjectName, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// StacksForUser returns stacks owned by userID (admin: all), newest first.
func (db *DB) StacksForUser(userID int64, admin bool) ([]Stack, error) {
	q := `select id, name, owner_id, compose_yaml, env_json, project_name, created_at, updated_at from stacks`
	var args []any
	if !admin {
		q += ` where owner_id=?`
		args = append(args, userID)
	}
	q += ` order by updated_at desc`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stack
	for rows.Next() {
		var s Stack
		if err := rows.Scan(&s.ID, &s.Name, &s.OwnerID, &s.ComposeYAML, &s.EnvJSON, &s.ProjectName, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if owner, err := db.usernameByID(s.OwnerID); err == nil {
			s.Owner = owner
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteStack removes a stack record. Ownership is checked by the handler.
func (db *DB) DeleteStack(id int64) error {
	_, err := db.Exec(`delete from stacks where id=?`, id)
	return err
}

// usernameByID is a tiny helper for denormalising owner display names into
// stack / share / volume lists. It returns display_name when present so Feishu
// users appear with their real name, while falling back to username for local
// users and older records.
func (db *DB) usernameByID(id int64) (string, error) {
	var username, displayName string
	err := db.QueryRow(`select username, display_name from users where id=?`, id).Scan(&username, &displayName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(displayName) != "" {
		return displayName, nil
	}
	return username, nil
}

// ---------------- Registries ----------------

// Registry is a stored authenticated registry credential. Token is plaintext in
// the DB for v1 (single-host); a follow-up should encrypt at rest.
type Registry struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Token    string `json:"token,omitempty"`
}

// Registries returns all stored registries. Token is included because this is
// admin-only and used to populate the docker auth config on pull/build.
func (db *DB) Registries() ([]Registry, error) {
	var raw string
	err := db.QueryRow(`select value from settings where key='registries'`).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var out []Registry
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveRegistries replaces the entire registry list.
func (db *DB) SaveRegistries(items []Registry) error {
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return db.setSetting("registries", string(raw))
}

func (db *DB) setSetting(key, value string) error {
	_, err := db.Exec(`insert into settings(key, value) values(?, ?)
		on conflict(key) do update set value=excluded.value`, key, value)
	return err
}

// Setting returns a single named value from the generic settings table, or ""
// if the key is absent.
func (db *DB) Setting(key string) (string, error) {
	return db.getSetting(key)
}

// SaveSetting upserts a single named value in the generic settings table.
func (db *DB) SaveSetting(key, value string) error {
	return db.setSetting(key, value)
}

// getSetting returns a single settings value, or "" if the key is absent.
func (db *DB) getSetting(key string) (string, error) {
	return getSettingTx(db, key)
}

// getSettingTx is getSetting evaluated against the given executor, so it can
// run inside an existing transaction (e.g. checkUserCapacity, called from
// inside createUserTx) instead of taking a second connection from the pool —
// which would self-deadlock a pool sized for exactly the one connection the
// enclosing transaction is already holding.
func getSettingTx(e executor, key string) (string, error) {
	var value string
	err := e.QueryRow(`select value from settings where key=?`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}
