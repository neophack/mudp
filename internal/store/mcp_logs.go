package store

import (
	"database/sql"
	"strings"
	"time"
)

// MCP usage & attack logging. These tables give the console visibility into
// what MCP tokens are actually doing: per-tool-call usage rows power the LOG
// dialog on each token, and rejected requests on the external MCP listener
// become attack rows the admin reviews on the Security page.
//
// Both are bounded by the background pruner (retention: 30 days), mirroring
// access_logs / audit_logs. Writes are best-effort: a logging failure must
// never fail the request it observes, so Record* never returns an error.

// MCPUsageLog is one recorded MCP tool invocation. The args_preview is a
// truncated copy of the JSON-RPC params, enough to see "did what" without
// storing potentially large file contents or command output.
//
// The geo/IP/UA fields are populated only for external (remote) MCP requests,
// where the caller's origin matters for the Security map's green dots. LAN
// requests leave them blank — an operator on the LAN is not an "access" worth
// plotting.
type MCPUsageLog struct {
	ID            int64  `json:"id"`
	TokenID       int64  `json:"tokenId"`
	OwnerID       int64  `json:"ownerId,omitempty"`
	Owner         string `json:"owner,omitempty"`
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
	TokenLabel    string `json:"tokenLabel,omitempty"`
	Tool          string `json:"tool"`
	ArgsPreview   string `json:"argsPreview,omitempty"`
	// Geo/client fields, for the external-access map (green dots).
	IP          string  `json:"ip,omitempty"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"countryCode,omitempty"`
	Region      string  `json:"region,omitempty"`
	City        string  `json:"city,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	ISP         string  `json:"isp,omitempty"`
	Timezone    string  `json:"timezone,omitempty"`
	SourceKind  string  `json:"sourceKind,omitempty"`
	CreatedAt   string  `json:"createdAt"`
}

// MCPUsageFilter narrows MCPUsageLogs results. Empty fields match everything.
// TokenID and ContainerID select one token / one container's calls.
type MCPUsageFilter struct {
	TokenID     int64
	OwnerID     int64
	ContainerID string
	Limit       int
}

// migrateCreateMCPUsageLogs creates the usage-log table. Each row is one
// tools/call dispatched against a container on a token's behalf.
func migrateCreateMCPUsageLogs(db executor) error {
	stmts := []string{
		`create table if not exists mcp_usage_logs (
			id integer primary key autoincrement,
			token_id integer not null,
			owner_id integer not null default 0,
			container_id text not null default '',
			container_name text not null default '',
			token_label text not null default '',
			tool text not null,
			args_preview text not null default '',
			created_at text not null
		)`,
		`create index if not exists idx_mcp_usage_created on mcp_usage_logs(created_at desc)`,
		`create index if not exists idx_mcp_usage_token on mcp_usage_logs(token_id, id desc)`,
		`create index if not exists idx_mcp_usage_container on mcp_usage_logs(container_id, id desc)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateWidenMCPUsageLogs adds the geo/client columns to databases created
// before the external-access map existed. Existing rows get NULL/empty, which
// the map treats as "no location" (skipped). Idempotent via execIgnoring.
func migrateWidenMCPUsageLogs(db executor) error {
	cols := []struct {
		name, ddl string
	}{
		{"ip", "text not null default ''"},
		{"country", "text not null default ''"},
		{"country_code", "text not null default ''"},
		{"city", "text not null default ''"},
		{"latitude", "real"},
		{"longitude", "real"},
	}
	for _, c := range cols {
		if err := execIgnoring(db, `alter table mcp_usage_logs add column `+c.name+` `+c.ddl, sqliteDuplicateColumn); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`create index if not exists idx_mcp_usage_geo on mcp_usage_logs(latitude, longitude)`); err != nil {
		return err
	}
	return nil
}

// RecordMCPUsage persists one tool-call usage entry. Best-effort, mirroring
// Audit(): it never returns an error so the MCP audit hook that calls it cannot
// derail the tool invocation it records.
func (db *DB) RecordMCPUsage(log MCPUsageLog) {
	if log.Tool == "" {
		log.Tool = "tool"
	}
	if log.CreatedAt == "" {
		log.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, _ = db.Exec(`insert into mcp_usage_logs(
		token_id, owner_id, container_id, container_name, token_label, tool, args_preview,
		ip, country, country_code, region, city, latitude, longitude, isp, timezone, source_kind, created_at
	) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		log.TokenID, log.OwnerID, log.ContainerID, log.ContainerName, log.TokenLabel, log.Tool, log.ArgsPreview,
		log.IP, log.Country, log.CountryCode, log.Region, log.City, nullFloat(log.Latitude), nullFloat(log.Longitude), log.ISP, log.Timezone, log.SourceKind, log.CreatedAt,
	)
}

// MCPUsageLogs returns recent usage entries matching the filter, newest first.
// OwnerID is enforced when non-zero so a non-admin caller can only read their
// own token's history.
func (db *DB) MCPUsageLogs(f MCPUsageFilter) ([]MCPUsageLog, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var (
		clauses []string
		args    []any
	)
	if f.TokenID != 0 {
		clauses = append(clauses, "u.token_id = ?")
		args = append(args, f.TokenID)
	}
	if f.OwnerID != 0 {
		clauses = append(clauses, "u.owner_id = ?")
		args = append(args, f.OwnerID)
	}
	if f.ContainerID != "" {
		clauses = append(clauses, "u.container_id = ?")
		args = append(args, f.ContainerID)
	}
	q := `select u.id, u.token_id, u.owner_id, u.container_id, u.container_name, u.token_label,
		u.tool, u.args_preview,
		u.ip, u.country, u.country_code, u.region, u.city, coalesce(u.latitude,0), coalesce(u.longitude,0),
		u.isp, u.timezone, u.source_kind,
		u.created_at,
		coalesce(nullif(us.display_name,''), us.username, '')
		from mcp_usage_logs u
		left join users us on us.id = u.owner_id`
	if len(clauses) > 0 {
		q += " where " + strings.Join(clauses, " and ")
	}
	q += " order by u.id desc limit ?"
	args = append(args, f.Limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMCPUsage(rows)
}

func scanMCPUsage(rows *sql.Rows) ([]MCPUsageLog, error) {
	var out []MCPUsageLog
	for rows.Next() {
		var u MCPUsageLog
		if err := rows.Scan(&u.ID, &u.TokenID, &u.OwnerID, &u.ContainerID, &u.ContainerName, &u.TokenLabel,
			&u.Tool, &u.ArgsPreview,
			&u.IP, &u.Country, &u.CountryCode, &u.Region, &u.City, &u.Latitude, &u.Longitude,
			&u.ISP, &u.Timezone, &u.SourceKind,
			&u.CreatedAt, &u.Owner); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// PruneMCPUsageLogs deletes usage entries older than the given time.
func (db *DB) PruneMCPUsageLogs(before time.Time) error {
	_, err := db.Exec(`delete from mcp_usage_logs where created_at < ?`, before.Format(time.RFC3339))
	return err
}

// MCPAttackLog is one rejected request against the external MCP listener:
// where it came from (IP + geo + UA), what it tried (path), and why it was
// refused (reason). Powers the admin Security page.
type MCPAttackLog struct {
	ID          int64   `json:"id"`
	IP          string  `json:"ip"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"countryCode,omitempty"`
	Region      string  `json:"region,omitempty"`
	City        string  `json:"city,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	ISP         string  `json:"isp,omitempty"`
	Timezone    string  `json:"timezone,omitempty"`
	// SourceKind is "extranet" (public IP) or "intranet" (private/loopback),
	// resolved from the visitor's address so the Security page can label each
	// row as coming from the public internet or a LAN.
	SourceKind string `json:"sourceKind,omitempty"`
	UserAgent  string `json:"userAgent,omitempty"`
	Browser    string `json:"browser,omitempty"`
	OS         string `json:"os,omitempty"`
	Reason     string `json:"reason"`
	Path       string `json:"path,omitempty"`
	CreatedAt  string `json:"createdAt"`
}

// MCPAttackFilter narrows MCPAttackLogs results. Q is free-text, matching
// ip / country / city / reason / path.
type MCPAttackFilter struct {
	IP     string
	Q      string
	Limit  int
	Offset int
}

// MCPAttackStats summarises the attack log for the Security page header cards.
type MCPAttackStats struct {
	TotalAttacks int           `json:"totalAttacks"`
	UniqueIPs    int           `json:"uniqueIPs"`
	Last24h      int           `json:"last24h"`
	TopCountries []AttackCount `json:"topCountries"`
	TopIPs       []AttackCount `json:"topIPs"`
	HourlyTrend  []AttackTrend `json:"hourlyTrend"`
}

// AttackCount is a label/count pair for the "top N" breakdowns.
type AttackCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// AttackTrend is one bucket of a time series.
type AttackTrend struct {
	Bucket string `json:"bucket"`
	Count  int    `json:"count"`
}

// migrateCreateMCPAttackLogs creates the attack-log table backing the Security
// page. Each row is one refused request on the external MCP listener.
func migrateCreateMCPAttackLogs(db executor) error {
	stmts := []string{
		`create table if not exists mcp_attack_logs (
			id integer primary key autoincrement,
			ip text not null default '',
			country text not null default '',
			country_code text not null default '',
			region text not null default '',
			city text not null default '',
			latitude real,
			longitude real,
			isp text not null default '',
			user_agent text not null default '',
			browser text not null default '',
			os text not null default '',
			reason text not null default '',
			path text not null default '',
			created_at text not null
		)`,
		`create index if not exists idx_mcp_attack_created on mcp_attack_logs(created_at desc)`,
		`create index if not exists idx_mcp_attack_ip on mcp_attack_logs(ip)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateWidenMCPLogs adds the Cloudflare-derived geography columns to the MCP
// usage and attack tables: timezone (IANA, from CF-IPTimezone) and source_kind
// ("extranet"/"intranet", classified from the visitor's IP). The usage table
// also gains region/isp, which the attack table already had. Existing rows keep
// their defaults (''), which the Security page treats as "pre-dating this
// field". Idempotent via execIgnoring + sqliteDuplicateColumn.
func migrateWidenMCPLogs(db executor) error {
	cols := []string{
		`alter table mcp_usage_logs add column timezone text not null default ''`,
		`alter table mcp_usage_logs add column source_kind text not null default ''`,
		`alter table mcp_usage_logs add column region text not null default ''`,
		`alter table mcp_usage_logs add column isp text not null default ''`,
		`alter table mcp_attack_logs add column timezone text not null default ''`,
		`alter table mcp_attack_logs add column source_kind text not null default ''`,
	}
	for _, c := range cols {
		if err := execIgnoring(db, c, sqliteDuplicateColumn); err != nil {
			return err
		}
	}
	return nil
}

// RecordMCPAttack persists one rejected external-MCP request. Best-effort,
// mirroring RecordAccess / Audit.
func (db *DB) RecordMCPAttack(log MCPAttackLog) {
	if log.Reason == "" {
		log.Reason = "rejected"
	}
	if log.CreatedAt == "" {
		log.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, _ = db.Exec(`insert into mcp_attack_logs(
		ip, country, country_code, region, city, latitude, longitude, isp,
		timezone, source_kind, user_agent, browser, os, reason, path, created_at
	) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		log.IP, log.Country, log.CountryCode, log.Region, log.City, nullFloat(log.Latitude), nullFloat(log.Longitude), log.ISP,
		log.Timezone, log.SourceKind,
		log.UserAgent, log.Browser, log.OS, log.Reason, log.Path, log.CreatedAt,
	)
}

// MCPAttackLogs returns recent attack entries matching the filter, newest first.
func (db *DB) MCPAttackLogs(f MCPAttackFilter) ([]MCPAttackLog, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var (
		clauses []string
		args    []any
	)
	if f.IP != "" {
		clauses = append(clauses, "ip like ?")
		args = append(args, "%"+f.IP+"%")
	}
	if f.Q != "" {
		q := "%" + f.Q + "%"
		clauses = append(clauses, "(ip like ? or country like ? or city like ? or reason like ? or path like ?)")
		args = append(args, q, q, q, q, q)
	}
	q := `select id, ip, country, country_code, region, city,
		coalesce(latitude,0), coalesce(longitude,0), isp, timezone, source_kind,
		user_agent, browser, os, reason, path, created_at
		from mcp_attack_logs`
	if len(clauses) > 0 {
		q += " where " + strings.Join(clauses, " and ")
	}
	q += " order by id desc limit ? offset ?"
	args = append(args, f.Limit, f.Offset)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPAttackLog
	for rows.Next() {
		var l MCPAttackLog
		if err := rows.Scan(&l.ID, &l.IP, &l.Country, &l.CountryCode, &l.Region, &l.City,
			&l.Latitude, &l.Longitude, &l.ISP, &l.Timezone, &l.SourceKind,
			&l.UserAgent, &l.Browser, &l.OS, &l.Reason, &l.Path, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// MCPAttackStats summarises the attack log for the Security page header.
func (db *DB) MCPAttackStats() (MCPAttackStats, error) {
	var s MCPAttackStats
	if err := db.QueryRow(`select count(*),
		(select count(distinct ip) from mcp_attack_logs where ip != ''),
		(select count(*) from mcp_attack_logs where created_at >= datetime('now','-24 hours'))
		from mcp_attack_logs`).Scan(&s.TotalAttacks, &s.UniqueIPs, &s.Last24h); err != nil {
		return s, err
	}
	s.TopCountries = attackTopCounts(db, `select coalesce(nullif(country_code,''),'Unknown'), count(*) from mcp_attack_logs group by country_code order by count(*) desc limit 5`)
	s.TopIPs = attackTopCounts(db, `select ip, count(*) from mcp_attack_logs where ip != '' group by ip order by count(*) desc limit 5`)
	s.HourlyTrend = attackTrend(db)
	return s, nil
}

func attackTopCounts(db *DB, query string) []AttackCount {
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AttackCount
	for rows.Next() {
		var c AttackCount
		if err := rows.Scan(&c.Label, &c.Count); err != nil {
			return nil
		}
		out = append(out, c)
	}
	return out
}

// attackTrend returns the last 24 hourly buckets of attack activity, oldest
// first. Mirrors accessTrend for consistent DST-stable buckets.
func attackTrend(db *DB) []AttackTrend {
	rows, err := db.Query(`select strftime('%Y-%m-%dT%H:00:00', created_at) bucket, count(*)
		from mcp_attack_logs
		where created_at >= datetime('now','-24 hours')
		group by bucket order by bucket asc`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AttackTrend
	for rows.Next() {
		var t AttackTrend
		if err := rows.Scan(&t.Bucket, &t.Count); err != nil {
			return nil
		}
		out = append(out, t)
	}
	return out
}

// PruneMCPAttackLogs deletes attack entries older than the given time.
func (db *DB) PruneMCPAttackLogs(before time.Time) error {
	_, err := db.Exec(`delete from mcp_attack_logs where created_at < ?`, before.Format(time.RFC3339))
	return err
}

// ---------------- Map geo aggregation ----------------

// MCPMapPoint is one aggregated location on the Security map: how many events
// originated there, and of what kind. Kind is "access" (a successful MCP call,
// green) or "attack" (a refused external request). Severity ranks attacks so the
// map can colour a one-off probe yellow and a repeat offender red.
type MCPMapPoint struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Label       string  `json:"label"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"countryCode,omitempty"`
	City        string  `json:"city,omitempty"`
	Count       int     `json:"count"`
	Kind        string  `json:"kind"`
	// Severity: 1 = low (single/low-frequency attack), 2 = high (repeat offender).
	// Access points carry 0.
	Severity int `json:"severity,omitempty"`
}

// mcpAttackThreshold is the count at which an attack location turns from a
// yellow one-off probe into a red repeat offender. Tuned so a single dropped
// probe reads yellow and a scanner hitting the port repeatedly reads red.
const mcpAttackThreshold = 5

// MCPMapPoints returns aggregated map locations for both successful MCP access
// (green) and rejected external requests (yellow/red), within the retention
// window. Each distinct lat/lon (rounded to 1 decimal ~11km) is one point with
// a kind and, for attacks, a severity derived from its count.
//
// Access points come only from remote usage rows (LAN calls have no geo, so
// they are naturally absent); attack points come only from the attack log.
func (db *DB) MCPMapPoints(limit int) ([]MCPMapPoint, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	out := make([]MCPMapPoint, 0, limit)

	// Green: successful remote MCP tool calls, grouped by location.
	accessRows, err := db.Query(`select round(latitude,1), round(longitude,1),
		coalesce(nullif(country,''),''), coalesce(nullif(country_code,''),''), coalesce(nullif(city,''),''),
		count(*) cnt
		from mcp_usage_logs
		where latitude is not null and longitude is not null
		group by round(latitude,1), round(longitude,1)
		order by cnt desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	for accessRows.Next() {
		var p MCPMapPoint
		if err := accessRows.Scan(&p.Latitude, &p.Longitude, &p.Country, &p.CountryCode, &p.City, &p.Count); err != nil {
			accessRows.Close()
			return nil, err
		}
		p.Kind = "access"
		p.Label = geoLabel(p.City, p.Country)
		out = append(out, p)
	}
	accessRows.Close()
	if err := accessRows.Err(); err != nil {
		return nil, err
	}

	// Yellow/red: refused external requests, grouped by location. Severity is
	// derived from the count so a repeat source shows red.
	attackRows, err := db.Query(`select round(latitude,1), round(longitude,1),
		coalesce(nullif(country,''),''), coalesce(nullif(country_code,''),''), coalesce(nullif(city,''),''),
		count(*) cnt
		from mcp_attack_logs
		where latitude is not null and longitude is not null
		group by round(latitude,1), round(longitude,1)
		order by cnt desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	for attackRows.Next() {
		var p MCPMapPoint
		if err := attackRows.Scan(&p.Latitude, &p.Longitude, &p.Country, &p.CountryCode, &p.City, &p.Count); err != nil {
			attackRows.Close()
			return nil, err
		}
		p.Kind = "attack"
		p.Severity = 1
		if p.Count >= mcpAttackThreshold {
			p.Severity = 2
		}
		p.Label = geoLabel(p.City, p.Country)
		out = append(out, p)
	}
	attackRows.Close()
	if err := attackRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
