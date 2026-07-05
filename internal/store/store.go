package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

type User struct {
	ID           int64    `json:"id"`
	Username     string   `json:"username"`
	Role         string   `json:"role"`
	Groups       []string `json:"groups,omitempty"`
	PortPrefix   int      `json:"portPrefix"`
	CreatedAt    string   `json:"createdAt"`
	LastLoginAt  *string  `json:"lastLoginAt,omitempty"`
	Disabled     bool     `json:"disabled"`
	ContainerCap int      `json:"containerCap"`
	FeishuOpenID string   `json:"feishuOpenId,omitempty"`
}

// ValidRole reports whether r is one of the supported RBAC roles.
func ValidRole(r string) bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleHelpdesk, RoleReadonly, RoleUser:
		return true
	}
	return false
}

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleHelpdesk = "helpdesk"
	RoleReadonly = "readonly"
	RoleUser     = "user"
)

// PendingGroup is the name of the holding group new Feishu users land in until
// an admin assigns them to a real group.
const PendingGroup = "pending"

type Group struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	NetdiskPath string `json:"netdiskPath,omitempty"`
}

type Image struct {
	ID          int64    `json:"id"`
	DisplayName string   `json:"name"`
	DockerRef   string   `json:"dockerRef"`
	SourceRef   string   `json:"sourceRef"`
	Groups      []string `json:"groups,omitempty"`
	CreatedAt   string   `json:"createdAt"`
}

type ScriptSettings struct {
	SSHScript    string `json:"sshScript"`
	VSCodeScript string `json:"vscodeScript"`
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
	Token     string   `json:"token"`
	OwnerID   int64    `json:"ownerId"`
	Owner     string   `json:"owner,omitempty"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
	CreatedAt string   `json:"createdAt"`
	ExpiresAt string   `json:"expiresAt,omitempty"`
	Permanent bool     `json:"permanent"`
	Expired   bool     `json:"expired"`
}

// FusedImage is a cached derived image that pre-installs SSH/VSCode for a
// specific base image + script body combination, so container start skips the
// slow per-boot install. Keyed by CacheKey (a hash of those inputs).
type FusedImage struct {
	CacheKey     string `json:"cacheKey"`
	BaseRef      string `json:"baseRef"`
	BaseImageID  string `json:"baseImageId"`
	FusedRef     string `json:"fusedRef"`
	EnableSSH    bool   `json:"enableSsh"`
	EnableVSCode bool   `json:"enableVscode"`
	ScriptHash   string `json:"scriptHash"`
	CreatedAt    string `json:"createdAt"`
}

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		pragma journal_mode = wal;
		pragma foreign_keys = on;
		pragma busy_timeout = 5000;
		pragma synchronous = normal;
		pragma wal_autocheckpoint = 1000;
	`); err != nil {
		return nil, err
	}
	return &DB{DB: db}, nil
}

// execIgnoring runs stmt and returns nil when the error message contains any
// of the ignored fragments. It is used for idempotent schema migrations where
// the target object may already exist.
func execIgnoring(db *sql.DB, stmt string, ignore ...string) error {
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

func (db *DB) Migrate(adminUser, adminPassword string) error {
	stmts := []string{
		// users table is created without a CHECK on role: the application layer
		// (store.ValidRole) validates role values. This avoids painful table
		// rebuilds when the supported role set grows. Existing DBs keep their
		// (possibly narrower) CHECK; widenRoleConstraint below relaxes it.
		`create table if not exists users (
			id integer primary key autoincrement,
			username text not null unique,
			password_hash text not null,
			role text not null,
			disabled integer not null default 0,
			container_cap integer not null default 10,
			port_prefix integer not null default 0,
			created_at text not null,
			last_login_at text
		)`,
		`create table if not exists groups (
			id integer primary key autoincrement,
			name text not null unique,
			netdisk_path text not null default ''
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
			created_at text not null
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
			permanent integer not null default 0
		)`,
		`create table if not exists fused_images (
			cache_key text primary key,
			base_ref text not null,
			base_image_id text not null,
			fused_ref text not null,
			enable_ssh integer not null,
			enable_vscode integer not null,
			script_hash text not null,
			created_at text not null
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	// Add feishu_open_id column if missing (idempotent across older DBs).
	// ALTER TABLE ADD COLUMN is safe to ignore when the column already exists;
	// other errors are surfaced so the schema cannot end up inconsistent.
	if err := execIgnoring(db.DB, `alter table users add column feishu_open_id text default ''`, sqliteDuplicateColumn); err != nil {
		return err
	}
	if err := execIgnoring(db.DB, `alter table users add column port_prefix integer not null default 0`, sqliteDuplicateColumn); err != nil {
		return err
	}
	if err := execIgnoring(db.DB, `alter table groups add column netdisk_path text not null default ''`, sqliteDuplicateColumn); err != nil {
		return err
	}
	if err := execIgnoring(db.DB, `alter table netdisk_shares add column expires_at text not null default ''`, sqliteDuplicateColumn); err != nil {
		return err
	}
	if err := execIgnoring(db.DB, `alter table netdisk_shares add column permanent integer not null default 0`, sqliteDuplicateColumn); err != nil {
		return err
	}
	if _, err := db.Exec(`create unique index if not exists idx_users_feishu_open_id on users(feishu_open_id) where feishu_open_id != ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`create index if not exists idx_audit_created on audit_logs(created_at desc)`); err != nil {
		return err
	}
	if _, err := db.Exec(`create index if not exists idx_resource_samples_created on resource_samples(created_at desc)`); err != nil {
		return err
	}
	if _, err := db.Exec(`create index if not exists idx_resource_samples_user_created on resource_samples(user_id, created_at desc)`); err != nil {
		return err
	}
	if _, err := db.Exec(`create index if not exists idx_netdisk_shares_owner on netdisk_shares(owner_id, created_at desc)`); err != nil {
		return err
	}
	if err := db.widenRoleConstraint(); err != nil {
		return err
	}
	var n int
	if err := db.QueryRow(`select count(*) from users where role='admin'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if err := db.CreateUser(adminUser, adminPassword, "admin", nil, 50); err != nil {
			return err
		}
	}
	if err := db.EnsurePendingGroup(); err != nil {
		return err
	}
	if err := db.upgradeDefaultScripts(); err != nil {
		return err
	}
	return db.ensureDefaultScripts()
}

// legacySSHScriptMarkers / legacyVSCodeScriptMarkers identify the previous
// default bootstrap scripts (the ones that started daemons unconditionally,
// which broke fused-image builds). upgradeDefaultScripts replaces any stored
// script matching these exact legacy bodies with the current default, so
// existing deployments get the build-safe version automatically. Admin-edited
// scripts that don't match are left untouched.
var legacySSHScriptMarkers = []string{
	`/usr/sbin/sshd || sshd || true`, // pre-MUDP_BUILD_PHASE guard: sshd started at build
}
var legacyVSCodeScriptMarkers = []string{
	`nohup code-server /workspace >/tmp/mudp/code-server.log 2>&1 &
`,
}

// upgradeDefaultScripts replaces stored scripts that still contain the legacy
// daemon-start lines (and lack the MUDP_BUILD_PHASE guard) with the current
// defaults. It only acts on scripts that look like the old defaults; admin
// customizations are preserved.
func (db *DB) upgradeDefaultScripts() error {
	cfg, err := db.ScriptSettings()
	if err != nil {
		return err
	}
	updated := ScriptSettings{}
	needUpdate := false
	if !strings.Contains(cfg.SSHScript, "MUDP_BUILD_PHASE") && containsAny(cfg.SSHScript, legacySSHScriptMarkers) {
		updated.SSHScript = defaultSSHScript()
		needUpdate = true
	} else {
		updated.SSHScript = cfg.SSHScript
	}
	if !strings.Contains(cfg.VSCodeScript, "MUDP_BUILD_PHASE") && containsAny(cfg.VSCodeScript, legacyVSCodeScriptMarkers) {
		updated.VSCodeScript = defaultVSCodeScript()
		needUpdate = true
	} else {
		updated.VSCodeScript = cfg.VSCodeScript
	}
	if !needUpdate {
		return nil
	}
	return db.SaveScriptSettings(updated)
}

// containsAny reports whether s contains any of the substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// widenRoleConstraint relaxes an older users.role CHECK that only allowed
// ('admin','user'). It is a no-op when the constraint is already permissive or
// absent. The rebuild is wrapped in a transaction and preserves every column
// and row, including auto-increment continuity.
func (db *DB) widenRoleConstraint() error {
	var sql string
	err := db.QueryRow(`select sql from sqlite_master where type='table' and name='users'`).Scan(&sql)
	if err != nil {
		return err
	}
	// Only rebuild when the old restrictive CHECK is still present.
	if !strings.Contains(sql, "check(role in ('admin','user'))") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	steps := []string{
		`alter table users rename to users_old`,
		`create table users (
			id integer primary key autoincrement,
			username text not null unique,
			password_hash text not null,
			role text not null,
			disabled integer not null default 0,
			container_cap integer not null default 10,
			port_prefix integer not null default 0,
			created_at text not null,
			last_login_at text,
			feishu_open_id text default ''
		)`,
		`insert into users(id, username, password_hash, role, disabled, container_cap, port_prefix, created_at, last_login_at, feishu_open_id)
		 select id, username, password_hash, role, disabled, container_cap, port_prefix, created_at, last_login_at, feishu_open_id from users_old`,
		`drop table users_old`,
	}
	for _, s := range steps {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) CreateUser(username, password, role string, groupIDs []int64, cap int) error {
	if cap <= 0 {
		cap = 10
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`insert into users(username,password_hash,role,container_cap,created_at) values(?,?,?,?,?)`,
		username, string(hash), role, cap, time.Now().Format(time.RFC3339))
	if err != nil {
		return err
	}
	uid, _ := res.LastInsertId()
	for _, gid := range groupIDs {
		if _, err := tx.Exec(`insert or ignore into user_groups(user_id,group_id) values(?,?)`, uid, gid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) Authenticate(username, password string) (*User, error) {
	var u User
	var hash string
	var disabled int
	err := db.QueryRow(`select id,username,password_hash,role,disabled,container_cap,port_prefix,created_at,last_login_at,feishu_open_id from users where username=?`, username).
		Scan(&u.ID, &u.Username, &hash, &u.Role, &disabled, &u.ContainerCap, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("invalid username or password")
	}
	if err != nil {
		return nil, err
	}
	if disabled != 0 {
		return nil, errors.New("user is disabled")
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
	var disabled int
	err := db.QueryRow(`select id,username,role,disabled,container_cap,port_prefix,created_at,last_login_at,feishu_open_id from users where id=?`, id).
		Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.ContainerCap, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID)
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

func (db *DB) Users() ([]User, error) {
	rows, err := db.Query(`select id,username,role,disabled,container_cap,port_prefix,created_at,last_login_at,feishu_open_id from users order by username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var disabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.ContainerCap, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		u.Groups = db.UserGroupNames(u.ID)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (db *DB) Groups() ([]Group, error) {
	rows, err := db.Query(`select id,name,netdisk_path from groups order by name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.NetdiskPath); err != nil {
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

func (db *DB) SaveImage(displayName, dockerRef, sourceRef string) error {
	_, err := db.Exec(`insert into images(display_name,docker_ref,source_ref,created_at) values(?,?,?,?)
		on conflict(display_name) do update set docker_ref=excluded.docker_ref, source_ref=excluded.source_ref`,
		displayName, dockerRef, sourceRef, time.Now().Format(time.RFC3339))
	return err
}

func (db *DB) ImagesForUser(userID int64, admin bool) ([]Image, error) {
	q := `select distinct i.id,i.display_name,i.docker_ref,i.source_ref,i.created_at
		from images i order by i.display_name`
	args := []any{}
	if !admin {
		q = `select distinct i.id,i.display_name,i.docker_ref,i.source_ref,i.created_at
			from images i
			join group_images gi on gi.image_id=i.id
			join user_groups ug on ug.group_id=gi.group_id
			where ug.user_id=?
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
		if err := rows.Scan(&img.ID, &img.DisplayName, &img.DockerRef, &img.SourceRef, &img.CreatedAt); err != nil {
			return nil, err
		}
		img.Groups = db.ImageGroupNames(img.ID)
		imgs = append(imgs, img)
	}
	return imgs, rows.Err()
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

func (db *DB) ScriptSettings() (ScriptSettings, error) {
	cfg := ScriptSettings{
		SSHScript:    defaultSSHScript(),
		VSCodeScript: defaultVSCodeScript(),
	}
	rows, err := db.Query(`select key, value from settings where key in ('ssh_script','vscode_script')`)
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
		case "ssh_script":
			cfg.SSHScript = value
		case "vscode_script":
			cfg.VSCodeScript = value
		}
	}
	return cfg, rows.Err()
}

func (db *DB) SaveScriptSettings(cfg ScriptSettings) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	items := map[string]string{
		"ssh_script":    cfg.SSHScript,
		"vscode_script": cfg.VSCodeScript,
	}
	for key, value := range items {
		if _, err := tx.Exec(`insert into settings(key, value) values(?, ?)
			on conflict(key) do update set value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) ensureDefaultScripts() error {
	cfg, err := db.ScriptSettings()
	if err != nil {
		return err
	}
	return db.SaveScriptSettings(cfg)
}

func defaultSSHScript() string {
	return `#!/bin/sh
set -eu

have_cmd() { command -v "$1" >/dev/null 2>&1; }

install_packages() {
  if have_cmd apt-get; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y openssh-server
    return 0
  fi
  if have_cmd apk; then
    apk add --no-cache openssh
    return 0
  fi
  if have_cmd dnf; then
    dnf install -y openssh-server openssh-clients
    return 0
  fi
  if have_cmd yum; then
    yum install -y openssh-server openssh-clients
    return 0
  fi
  echo "No supported package manager found for SSH bootstrap." >&2
  exit 1
}

have_cmd sshd || install_packages
mkdir -p /var/run/sshd
if command -v ssh-keygen >/dev/null 2>&1; then
  ssh-keygen -A >/dev/null 2>&1 || true
fi
cat <<EOF | chpasswd
root:${MUDP_ACCESS_PASSWORD}
EOF
if [ -f /etc/ssh/sshd_config ]; then
  sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config || true
  sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config || true
  grep -q '^PermitRootLogin yes' /etc/ssh/sshd_config || echo 'PermitRootLogin yes' >> /etc/ssh/sshd_config
  grep -q '^PasswordAuthentication yes' /etc/ssh/sshd_config || echo 'PasswordAuthentication yes' >> /etc/ssh/sshd_config
fi
# Start sshd only at container runtime, never during a fused-image build (where
# MUDP_BUILD_PHASE=1), otherwise the daemon would hang or be killed mid-build.
if [ -z "${MUDP_BUILD_PHASE:-}" ]; then
  if command -v service >/dev/null 2>&1; then
    service ssh start || true
  fi
  if command -v sshd >/dev/null 2>&1; then
    /usr/sbin/sshd || sshd || true
  fi
fi
`
}

func defaultVSCodeScript() string {
	return `#!/bin/sh
set -eu

have_cmd() { command -v "$1" >/dev/null 2>&1; }

if ! have_cmd code-server; then
  if ! have_cmd curl; then
    if have_cmd apt-get; then
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y curl
    elif have_cmd apk; then
      apk add --no-cache curl
    elif have_cmd dnf; then
      dnf install -y curl
    elif have_cmd yum; then
      yum install -y curl
    else
      echo "curl is required to install code-server." >&2
      exit 1
    fi
  fi
  curl -fsSL https://code-server.dev/install.sh | sh
fi

mkdir -p /root/.config/code-server /root/.local/share/code-server /workspace /tmp/mudp
cat > /root/.config/code-server/config.yaml <<EOF
bind-addr: 0.0.0.0:13337
auth: password
password: ${MUDP_ACCESS_PASSWORD}
cert: false
user-data-dir: /root/.local/share/code-server
EOF
# Start code-server only at container runtime, never during a fused-image build
# (MUDP_BUILD_PHASE=1), where launching it would hang the build.
if [ -z "${MUDP_BUILD_PHASE:-}" ]; then
  nohup code-server /workspace >/tmp/mudp/code-server.log 2>&1 &
fi
`
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

// UserByFeishu looks up a user by their Feishu open_id.
func (db *DB) UserByFeishu(openID string) (*User, error) {
	if openID == "" {
		return nil, sql.ErrNoRows
	}
	var u User
	var disabled int
	err := db.QueryRow(`select id,username,role,disabled,container_cap,port_prefix,created_at,last_login_at,feishu_open_id from users where feishu_open_id=?`, openID).
		Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.ContainerCap, &u.PortPrefix, &u.CreatedAt, &u.LastLoginAt, &u.FeishuOpenID)
	if err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	u.Groups = db.UserGroupNames(u.ID)
	return &u, nil
}

// CreateFeishuUser registers a new user from a Feishu login. The user is placed
// in the pending group until an admin assigns them a real group. Returns the
// created user.
func (db *DB) CreateFeishuUser(openID, name string) (*User, error) {
	if openID == "" {
		return nil, errors.New("feishu open_id is required")
	}
	username := name
	if strings.TrimSpace(username) == "" {
		username = "feishu-" + openID
	}
	base := username
	// Retry a few times if another request claims the generated username
	// between the existence check and the insert.
	for i := 0; i < 10; i++ {
		if i > 0 {
			username = fmt.Sprintf("%s-%d", base, i)
		}
		u, err := db.createFeishuUserTx(openID, username)
		if err == nil {
			return u, nil
		}
		if !isSQLiteConstraintError(err) {
			return nil, err
		}
	}
	return nil, errors.New("could not allocate a unique username")
}

func (db *DB) createFeishuUserTx(openID, username string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(openID), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRow(`select count(*) from users where username=?`, username).Scan(&existing); err != nil {
		return nil, err
	}
	if existing != 0 {
		return nil, errors.New("duplicate username")
	}
	res, err := tx.Exec(`insert into users(username,password_hash,role,container_cap,created_at,feishu_open_id) values(?,?,?,?,?,?)`,
		username, string(hash), "user", 10, time.Now().Format(time.RFC3339), openID)
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

func isSQLiteConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		strings.Contains(err.Error(), "constraint failed")
}

func pendingGroupIDTx(tx *sql.Tx) (int64, error) {
	if _, err := tx.Exec(`insert or ignore into groups(name) values(?)`, PendingGroup); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(`select id from groups where name=?`, PendingGroup).Scan(&id)
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
func (db *DB) UpdateUser(id int64, password, role string, containerCap int, disabled *bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if password != "" {
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

func (db *DB) CreateNetdiskShare(ownerID int64, token, name string, paths []string, expiresAt string, permanent bool) error {
	raw, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	permanentInt := 0
	if permanent {
		permanentInt = 1
	}
	_, err = db.Exec(`insert into netdisk_shares(token, owner_id, name, paths_json, created_at, expires_at, permanent) values(?,?,?,?,?,?,?)`,
		token, ownerID, strings.TrimSpace(name), string(raw), time.Now().Format(time.RFC3339), expiresAt, permanentInt)
	return err
}

func (db *DB) NetdiskShare(token string) (NetdiskShare, error) {
	var s NetdiskShare
	var raw string
	var permanent int
	err := db.QueryRow(`select token, owner_id, name, paths_json, created_at, expires_at, permanent from netdisk_shares where token=?`, token).
		Scan(&s.Token, &s.OwnerID, &s.Name, &raw, &s.CreatedAt, &s.ExpiresAt, &permanent)
	if err != nil {
		return s, err
	}
	_ = json.Unmarshal([]byte(raw), &s.Paths)
	s.Permanent = permanent != 0
	s.Expired = netdiskShareExpired(s)
	if owner, err := db.usernameByID(s.OwnerID); err == nil {
		s.Owner = owner
	}
	return s, nil
}

func (db *DB) NetdiskShares(ownerID int64) ([]NetdiskShare, error) {
	rows, err := db.Query(`select token, owner_id, name, paths_json, created_at, expires_at, permanent from netdisk_shares where owner_id=? order by created_at desc`, ownerID)
	return scanNetdiskShares(rows, err)
}

func (db *DB) AllNetdiskShares() ([]NetdiskShare, error) {
	rows, err := db.Query(`select token, owner_id, name, paths_json, created_at, expires_at, permanent from netdisk_shares order by created_at desc`)
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
		if err := rows.Scan(&s.Token, &s.OwnerID, &s.Name, &raw, &s.CreatedAt, &s.ExpiresAt, &permanent); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &s.Paths)
		s.Permanent = permanent != 0
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

// usernameByID is a tiny helper for denormalising owner names into stack lists.
func (db *DB) usernameByID(id int64) (string, error) {
	var name string
	err := db.QueryRow(`select username from users where id=?`, id).Scan(&name)
	return name, err
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

// --- Fused image cache ------------------------------------------------------

// GetFusedImage returns the cached derived image for a cache key, or ok=false
// when there is no row yet.
func (db *DB) GetFusedImage(cacheKey string) (FusedImage, bool, error) {
	var f FusedImage
	var ssh, vscode int
	err := db.QueryRow(`select cache_key, base_ref, base_image_id, fused_ref,
		enable_ssh, enable_vscode, script_hash, created_at
		from fused_images where cache_key=?`, cacheKey).
		Scan(&f.CacheKey, &f.BaseRef, &f.BaseImageID, &f.FusedRef,
			&ssh, &vscode, &f.ScriptHash, &f.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FusedImage{}, false, nil
		}
		return FusedImage{}, false, err
	}
	f.EnableSSH = ssh == 1
	f.EnableVSCode = vscode == 1
	return f, true, nil
}

// SaveFusedImage inserts or replaces a fused-image cache entry.
func (db *DB) SaveFusedImage(f FusedImage) error {
	ssh, vscode := 0, 0
	if f.EnableSSH {
		ssh = 1
	}
	if f.EnableVSCode {
		vscode = 1
	}
	_, err := db.Exec(`insert into fused_images
		(cache_key, base_ref, base_image_id, fused_ref, enable_ssh, enable_vscode, script_hash, created_at)
		values(?,?,?,?,?,?,?,?)
		on conflict(cache_key) do update set
			base_ref=excluded.base_ref, base_image_id=excluded.base_image_id,
			fused_ref=excluded.fused_ref, enable_ssh=excluded.enable_ssh,
			enable_vscode=excluded.enable_vscode, script_hash=excluded.script_hash,
			created_at=excluded.created_at`,
		f.CacheKey, f.BaseRef, f.BaseImageID, f.FusedRef, ssh, vscode, f.ScriptHash,
		time.Now().Format(time.RFC3339))
	return err
}

// ListFusedImages returns all cached fused images, newest first.
func (db *DB) ListFusedImages() ([]FusedImage, error) {
	rows, err := db.Query(`select cache_key, base_ref, base_image_id, fused_ref,
		enable_ssh, enable_vscode, script_hash, created_at
		from fused_images order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FusedImage
	for rows.Next() {
		var f FusedImage
		var ssh, vscode int
		if err := rows.Scan(&f.CacheKey, &f.BaseRef, &f.BaseImageID, &f.FusedRef,
			&ssh, &vscode, &f.ScriptHash, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.EnableSSH = ssh == 1
		f.EnableVSCode = vscode == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFusedImage drops a cache entry (e.g. when the underlying image was
// pruned out from under us).
func (db *DB) DeleteFusedImage(cacheKey string) error {
	_, err := db.Exec(`delete from fused_images where cache_key=?`, cacheKey)
	return err
}
