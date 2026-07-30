package store

// On-demand database housekeeping for the admin "Database" page. The automatic
// pruner (server/resources.go) keeps the log tables bounded on a schedule, but
// an operator looking at disk pressure wants to see where the bytes are and to
// reclaim them now rather than waiting for the next tick.
//
// Safety is the central concern of this file: a cleanup that accepted a table
// name from the request and issued `delete from <name>` would be a footgun the
// first time someone pointed it at `users`. So the set of tables an admin may
// prune is a hard-coded constant here, and every entry in it is a log table.
// User data — users, groups, images, settings — can never appear in it, and the
// method rejects anything else by name.

import (
	"fmt"
	"os"
	"sort"
	"time"
)

// pruneableLogTables is the fixed allow-list of tables an admin may clear from
// the Database page. It holds only append-only log/telemetry tables whose loss
// is acceptable. Adding a table here is the only way to make it pruneable; there
// is no string interpolation that could reach an arbitrary table.
var pruneableLogTables = map[string]bool{
	"audit_logs":       true,
	"access_logs":      true,
	"mcp_usage_logs":   true,
	"mcp_attack_logs":  true,
	"resource_samples": true,
}

// PruneableLogTables returns the names an admin is allowed to prune, sorted, so
// the handler can both validate input and render the UI from the same source of
// truth.
func PruneableLogTables() []string {
	out := make([]string, 0, len(pruneableLogTables))
	for name := range pruneableLogTables {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IsPruneableLogTable reports whether name is in the prune allow-list.
func IsPruneableLogTable(name string) bool {
	return pruneableLogTables[name]
}

// TableUsage is one table's footprint: how many bytes its pages occupy and how
// many rows it holds. Bytes may be zero when the page-size query is unavailable
// (the dbstat virtual table is not compiled in); rows is always populated for
// known tables so the page is still useful without it. Description is a short
// human-readable note about what the table is for, shown in the Database page so
// an operator reading the breakdown knows what each name means.
type TableUsage struct {
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	Rows        int64  `json:"rows"`
	Description string `json:"description,omitempty"`
}

// DBUsageReport is what the Database page renders. FileBytes/WalBytes/ShmBytes
// come from the host filesystem; TotalBytes is the sum of the table pages, which
// is usually smaller than FileBytes because of the WAL and free pages.
type DBUsageReport struct {
	Tables     []TableUsage `json:"tables"`
	TotalBytes int64        `json:"totalBytes"`
	FileBytes  int64        `json:"fileBytes"`
	WalBytes   int64        `json:"walBytes"`
	ShmBytes   int64        `json:"shmBytes"`
	FreePages  int64        `json:"freePages"`
	// ByteSizes reports whether per-table byte sizes are available. When false,
	// the page falls back to ordering by row count and shows sizes as "—".
	ByteSizes bool `json:"byteSizes"`
}

// DBUsage builds the report. dbPath is the on-disk database file (cfg.DBPath);
// the -wal and -shm siblings are sized alongside it. Errors gathering one table
// or one stat do not abort the rest: an admin reading this page wants whatever
// is available, not a blank page because one metadata query failed.
func (db *DB) DBUsage(dbPath string) (DBUsageReport, error) {
	report := DBUsageReport{}

	// Filesystem sizes first: they are cheap, independent of SQLite, and the
	// page leads with them. A missing file is reported but does not stop the
	// table breakdown from running.
	report.FileBytes = fileSize(dbPath)
	report.WalBytes = fileSize(dbPath + "-wal")
	report.ShmBytes = fileSize(dbPath + "-shm")

	// Per-table page sizes via the dbstat virtual table. modernc.org/sqlite
	// builds it in, but a build that does not (or a read-only connection that
	// forbids it) returns an error here; we record the failure and carry on so
	// the page still shows row counts.
	pageBytes := map[string]int64{}
	rows, err := db.Query(`select name, sum(pgsize) from dbstat group by name`)
	if err == nil {
		for rows.Next() {
			var name string
			var bytes int64
			if err := rows.Scan(&name, &bytes); err == nil {
				pageBytes[name] += bytes
			}
		}
		_ = rows.Close()
		report.ByteSizes = len(pageBytes) > 0
	}

	// Row counts for the tables we know about. We list the union of the prune
	// allow-list and the other well-known tables so the page shows the full
	// picture (user tables appear too, just without a "clean" button).
	known := knownTableSet()
	type rc struct {
		name string
		rows int64
	}
	counts := make([]rc, 0, len(known))
	for name := range known {
		n, err := db.tableRowCount(name)
		if err != nil {
			// A table that no longer exists (an old schema) or is locked is
			// skipped rather than fatal: the page is informational.
			continue
		}
		counts = append(counts, rc{name, n})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].rows != counts[j].rows {
			return counts[i].rows > counts[j].rows
		}
		return counts[i].name < counts[j].name
	})

	report.Tables = make([]TableUsage, 0, len(counts))
	for _, c := range counts {
		b := pageBytes[c.name]
		report.TotalBytes += b
		report.Tables = append(report.Tables, TableUsage{Name: c.name, Bytes: b, Rows: c.rows, Description: tableDescriptions[c.name]})
	}

	// Free pages: reclaimed space a checkpoint would return to the filesystem.
	// Failing to read the pragma is harmless (older SQLite / read-only).
	var freePages int64
	_ = db.QueryRow(`pragma freelist_count`).Scan(&freePages)
	report.FreePages = freePages
	return report, nil
}

// tableRowCount returns the row count for a table, or an error if it cannot be
// counted. Table names come from the hard-coded known set, not from user input,
// so interpolation here is safe.
func (db *DB) tableRowCount(name string) (int64, error) {
	var n int64
	// nolint:gosec // name is from the hard-coded knownTableSet, not user input.
	err := db.QueryRow(fmt.Sprintf(`select count(*) from %s`, name)).Scan(&n)
	return n, err
}

// tableDescriptions maps each known table to a short note explaining what it
// holds and where it surfaces in the console. It is the single source the
// Database page reads from, so the list shown to the operator can never drift
// from what the schema actually contains. A missing entry falls back to "",
// which the page renders as "—". Keyed on the physical table name.
var tableDescriptions = map[string]string{
	// User data & access control
	"users":          "用户账号、密码哈希、角色、端口前缀、容器与网盘配额、飞书绑定及登录状态",
	"groups":         "用户组（含内置 pending 待审组、users 默认组）、组网盘与备份根路径、组语言",
	"user_groups":    "用户与组的关联（多对多），决定用户可见的镜像/网络/网盘路径",

	// Images & container defaults
	"images":         "可用镜像注册表，含管理员预设的默认配置（GPU/端口/环境变量/设备等）",
	"group_images":   "镜像与组的授权关联；未授权的镜像对所有用户公开，授权的仅组成员可见",

	// Networks
	"group_networks": "组与 Docker 网络的授权，决定哪些用户组可接入某网络",

	// Compose
	"stacks":         "用户拥有的 Docker Compose 堆栈（compose YAML、环境变量、项目名）",

	// Netdisk & sharing
	"netdisk_shares": "网盘分享链接：令牌、路径、过期/永久、访问密码（哈希+所有者可查看的明文）",

	// MCP
	"mcp_tokens":      "每个容器的 MCP 访问令牌：哈希（请求查找）、明文（重看配置）、容器与所有者、使用/过期时间",
	"mcp_usage_logs":  "MCP 工具调用日志（令牌、容器、工具、参数预览、外部请求的地理/IP）— 日志表，可清理",
	"mcp_attack_logs": "被拒绝的外部 MCP 请求记录（来源、地理、原因、路径）— 日志表，可清理",

	// Port forwarding
	"port_forwards": "管理员手动添加的端口转发规则（主机端口、协议、目标、是否需登录）",

	// Security monitoring
	"access_logs": "登录页访问与认证日志（IP、地理、浏览器/OS、VPN/代理检测、客户端设备指纹）— 日志表，可清理",

	// Audit & resource monitoring
	"audit_logs":       "管理操作审计日志（操作者、动作、目标、时间）— 日志表，可清理",
	"resource_samples": "容器资源采样（CPU/内存/磁盘/GPU 时序点，用于使用图表）— 日志表，可清理",

	// Notifications
	"notifications": "站内通知（按用户的提醒，含已读状态，可批量通知所有管理员）",

	// Configuration & system
	"settings":         "通用键值配置：飞书 OAuth、镜像仓库、安全监控、端口转发、磁盘挂载、站点名等",
	"backup_schedule":  "每日数据库备份计划（单行配置：执行时间、启用、上次运行）",
	"schema_version":   "数据库迁移版本追踪（系统表，记录已应用的迁移版本）",
}

// TableDescription returns the human-readable note for a known table, or "" when
// the name is unknown. Exposed so the handler (and tests) share one source.
func TableDescription(name string) string {
	return tableDescriptions[name]
}

// knownTableSet returns the tables the Database page lists. It is the prune
// allow-list plus the user/system tables an admin should be able to see the size
// of (but never clean). Anything not here is simply omitted from the page. It is
// the union of tableDescriptions' keys and the prune allow-list, so adding a
// table's description automatically makes it appear in the page.
func knownTableSet() map[string]bool {
	out := map[string]bool{}
	for name := range pruneableLogTables {
		out[name] = true
	}
	for name := range tableDescriptions {
		out[name] = true
	}
	return out
}

// fileSize returns the size of a file, or 0 if it is absent. The WAL and SHM
// files are created on demand and may not exist; that is normal, not an error.
func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// PruneLogs deletes rows older than `before` from each named table. It returns
// the number of rows deleted per table. Every name must be in the prune
// allow-list — anything else (users, settings, …) is refused before any SQL
// runs, so a bad request can never reach user data. A name not present in the
// result (because it was unknown or errored) is simply absent from the map.
func (db *DB) PruneLogs(tables []string, before time.Time) (map[string]int64, error) {
	deleted := map[string]int64{}
	for _, name := range tables {
		if !pruneableLogTables[name] {
			return nil, fmt.Errorf("table %q is not a pruneable log table", name)
		}
		countBefore, err := db.tableRowCount(name)
		if err != nil {
			continue
		}
		switch name {
		case "audit_logs":
			err = db.PruneAuditLogs(before)
		case "access_logs":
			err = db.PruneAccessLogs(before)
		case "mcp_usage_logs":
			err = db.PruneMCPUsageLogs(before)
		case "mcp_attack_logs":
			err = db.PruneMCPAttackLogs(before)
		case "resource_samples":
			err = db.PruneResourceSamples(before)
		}
		if err != nil {
			return deleted, err
		}
		countAfter, err := db.tableRowCount(name)
		if err == nil {
			deleted[name] = countBefore - countAfter
		}
	}
	return deleted, nil
}

// VacuumAndCheckpoint returns freed pages to the filesystem after a prune. It
// runs a TRUNCATE checkpoint rather than VACUUM: VACUUM rewrites the whole
// database and locks it for the duration, which is unacceptable on a live
// server, while a checkpoint folds the WAL back in and truncates the file in
// bounded time. Free pages left inside the database file are not compacted, but
// they are reused by future writes, so the net effect is that growth stops.
func (db *DB) VacuumAndCheckpoint() error {
	_, err := db.Exec(`pragma wal_checkpoint(TRUNCATE)`)
	return err
}
