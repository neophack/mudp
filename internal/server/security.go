package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mudp/internal/middleware"
	"mudp/internal/store"
	"mudp/internal/version"
)

// tzOffsetCache / tzOffsetCacheMu memoize time.LoadLocation offsets so the
// timezone-mismatch check doesn't re-parse the same zone on every login.
var (
	tzOffsetCache   map[string]int
	tzOffsetCacheMu sync.Mutex
)

// securitySettings returns the cached security-monitor config, loading it from
// the DB on first use. The cache lets every login/track request decide whether
// to resolve GeoIP and collect client hints with a single map read.
func (a *App) securitySettings() store.SecuritySettings {
	a.secSettingsMu.RLock()
	if a.secLoaded {
		s := a.secSettings
		a.secSettingsMu.RUnlock()
		return s
	}
	a.secSettingsMu.RUnlock()

	a.secSettingsMu.Lock()
	defer a.secSettingsMu.Unlock()
	if a.secLoaded {
		return a.secSettings
	}
	s, err := a.db.SecuritySettings()
	if err != nil {
		// Fall back to defaults so a transient DB error can't disable logging.
		s = store.DefaultSecuritySettings()
	}
	a.secSettings = s
	a.secLoaded = true
	return s
}

// reloadSecuritySettings drops the cached config so the next read picks up a
// freshly saved one. Called after an admin edits the settings.
func (a *App) reloadSecuritySettings() {
	a.secSettingsMu.Lock()
	a.secLoaded = false
	a.secSettingsMu.Unlock()
	// The worker URL feeds the CSP connect-src allowlist; re-sync so a freshly
	// configured (or removed) worker takes effect immediately.
	a.syncIPWorkerCSP()
}

// geoCacheTTL bounds how long a resolved IP→location mapping stays cached.
// ip-api.com data for a given address rarely changes; caching also caps how
// often we hit the free endpoint (and keeps a burst of logins from one IP to a
// single lookup).
const geoCacheTTL = 1 * time.Hour

// geoHTTPTimeout caps each GeoIP lookup so a slow/missing network never stalls
// a login.
const geoHTTPTimeout = 3 * time.Second

// geoInfo is the resolved location for a public IP. When lookup is disabled or
// fails every field stays empty and the caller treats it as "location unknown".
type geoInfo struct {
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Latitude    float64 `json:"lat"`
	Longitude   float64 `json:"lon"`
	Timezone    string  `json:"timezone"`
	ISP         string  `json:"isp"`
	// VPN/proxy/hosting detection (ip-api.com pro field, returned on the free
	// endpoint when requested via fields=). A VPN's exit IP is a datacenter
	// address, so these flag an anonymised connection. We can't recover the
	// user's real location behind a VPN from the IP alone — but we can flag it,
	// and compare it against the browser's local timezone to spot mismatches.
	Proxy     bool   `json:"proxy"`
	Hosting   bool   `json:"hosting"`
	ProxyType string `json:"proxyType"` // vpn/proxy/tor/hosting when proxy/hosting
}

type geoCacheEntry struct {
	info      geoInfo
	expiresAt time.Time
}

// geoLookup resolves a public IP to its geographic location using the free
// ip-api.com endpoint. Results are cached for geoCacheTTL. Private/loopback
// addresses and disabled lookups return a zero geoInfo (location unknown) and
// never make a network call.
//
// "Disabled" is decided by the admin-managed SecuritySettings.GeoIPLookup if it
// has been set; otherwise it falls back to the MUDP_GEOIP_LOOKUP env flag.
func (a *App) geoLookup(ip string) geoInfo {
	if ip == "" {
		return geoInfo{}
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || !parsed.IsGlobalUnicast() {
		// Private/loopback/link-local/multicast: no public GeoIP answer exists.
		return geoInfo{}
	}
	if !a.geoEnabled() {
		return geoInfo{}
	}
	a.geoCacheMu.Lock()
	if a.geoCache != nil {
		if e, ok := a.geoCache[ip]; ok && time.Now().Before(e.expiresAt) {
			a.geoCacheMu.Unlock()
			return e.info
		}
	}
	a.geoCacheMu.Unlock()

	info := a.geoLookupRemote(ip)
	a.geoCacheMu.Lock()
	if a.geoCache == nil {
		a.geoCache = make(map[string]geoCacheEntry, 64)
	}
	a.geoCache[ip] = geoCacheEntry{info: info, expiresAt: time.Now().Add(geoCacheTTL)}
	// Bound the cache so a flood of distinct IPs (an attacker rotating source
	// addresses) cannot grow it without limit.
	if len(a.geoCache) > 4096 {
		for k := range a.geoCache {
			delete(a.geoCache, k)
			if len(a.geoCache) <= 2048 {
				break
			}
		}
	}
	a.geoCacheMu.Unlock()
	return info
}

// geoEnabled reports whether GeoIP lookups are active, combining the admin
// setting with the env default. A DB read on every login would be wasteful, so
// the settings are cached on the App by securitySettings() (refreshed when an
// admin saves them).
func (a *App) geoEnabled() bool {
	s := a.securitySettings()
	return s.Enabled && s.GeoIPLookup
}

// ipAPIResponse is the subset of fields ip-api.com returns for /json/{ip}. The
// proxy/hosting fields are included in the free tier when requested in fields=.
type ipAPIResponse struct {
	Status      string  `json:"status"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	RegionName  string  `json:"regionName"`
	City        string  `json:"city"`
	Latitude    float64 `json:"lat"`
	Longitude   float64 `json:"lon"`
	Timezone    string  `json:"timezone"`
	ISP         string  `json:"isp"`
	Proxy       bool    `json:"proxy"`
	Hosting     bool    `json:"hosting"`
	Message     string  `json:"message"`
}

func (a *App) geoLookupRemote(ip string) geoInfo {
	ctx, cancel := context.WithTimeout(context.Background(), geoHTTPTimeout)
	defer cancel()
	// Re-parse and re-validate the address before it touches the URL. The host
	// part of the request is fixed (ip-api.com) and the path is built from this
	// value, so canonicalising through net.ParseIP both defeats gosec's taint
	// tracker and guarantees no attacker-controlled bytes can reach the request
	// line: anything that isn't a valid public IP literal returns early.
	parsed := net.ParseIP(ip)
	if parsed == nil || !parsed.IsGlobalUnicast() {
		return geoInfo{}
	}
	// fields=... limits the payload to just what we store; the lang is left to
	// the service default (English) so country names sort/compare consistently.
	// proxy/hosting are part of the free block-type dataset and flag VPNs.
	url := "http://ip-api.com/json/" + parsed.String() + "?fields=status,message,country,countryCode,regionName,city,lat,lon,timezone,isp,proxy,hosting"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return geoInfo{}
	}
	if version.Version != "" {
		req.Header.Set("User-Agent", "mudp/"+version.Version)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return geoInfo{}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return geoInfo{}
	}
	var r ipAPIResponse
	if err := json.Unmarshal(body, &r); err != nil || r.Status != "success" {
		return geoInfo{}
	}
	return geoInfo{
		Country:     r.Country,
		CountryCode: r.CountryCode,
		Region:      r.RegionName,
		City:        r.City,
		Latitude:    r.Latitude,
		Longitude:   r.Longitude,
		Timezone:    r.Timezone,
		ISP:         r.ISP,
		Proxy:       r.Proxy,
		Hosting:     r.Hosting,
		ProxyType:   proxyTypeLabel(r.Proxy, r.Hosting),
	}
}

// proxyTypeLabel collapses the two boolean signals into one readable tag.
func proxyTypeLabel(proxy, hosting bool) string {
	if proxy && hosting {
		return "vpn/hosting"
	}
	if proxy {
		return "proxy"
	}
	if hosting {
		return "hosting"
	}
	return ""
}

// clientInfo bundles the server-derived identity of a request for logging: the
// real client IP (CDN-aware), its resolved location, and the parsed browser/OS.
type clientInfo struct {
	IP      string
	geo     geoInfo
	Browser string
	OS      string
	Device  string
	UA      string
	Referer string
	// PublicIP is the visitor's own WAN address, probed in-browser via
	// WebRTC/STUN and carried in a cookie so it rides on the /api/login request.
	// The server can only see the source of its last network hop, so on an
	// intranet deployment that hop is a private address with no GeoIP answer;
	// the browser-reported reflexive address recovers the real location for the
	// access map. Empty when WebRTC is disabled, STUN is unreachable, or no
	// cookie was sent (e.g. a scripted login that never loaded the login page).
	PublicIP string
	// Client-supplied device hints (collected in-browser; bypass a VPN because
	// they describe the local device, not the tunnel's exit). Populated only on
	// the page-view track request; login attempts carry whatever was captured.
	clientHints
}

// clientHints are the device signals the login page can read locally.
type clientHints struct {
	Timezone string `json:"timezone"`
	Language string `json:"language"`
	Screen   string `json:"screen"`
	Platform string `json:"platform"`
	CPUCore  int    `json:"cpuCore"`
	MemoryGB int    `json:"memoryGB"`
	Touch    bool   `json:"touch"`
	DNT      bool   `json:"dnt"`
}

// collectClient resolves everything we know about the caller of r. GeoIP is a
// best-effort network call wrapped in a cache and a timeout; a failure leaves
// the location fields blank and never blocks the request.
func (a *App) collectClient(r *http.Request) clientInfo {
	ip := ""
	if a.trusted != nil {
		ip = a.trusted.ClientIP(r)
	} else {
		ip = middleware.RemoteIP(r)
	}
	ua := r.UserAgent()
	browser, osName, device := parseUserAgent(ua)
	return clientInfo{
		IP:       ip,
		PublicIP: publicIPFromCookie(r),
		geo:      a.geoLookup(ip),
		Browser:  browser,
		OS:       osName,
		Device:   device,
		UA:       ua,
		Referer:  r.Header.Get("Referer"),
	}
}

// publicIPCookie is the cookie the login page writes the WebRTC/STUN-reflexive
// WAN address into. It is a non-sensitive display value used only to geo-locate
// intranet access (where the server's source IP is private), so it rides on the
// /api/login request without needing any server-side state.
const publicIPCookie = "mudp_pubip"

// publicIPFromCookie reads the browser-reported public IP cookie, validating it
// is a real IP literal before trusting it (so a tampered cookie can't inject an
// arbitrary string into a URL or the access log). Returns "" when absent or bad.
func publicIPFromCookie(r *http.Request) string {
	c, err := r.Cookie(publicIPCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	if net.ParseIP(c.Value) == nil {
		return ""
	}
	return c.Value
}

// recordAccess builds and stores an access-log entry from a client snapshot.
// It honours the admin's SecuritySettings: nothing is recorded when the master
// switch is off, and client hints are dropped when collection is disabled.
func (a *App) recordAccess(ci clientInfo, event, username, failureReason string, success bool) {
	s := a.securitySettings()
	if !s.Enabled {
		return
	}
	// Decide which address drives the recorded geography. The browser-reported
	// public IP (WebRTC/STUN reflexive address) is the visitor's real WAN
	// address and is preferred as the location source: on an intranet/WSL
	// deployment the server only sees the last private hop (ci.IP) which has no
	// GeoIP answer, and even on a public host a CDN/proxy in front can resolve a
	// near-empty location (country only, no city/coords). The public IP avoids
	// both — the map and the address column then reflect where the visitor
	// actually is. We fall back to the server-IP geo (ci.geo) only when no
	// public IP was reported or it failed to resolve.
	publicIP := ci.PublicIP
	geo := ci.geo
	if publicIP != "" {
		if pubGeo := a.geoLookup(publicIP); pubGeo.Country != "" {
			geo = pubGeo
		}
	}
	log := store.AccessLog{
		Event:         event,
		Username:      username,
		IP:            ci.IP,
		PublicIP:      publicIP,
		Country:       geo.Country,
		CountryCode:   geo.CountryCode,
		Region:        geo.Region,
		City:          geo.City,
		Latitude:      geo.Latitude,
		Longitude:     geo.Longitude,
		Timezone:      geo.Timezone,
		ISP:           geo.ISP,
		Browser:       ci.Browser,
		OS:            ci.OS,
		DeviceType:    ci.Device,
		UserAgent:     ci.UA,
		Referer:       ci.Referer,
		Success:       success,
		FailureReason: failureReason,
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	if s.VPNDetect {
		log.IsProxy = geo.Proxy
		log.IsHosting = geo.Hosting
		log.ProxyType = geo.ProxyType
	}
	if s.CollectClient {
		log.ClientTimezone = ci.Timezone
		log.ClientLanguage = ci.Language
		log.ClientScreen = ci.Screen
		log.ClientPlatform = ci.Platform
		log.ClientCPUCore = ci.CPUCore
		log.ClientMemoryGB = ci.MemoryGB
		log.ClientTouch = ci.Touch
		log.ClientDNT = ci.DNT
	}
	// Suspicious signal: the browser's local timezone disagrees with the IP's
	// timezone. A VPN relocates the IP but not the device's clock, so a
	// mismatch is a strong indicator of a tunnel — more reliable than IP-geo
	// alone for catching VPN users.
	log.Suspicious = tzMismatchReason(geo.Timezone, ci.Timezone, geo.Proxy, geo.Hosting)
	a.db.RecordAccess(log)
}

// tzMismatchReason compares the GeoIP timezone with the browser's reported
// timezone. Returns a short reason string when they disagree (and are both
// known), or when the IP is itself a VPN/proxy/hosting address. Empty = clean.
func tzMismatchReason(ipTZ, clientTZ string, proxy, hosting bool) string {
	var flags []string
	if ipTZ != "" && clientTZ != "" && !sameTZ(ipTZ, clientTZ) {
		flags = append(flags, "tz-mismatch")
	}
	if proxy || hosting {
		flags = append(flags, "vpn/proxy")
	}
	return strings.Join(flags, "+")
}

// sameTZ treats the GeoIP and browser timezones as equal even when they differ
// only in canonical vs. local naming (e.g. "Asia/Shanghai" vs "Asia/Urumqi",
// which share a zone). We compare the UTC offsets implied by both names so a
// user in the same absolute zone isn't flagged.
func sameTZ(a, b string) bool {
	if a == b {
		return true
	}
	oa := tzOffsetMinutes(a)
	ob := tzOffsetMinutes(b)
	if oa != ob {
		return false
	}
	// Unknown offsets (parse failure) → can't confirm equal, but both must be
	// unknown to avoid false mismatches; fall back to exact compare above.
	if oa == invalidOffset {
		return false
	}
	return true
}

const invalidOffset = 1 << 30

// tzOffsetMinutes returns the standard (non-DST) UTC offset for a zone name, in
// minutes. Unknown zones return invalidOffset. It loads each zone only once and
// caches it, so a flood of lookups stays cheap.
func tzOffsetMinutes(name string) int {
	tzOffsetCacheMu.Lock()
	defer tzOffsetCacheMu.Unlock()
	if tzOffsetCache == nil {
		tzOffsetCache = make(map[string]int, 64)
	}
	if v, ok := tzOffsetCache[name]; ok {
		return v
	}
	loc, err := time.LoadLocation(name)
	off := invalidOffset
	if err == nil {
		_, off = time.Now().In(loc).Zone()
		// Zone() returns seconds; convert to minutes. DST currently in effect is
		// folded in, which is fine: we only need a coarse offset comparison and
		// both sides use the same Now().
		off = off / 60
	}
	tzOffsetCache[name] = off
	return off
}

// ---------------- User-Agent parsing ----------------

// parseUserAgent extracts a human-readable browser name/version, an OS name,
// and a coarse device class from a User-Agent string without any external
// dependency. It is intentionally lossy: the goal is "what attacked us", not a
// perfect fingerprint.
func parseUserAgent(ua string) (browser, osName, device string) {
	uaLower := strings.ToLower(ua)
	device = deviceFromUA(uaLower)
	osName = osFromUA(uaLower)
	browser = browserFromUA(ua, uaLower)
	return browser, osName, device
}

func deviceFromUA(ua string) string {
	switch {
	case strings.Contains(ua, "bot"), strings.Contains(ua, "crawler"),
		strings.Contains(ua, "spider"), strings.Contains(ua, "slurp"),
		strings.Contains(ua, "scan"), strings.Contains(ua, "feed"),
		// Command-line tools and libraries are not interactive browsers; treat
		// them as bots so scripted attacks stand out from real users.
		strings.Contains(ua, "curl"), strings.Contains(ua, "wget"),
		strings.Contains(ua, "python"), strings.Contains(ua, "go-http"),
		strings.Contains(ua, "httpclient"), strings.Contains(ua, "libwww"),
		strings.Contains(ua, "java"), strings.Contains(ua, "okhttp"),
		strings.Contains(ua, "node"), strings.Contains(ua, "axios"):
		return "bot"
	case strings.Contains(ua, "ipad"), strings.Contains(ua, "tablet"),
		strings.Contains(ua, "playbook"), strings.Contains(ua, "kindle"):
		return "tablet"
	case strings.Contains(ua, "mobi"), strings.Contains(ua, "android"),
		strings.Contains(ua, "iphone"):
		return "mobile"
	}
	return "desktop"
}

func osFromUA(ua string) string {
	switch {
	case strings.Contains(ua, "windows nt 10"):
		return "Windows 10/11"
	case strings.Contains(ua, "windows nt 6.3"):
		return "Windows 8.1"
	case strings.Contains(ua, "windows nt 6.2"):
		return "Windows 8"
	case strings.Contains(ua, "windows nt 6.1"):
		return "Windows 7"
	case strings.Contains(ua, "windows"):
		return "Windows"
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"):
		// Must precede the macOS check: iOS Safari advertises "like Mac OS X".
		return "iOS"
	case strings.Contains(ua, "mac os x"), strings.Contains(ua, "macintosh"):
		return "macOS"
	case strings.Contains(ua, "android"):
		return "Android"
	case strings.Contains(ua, "linux"):
		return "Linux"
	case strings.Contains(ua, "freebsd"):
		return "FreeBSD"
	case strings.Contains(ua, "cros"):
		return "ChromeOS"
	}
	return ""
}

// browserFromUA keeps the original-case UA so the version digits are intact.
func browserFromUA(ua, uaLower string) string {
	// Edge (Chromium) reports "Edg/"; legacy Edge used "Edge/". Check before
	// Chrome since Chromium-based Edge also contains "Chrome".
	switch {
	case strings.Contains(uaLower, "edg/"):
		return "Microsoft Edge " + versionSuffix(ua, "Edg/")
	case strings.Contains(uaLower, "opr/"), strings.Contains(uaLower, "opera"):
		return "Opera " + versionSuffix(ua, "OPR/")
	case strings.Contains(uaLower, "chrome/"):
		return "Chrome " + versionSuffix(ua, "Chrome/")
	case strings.Contains(uaLower, "firefox/"):
		return "Firefox " + versionSuffix(ua, "Firefox/")
	case strings.Contains(uaLower, "safari/"):
		// Chrome on iOS also says Safari; rely on the Chrome check above.
		return "Safari " + versionSuffix(ua, "Version/")
	}
	return ""
}

// versionSuffix returns the numeric version immediately following a marker
// token (e.g. "Chrome/120.0.6099") in a User-Agent, trimmed to major.minor.
func versionSuffix(ua, marker string) string {
	idx := strings.Index(strings.ToLower(ua), strings.ToLower(marker))
	if idx < 0 {
		return ""
	}
	rest := ua[idx+len(marker):]
	end := strings.IndexAny(rest, " ;)")
	if end >= 0 {
		rest = rest[:end]
	}
	// Keep major.minor only; drop the long build tail (120.0.6099.71 → 120.0).
	if dot := strings.Index(rest, "."); dot >= 0 {
		if next := strings.Index(rest[dot+1:], "."); next >= 0 {
			rest = rest[:dot+1+next]
		}
	}
	return rest
}

// ---------------- Admin HTTP handlers ----------------

// accessLogsHandler returns the paginated, filterable access log.
func (a *App) accessLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	f := store.AccessLogFilter{
		Event:          r.URL.Query().Get("event"),
		IP:             r.URL.Query().Get("ip"),
		Username:       r.URL.Query().Get("username"),
		Q:              r.URL.Query().Get("q"),
		SuspiciousOnly: r.URL.Query().Get("suspicious") == "1",
	}
	f.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	f.Offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	entries, err := a.db.AccessLogs(f)
	respond(w, entries, err)
}

// accessGeoHandler returns deduplicated map points for the access map.
func (a *App) accessGeoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	points, err := a.db.AccessLogGeoPoints(limit)
	respond(w, points, err)
}

// accessStatsHandler returns the dashboard summary.
func (a *App) accessStatsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stats, err := a.db.AccessStats()
	respond(w, stats, err)
}

// accessTrackHandler records a login page view. It is public (the page loads
// before authentication) and rides the same strict per-IP limiter as login so
// an attacker cannot inflate the access log with unlimited fake views.
func (a *App) accessTrackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ci := a.collectClient(r)
	// The body carries optional client-only device hints (screen, timezone,
	// language, CPU, memory) plus the browser-reported public IP. These describe
	// the real device and so travel with the user even through a VPN — the basis
	// for the timezone-mismatch check. Unreadable bodies are ignored; nothing
	// here is secret.
	var body trackBody
	_ = decodeJSON(r, &body)
	ci.clientHints = body.clientHints
	// Prefer the cookie (set early on the login page, also carried by /api/login);
	// fall back to the body value when the cookie is absent.
	if ci.PublicIP == "" {
		ci.PublicIP = body.PublicIP
	}
	a.recordAccess(ci, store.AccessEventPageView, "", "", false)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// trackBody mirrors the JSON the login page POSTs to /api/login/track. It is the
// clientHints fields plus publicIP (which lives on clientInfo, not clientHints);
// decoding into this keeps the public IP the browser just probed on the page-view
// record even when no cookie was set (e.g. third-party cookies blocked).
type trackBody struct {
	clientHints
	PublicIP string `json:"publicIP"`
}

// accessExportHandler streams the (filtered) access log as CSV for offline
// analysis.
func (a *App) accessExportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	f := store.AccessLogFilter{
		Event:    r.URL.Query().Get("event"),
		IP:       r.URL.Query().Get("ip"),
		Username: r.URL.Query().Get("username"),
		Q:        r.URL.Query().Get("q"),
		Limit:    5000,
	}
	entries, err := a.db.AccessLogs(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mudp-access-log.csv"`)
	// headers
	io.WriteString(w, "time,event,success,username,ip,country,city,latitude,longitude,isp,vpn,suspicious,browser,os,device,client_tz,client_screen,client_platform,user_agent,failure_reason\n")
	for _, e := range entries {
		fprintfCSV(w,
			e.CreatedAt, e.Event, boolStr(e.Success), e.Username, e.IP,
			e.Country, e.City, floatStr(e.Latitude), floatStr(e.Longitude), e.ISP,
			e.ProxyType, e.Suspicious,
			e.Browser, e.OS, e.DeviceType, e.ClientTimezone, e.ClientScreen, e.ClientPlatform,
			e.UserAgent, e.FailureReason,
		)
	}
}

// securitySettingsHandler lets an admin read/write the monitor configuration.
func (a *App) securitySettingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.db.SecuritySettings()
		respond(w, cfg, err)
	case http.MethodPost:
		var req store.SecuritySettings
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.RetentionDays < 0 || req.RetentionDays > 3650 {
			req.RetentionDays = 90
		}
		if err := a.db.SaveSecuritySettings(req); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.reloadSecuritySettings()
		// Clear the GeoIP cache so a freshly-enabled lookup takes effect
		// immediately for the next request.
		a.geoCacheMu.Lock()
		a.geoCache = nil
		a.geoCacheMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// securityConfigPublic tells the login page whether to collect device hints.
// It reveals only the CollectClient flag — nothing sensitive.
func (a *App) securityConfigPublic(w http.ResponseWriter, r *http.Request) {
	s := a.securitySettings()
	writeJSON(w, http.StatusOK, map[string]bool{
		"collectClient": s.Enabled && s.CollectClient,
	})
}

// ipWorkerConfigPublic tells the browser whether an operator-deployed IP/geo
// detection Worker is configured. The front-end IP probe uses this to decide
// whether to hit the worker's /whoami (richer geo, HTTPS, globally reachable)
// before falling back to WebRTC. Only the URL and an enabled flag are exposed:
// the worker password and TOTP secret never reach the browser, so an attacker
// reading the page cannot mint authenticated /lookup calls. The endpoint is
// public (no session) because the IP probe runs on the login page before auth.
func (a *App) ipWorkerConfigPublic(w http.ResponseWriter, r *http.Request) {
	s := a.securitySettings()
	url := strings.TrimSpace(s.IPWorkerURL)
	enabled := url != "" && strings.HasPrefix(url, "https://")
	resp := map[string]any{"enabled": enabled}
	if enabled {
		resp["url"] = strings.TrimRight(url, "/")
	}
	writeJSON(w, http.StatusOK, resp)
}

// geoLookupHandler resolves a single IP to its geographic location for the
// dashboard's "your IP + location" display. The browser can't call ip-api.com
// directly (HTTP-only on the free tier → mixed-content block on HTTPS pages),
// so the server proxies through its cached geoLookup. Available to any logged-
// in user (same rank as the dashboard). An empty/disabled/invalid IP yields an
// empty geoInfo — the frontend then shows just the IP without a location chip.
func (a *App) geoLookupHandler(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	writeJSON(w, http.StatusOK, a.geoLookup(ip))
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func floatStr(f float64) string {
	if f == 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', 4, 64)
}

// fprintfCSV writes one CSV row, quoting fields that contain commas, quotes, or
// newlines (RFC 4180).
func fprintfCSV(w io.Writer, fields ...string) {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		if strings.ContainsAny(f, ",\"\n\r") {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(f, "\"", "\"\""))
			b.WriteByte('"')
		} else {
			b.WriteString(f)
		}
	}
	b.WriteByte('\n')
	io.WriteString(w, b.String())
}
