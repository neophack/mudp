package store

import (
	"encoding/json"
	"strings"
	"time"
)

// Access events recorded by the security monitor. page_view is recorded when
// the login page is opened; login_success / login_failed for password attempts.
const (
	AccessEventPageView     = "page_view"
	AccessEventLoginSuccess = "login_success"
	AccessEventLoginFailed  = "login_failed"
)

// AccessLog is one recorded login-page visit or authentication attempt. It
// powers the admin security dashboard: where visitors come from (geo + map),
// what they use (browser/OS/device), and whether a login attempt succeeded.
// Passwords are never stored — only the username and the outcome.
type AccessLog struct {
	ID       int64  `json:"id"`
	Event    string `json:"event"`
	Username string `json:"username,omitempty"`
	IP       string `json:"ip"`
	// PublicIP is the visitor's own WAN address, probed in-browser via
	// WebRTC/STUN. On an intranet deployment the server only sees the last-hop
	// private address (IP), which has no GeoIP answer; this field recovers the
	// real location for the access map. Empty when WebRTC is unavailable.
	PublicIP      string  `json:"publicIP,omitempty"`
	Country       string  `json:"country,omitempty"`
	CountryCode   string  `json:"countryCode,omitempty"`
	Region        string  `json:"region,omitempty"`
	City          string  `json:"city,omitempty"`
	Latitude      float64 `json:"latitude,omitempty"`
	Longitude     float64 `json:"longitude,omitempty"`
	Timezone      string  `json:"timezone,omitempty"` // IP-derived timezone
	ISP           string  `json:"isp,omitempty"`
	Browser       string  `json:"browser,omitempty"`
	OS            string  `json:"os,omitempty"`
	DeviceType    string  `json:"deviceType,omitempty"`
	UserAgent     string  `json:"userAgent,omitempty"`
	Referer       string  `json:"referer,omitempty"`
	Success       bool    `json:"success"`
	FailureReason string  `json:"failureReason,omitempty"`
	// VPN/proxy/hosting detection from the GeoIP provider. A VPN's IP is a
	// datacenter address, so these flag the connection as anonymised.
	IsProxy   bool   `json:"isProxy"`
	IsHosting bool   `json:"isHosting"`
	ProxyType string `json:"proxyType,omitempty"` // vpn/proxy/tor/hosting
	// Suspicious flags derived by comparing server- and client-supplied data:
	// TZMismatch means the browser's local timezone disagrees with the IP's
	// timezone (a strong VPN tell, since the local clock travels with the user).
	Suspicious string `json:"suspicious,omitempty"`
	// Client-supplied signals (collected in-browser; they bypass a VPN because
	// they describe the actual device, not the tunnel's exit node).
	ClientTimezone string `json:"clientTimezone,omitempty"`
	ClientLanguage string `json:"clientLanguage,omitempty"`
	ClientScreen   string `json:"clientScreen,omitempty"`
	ClientPlatform string `json:"clientPlatform,omitempty"`
	ClientCPUCore  int    `json:"clientCpuCore,omitempty"`
	ClientMemoryGB int    `json:"clientMemoryGB,omitempty"`
	ClientTouch    bool   `json:"clientTouch"`
	ClientDNT      bool   `json:"clientDnt"`
	CreatedAt      string `json:"createdAt"`
}

// AccessLogFilter narrows AccessLogs results. Empty fields match everything.
type AccessLogFilter struct {
	Event          string
	IP             string
	Username       string
	Q              string // free-text, matches ip/username/city/country/user_agent
	SuspiciousOnly bool   // restrict to entries flagged vpn/tz-mismatch/proxy
	Limit          int
	Offset         int
}

// GeoPoint is a deduplicated access location for map rendering: each distinct
// lat/lng with its label and how many visits originated there.
type GeoPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Label     string  `json:"label"`
	Country   string  `json:"country,omitempty"`
	City      string  `json:"city,omitempty"`
	Count     int     `json:"count"`
}

// AccessStats summarises the access log for the dashboard header cards and the
// "top sources" breakdowns.
type AccessStats struct {
	TotalVisits  int           `json:"totalVisits"`
	LoginSuccess int           `json:"loginSuccess"`
	LoginFailed  int           `json:"loginFailed"`
	UniqueIPs    int           `json:"uniqueIPs"`
	VPNProxy     int           `json:"vpnProxy"`   // IPs detected as vpn/proxy/hosting
	Suspicious   int           `json:"suspicious"` // entries with any suspicious marker
	TopCountries []AccessCount `json:"topCountries"`
	TopIPs       []AccessCount `json:"topIPs"`
	TopBrowsers  []AccessCount `json:"topBrowsers"`
	TopOS        []AccessCount `json:"topOS"`
	HourlyTrend  []AccessTrend `json:"hourlyTrend"`
}

// AccessCount is a label/count pair used for the "top N" breakdowns.
type AccessCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// AccessTrend is one bucket of a time series.
type AccessTrend struct {
	Bucket string `json:"bucket"`
	Count  int    `json:"count"`
}

// migrateCreateAccessLogs creates the access_logs table backing the security
// monitor. Each row is one page view or one authentication attempt.
func migrateCreateAccessLogs(db executor) error {
	stmts := []string{
		`create table if not exists access_logs (
			id integer primary key autoincrement,
			event text not null,
			username text not null default '',
			ip text not null default '',
			country text not null default '',
			country_code text not null default '',
			region text not null default '',
			city text not null default '',
			latitude real,
			longitude real,
			timezone text not null default '',
			isp text not null default '',
			browser text not null default '',
			os text not null default '',
			device_type text not null default '',
			user_agent text not null default '',
			referer text not null default '',
			success integer not null default 0,
			failure_reason text not null default '',
			created_at text not null
		)`,
		`create index if not exists idx_access_logs_created on access_logs(created_at desc)`,
		`create index if not exists idx_access_logs_ip on access_logs(ip)`,
		`create index if not exists idx_access_logs_event on access_logs(event)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateExtendAccessLogs adds the VPN/proxy detection, timezone-mismatch, and
// client-device-signal columns introduced after the initial table. It uses
// alter-table-add so databases created at v28 are upgraded in place.
func migrateExtendAccessLogs(db executor) error {
	cols := []string{
		`alter table access_logs add column is_proxy integer not null default 0`,
		`alter table access_logs add column is_hosting integer not null default 0`,
		`alter table access_logs add column proxy_type text not null default ''`,
		`alter table access_logs add column suspicious text not null default ''`,
		`alter table access_logs add column client_timezone text not null default ''`,
		`alter table access_logs add column client_language text not null default ''`,
		`alter table access_logs add column client_screen text not null default ''`,
		`alter table access_logs add column client_platform text not null default ''`,
		`alter table access_logs add column client_cpu_core integer not null default 0`,
		`alter table access_logs add column client_memory_gb integer not null default 0`,
		`alter table access_logs add column client_touch integer not null default 0`,
		`alter table access_logs add column client_dnt integer not null default 0`,
	}
	for _, c := range cols {
		if err := execIgnoring(db, c, sqliteDuplicateColumn); err != nil {
			return err
		}
	}
	return nil
}

// migrateExtendAccessLogsPublicIP adds the browser-reported WAN address column.
// On an intranet deployment the server's view of the source IP is a private
// address with no GeoIP answer, so the access map would be empty; the
// WebRTC/STUN-reflexive public IP recovers the real location.
func migrateExtendAccessLogsPublicIP(db executor) error {
	return execIgnoring(db,
		`alter table access_logs add column public_ip text not null default ''`,
		sqliteDuplicateColumn)
}

// RecordAccess persists one access-log entry. Best-effort: it never returns an
// error so the surrounding handler is unaffected, mirroring Audit().
func (db *DB) RecordAccess(log AccessLog) {
	if log.Event == "" {
		log.Event = AccessEventPageView
	}
	if log.CreatedAt == "" {
		log.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, _ = db.Exec(`insert into access_logs(
		event, username, ip, public_ip, country, country_code, region, city,
		latitude, longitude, timezone, isp,
		browser, os, device_type, user_agent, referer,
		success, failure_reason,
		is_proxy, is_hosting, proxy_type, suspicious,
		client_timezone, client_language, client_screen, client_platform,
		client_cpu_core, client_memory_gb, client_touch, client_dnt,
		created_at
	) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		log.Event, log.Username, log.IP, log.PublicIP, log.Country, log.CountryCode, log.Region, log.City,
		nullFloat(log.Latitude), nullFloat(log.Longitude), log.Timezone, log.ISP,
		log.Browser, log.OS, log.DeviceType, log.UserAgent, log.Referer,
		log.Success, log.FailureReason,
		log.IsProxy, log.IsHosting, log.ProxyType, log.Suspicious,
		log.ClientTimezone, log.ClientLanguage, log.ClientScreen, log.ClientPlatform,
		log.ClientCPUCore, log.ClientMemoryGB, log.ClientTouch, log.ClientDNT,
		log.CreatedAt,
	)
}

// nullFloat converts 0 (the empty value for a coordinate) to SQL NULL so the
// map renderer can tell "no location" apart from "equator/prime meridian".
func nullFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

// AccessLogs returns recent access entries matching the filter, newest first.
func (db *DB) AccessLogs(f AccessLogFilter) ([]AccessLog, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var (
		clauses []string
		args    []any
	)
	if f.Event != "" {
		clauses = append(clauses, "event = ?")
		args = append(args, f.Event)
	}
	if f.IP != "" {
		clauses = append(clauses, "ip like ?")
		args = append(args, "%"+f.IP+"%")
	}
	if f.Username != "" {
		clauses = append(clauses, "username like ?")
		args = append(args, "%"+f.Username+"%")
	}
	if f.Q != "" {
		q := "%" + f.Q + "%"
		clauses = append(clauses, "(ip like ? or username like ? or city like ? or country like ? or user_agent like ?)")
		args = append(args, q, q, q, q, q)
	}
	if f.SuspiciousOnly {
		// Any of: detected proxy/hosting, or a non-empty suspicious marker.
		clauses = append(clauses, "(is_proxy=1 or is_hosting=1 or suspicious != '')")
	}
	q := `select id, event, username, ip, public_ip, country, country_code, region, city,
		coalesce(latitude,0), coalesce(longitude,0), timezone, isp,
		browser, os, device_type, user_agent, referer, success, failure_reason,
		is_proxy, is_hosting, proxy_type, suspicious,
		client_timezone, client_language, client_screen, client_platform,
		client_cpu_core, client_memory_gb, client_touch, client_dnt,
		created_at
		from access_logs`
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
	var out []AccessLog
	for rows.Next() {
		var l AccessLog
		if err := rows.Scan(&l.ID, &l.Event, &l.Username, &l.IP, &l.PublicIP, &l.Country, &l.CountryCode,
			&l.Region, &l.City, &l.Latitude, &l.Longitude, &l.Timezone, &l.ISP,
			&l.Browser, &l.OS, &l.DeviceType, &l.UserAgent, &l.Referer, &l.Success, &l.FailureReason,
			&l.IsProxy, &l.IsHosting, &l.ProxyType, &l.Suspicious,
			&l.ClientTimezone, &l.ClientLanguage, &l.ClientScreen, &l.ClientPlatform,
			&l.ClientCPUCore, &l.ClientMemoryGB, &l.ClientTouch, &l.ClientDNT,
			&l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AccessLogGeoPoints returns deduplicated geographic locations (latitude and
// longitude both set), aggregated by visit count and ordered by activity.
// Used to render the access map.
func (db *DB) AccessLogGeoPoints(limit int) ([]GeoPoint, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := db.Query(`select latitude, longitude,
		coalesce(country,''), coalesce(city,''),
		count(*) cnt
		from access_logs
		where latitude is not null and longitude is not null
		group by round(latitude,2), round(longitude,2)
		order by cnt desc
		limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GeoPoint
	for rows.Next() {
		var p GeoPoint
		if err := rows.Scan(&p.Latitude, &p.Longitude, &p.Country, &p.City, &p.Count); err != nil {
			return nil, err
		}
		p.Label = geoLabel(p.City, p.Country)
		out = append(out, p)
	}
	return out, rows.Err()
}

func geoLabel(city, country string) string {
	switch {
	case city != "" && country != "":
		return city + ", " + country
	case city != "":
		return city
	case country != "":
		return country
	}
	return "Unknown"
}

// AccessStats summarises the access log for the dashboard.
func (db *DB) AccessStats() (AccessStats, error) {
	var s AccessStats
	if err := db.QueryRow(`select count(*),
		(select count(*) from access_logs where event='login_success'),
		(select count(*) from access_logs where event='login_failed'),
		(select count(distinct ip) from access_logs where ip != ''),
		(select count(distinct ip) from access_logs where is_proxy=1 or is_hosting=1),
		(select count(*) from access_logs where is_proxy=1 or is_hosting=1 or suspicious != '')
		from access_logs`).Scan(&s.TotalVisits, &s.LoginSuccess, &s.LoginFailed, &s.UniqueIPs, &s.VPNProxy, &s.Suspicious); err != nil {
		return s, err
	}
	s.TopCountries = accessTopCounts(db, `select coalesce(nullif(country_code,''),'Unknown'), count(*) from access_logs where event in ('login_success','login_failed') group by country_code order by count(*) desc limit 5`)
	// Top IPs are ranked by the visitor's real WAN address when available: on an
	// intranet deployment the server's `ip` is a private last-hop address, so
	// grouping by it collapses the whole list to one LAN entry. The browser-
	// reported public_ip (WebRTC/STUN) recovers the actual source — mirroring
	// how recordAccess already derives the geography. nullif/coalesce give the
	// "effective IP": public_ip if present, else ip; blank effective IPs are
	// excluded so the count isn't padded by unlocatable private hops.
	const effectiveIP = "coalesce(nullif(public_ip,''), ip)"
	s.TopIPs = accessTopCounts(db, "select "+effectiveIP+", count(*) from access_logs where "+effectiveIP+" != '' group by "+effectiveIP+" order by count(*) desc limit 5")
	s.TopBrowsers = accessTopCounts(db, `select coalesce(nullif(browser,''),'Unknown'), count(*) from access_logs group by browser order by count(*) desc limit 5`)
	s.TopOS = accessTopCounts(db, `select coalesce(nullif(os,''),'Unknown'), count(*) from access_logs group by os order by count(*) desc limit 5`)
	s.HourlyTrend = accessTrend(db)
	return s, nil
}

func accessTopCounts(db *DB, query string) []AccessCount {
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AccessCount
	for rows.Next() {
		var c AccessCount
		if err := rows.Scan(&c.Label, &c.Count); err != nil {
			return nil
		}
		out = append(out, c)
	}
	return out
}

// accessTrend returns the last 24 hourly buckets of access activity, oldest
// first. SQLite's strftime with the 'unixepoch' modifier keeps buckets stable
// across DST transitions.
func accessTrend(db *DB) []AccessTrend {
	rows, err := db.Query(`select strftime('%Y-%m-%dT%H:00:00', created_at) bucket, count(*)
		from access_logs
		where created_at >= datetime('now','-24 hours')
		group by bucket order by bucket asc`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AccessTrend
	for rows.Next() {
		var t AccessTrend
		if err := rows.Scan(&t.Bucket, &t.Count); err != nil {
			return nil
		}
		out = append(out, t)
	}
	return out
}

// PruneAccessLogs deletes access entries older than the given time.
func (db *DB) PruneAccessLogs(before time.Time) error {
	_, err := db.Exec(`delete from access_logs where created_at < ?`, before.Format(time.RFC3339))
	return err
}

// ---------------- Security monitor settings ----------------

const securitySettingKey = "security_monitor"

// SecuritySettings is the admin-controlled configuration for the access
// monitor. Defaults are chosen so a fresh install records everything (it is a
// security feature), with finer controls for an admin who wants to scale it
// back — e.g. on an air-gapped host where the GeoIP lookup would only time out.
type SecuritySettings struct {
	// Enabled is the master switch. When off, no access entries are recorded and
	// the admin handlers return empty results.
	Enabled bool `json:"enabled"`
	// GeoIPLookup overrides the MUDP_GEOIP_LOOKUP env default at runtime. Off
	// skips the network call and leaves location fields blank.
	GeoIPLookup bool `json:"geoipLookup"`
	// VPNDetect requests the proxy/hosting fields from the GeoIP provider and
	// flags anonymised connections.
	VPNDetect bool `json:"vpnDetect"`
	// CollectClient toggles whether the login page POSTs device hints (timezone,
	// screen, CPU). Off records server-side data only.
	CollectClient bool `json:"collectClient"`
	// RetentionDays bounds how long entries are kept before the background
	// pruner deletes them. 0 falls back to the built-in 90-day default.
	RetentionDays int `json:"retentionDays"`

	// IPWorkerURL is the base URL of the operator's self-hosted Cloudflare
	// Worker that the browser hits (/whoami) to recover the visitor's own
	// public IP + geo (from Cloudflare's request.cf). Empty = worker not
	// deployed; the front end then falls back to WebRTC. The Worker is
	// unauthenticated (it only ever reflects the caller's own address), so
	// there is no password/secret here — and no server-side /lookup: the
	// server resolves arbitrary IPs (for access-log geography) directly via
	// ip-api.com, independent of this Worker.
	IPWorkerURL string `json:"ipWorkerUrl"`
}

// DefaultSecuritySettings returns the recommended defaults for a new install.
func DefaultSecuritySettings() SecuritySettings {
	return SecuritySettings{
		Enabled:       true,
		GeoIPLookup:   true,
		VPNDetect:     true,
		CollectClient: true,
		RetentionDays: 90,
	}
}

// SecuritySettings returns the stored configuration, or the defaults when unset.
func (db *DB) SecuritySettings() (SecuritySettings, error) {
	cfg := DefaultSecuritySettings()
	raw, err := db.getSetting(securitySettingKey)
	if err != nil {
		// No row yet: return defaults rather than erroring.
		return cfg, nil
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultSecuritySettings(), nil
	}
	return cfg, nil
}

// SaveSecuritySettings replaces the stored configuration.
func (db *DB) SaveSecuritySettings(cfg SecuritySettings) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return db.setSetting(securitySettingKey, string(raw))
}
