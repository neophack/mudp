// Client-side IP detection for the dashboard's "your IP" display.
//
// Two paths, raced in parallel; whichever answers first wins:
//   1. A self-hosted Cloudflare Worker (/whoami) — when the operator deployed
//      one and pointed mudp at it (admin: Security → Settings → IP-detection
//      Worker). This is the preferred path: HTTPS, globally reachable, no rate
//      limit, and it returns full geo (lat/lon/city/...) straight from
//      Cloudflare's edge, so the dashboard can skip a second /api/geo round-trip.
//   2. WebRTC/STUN — always on, no third-party dependency. STUN "srflx"
//      candidates carry the public IP; "host" candidates carry the LAN IP.
//
// We deliberately no longer race a list of public HTTPS "what-is-my-IP" echo
// services (ipify/ifconfig/...). They reveal the IP to third parties, get
// blocked on some networks, and are redundant once the Worker + WebRTC paths
// cover public and LAN addresses. The server-side GeoIP provider
// (ip-api.com / Worker /lookup) is the source of truth for recorded locations;
// the browser only needs to recover the visitor's own public IP when the server
// would otherwise see only a private last hop (intranet/WSL deployments).

// localStorage cache keeps the dashboard from flickering back to "detecting"
// on every poll. The result is refreshed in the background every minute.
const CACHE_KEY = "mudp:client-ip";
const CACHE_TTL_MS = 60 * 1000;

// backgroundRefresh holds the in-flight background refresh promise, if any,
// so concurrent callers share one probe instead of spawning many.
let backgroundRefresh = null;

// readIPCache returns the last cached detection result, or null. Exported so
// the dashboard can render the cached value immediately without awaiting.
export function readIPCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return null;
    return parsed;
  } catch (_) {
    return null;
  }
}

function writeIPCache(result) {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify({ ...result, ts: Date.now() }));
  } catch (_) {}
}

// isCacheFresh reports whether a cached result is still valid (under the
// 1-minute TTL). Exported for the dashboard's initial render path.
export function isCacheFresh(cached) {
  return !!cached && !!cached.ts && Date.now() - cached.ts < CACHE_TTL_MS;
}

// STUN servers for the WebRTC path. The ICE agent queries these to obtain
// "server reflexive" (srflx) candidates carrying the host's public address.
// A geographically diverse mix: any one server may be blocked or rate-limited
// in a given network, and STUN still works as long as ONE answers.
const STUN_SERVERS = [
  "stun:stun.qq.com:3478",
  "stun:stun.miwifi.com:3478",
  "stun:stun.cloudflare.com:3478",
  "stun:stun.l.google.com:19302",
  "stun:stun.syncthing.net:3478",
];

// Cached worker base URL, if any. Resolved once via /api/security/ipworker and
// reused for the lifetime of the page. null means "not yet probed"; "" means
// "probed and none configured".
let workerBase = null;

// workerURL resolves the operator-configured Worker base URL, or "" when none
// is configured. Public endpoint, so it is safe to call before login.
async function workerURL(timeoutMs) {
  if (workerBase !== null) return workerBase;
  try {
    const res = await withTimeout(
      fetch("/api/security/ipworker", { cache: "no-store" }),
      timeoutMs
    );
    if (res.ok) {
      const data = await res.json();
      if (data && data.enabled && data.url) {
        workerBase = String(data.url).replace(/\/+$/, "");
        return workerBase;
      }
    }
  } catch (_) {
    // Network/CSP failure: treat as "no worker" so we fall back to WebRTC.
  }
  workerBase = "";
  return workerBase;
}

// fetchWorkerGeo asks the Worker for the visitor's own IP + geo. Returns
// { ip, geo } when it answers, or null. credentials:'omit' — the Worker is a
// separate origin and authenticates nobody; sending cookies there would only
// leak them.
async function fetchWorkerGeo(timeoutMs) {
  const base = await workerURL(timeoutMs);
  if (!base) return null;
  try {
    const res = await fetch(base + "/whoami", {
      cache: "no-store",
      credentials: "omit",
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!res.ok) return null;
    const data = await res.json();
    if (!data || data.status !== "success") return null;
    const ip = isValidIP(data.ip) ? String(data.ip).trim() : "";
    const geo = workerGeoToMudp(data);
    return { ip, geo };
  } catch (_) {
    return null;
  }
}

// workerGeoToMudp maps the Worker's /whoami payload to the shape the dashboard's
// renderClientLocation expects (and the same keys /api/geo returns), so the
// caller can use it without a separate adapter.
function workerGeoToMudp(d) {
  return {
    city: d.city || "",
    country: d.country || "",
    countryCode: d.countryCode || "",
    region: d.region || "",
    timezone: d.timezone || "",
    isp: d.isp || d.asOrganization || "",
    lat: d.lat ?? d.latitude ?? 0,
    lon: d.lon ?? d.longitude ?? 0,
    proxy: d.proxy === true,
    hosting: d.hosting === true,
  };
}

// withTimeout races a promise against a timeout so a non-responsive probe can't
// hang the dashboard.
function withTimeout(promise, ms) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timeout")), ms);
    promise.then(
      (v) => {
        clearTimeout(timer);
        resolve(v);
      },
      (e) => {
        clearTimeout(timer);
        reject(e);
      }
    );
  });
}

// isValidIP reports whether s is a plausible IPv4/IPv6 literal we'd want to
// show. Used to validate the Worker's reported IP.
function isValidIP(s) {
  if (!s) return false;
  const v = s.trim();
  if (!v) return false;
  // Naive but sufficient: IPv4 dotted-quad, or IPv6 containing ':'.
  const ipv4 = /^(\d{1,3}\.){3}\d{1,3}$/;
  return ipv4.test(v) || v.includes(":");
}

// isPublicCandidate filters STUN reflexive candidates. We keep anything that
// isn't a private/loopback range — STUN already strips the local view, so a
// srflx address is by definition the public-facing one.
function isPublicIP(ip) {
  if (!ip || ip.endsWith(".local")) return false;
  // Drop obvious private ranges that could sneak in via a misbehaving STUN
  // reply or a host candidate mislabeled srflx.
  if (
    ip.startsWith("127.") ||
    ip.startsWith("169.254.") ||
    ip.startsWith("10.") ||
    ip.startsWith("192.168.") ||
    ip === "::1"
  )
    return false;
  // 172.16.0.0/12 private range.
  if (ip.startsWith("172.")) {
    const o2 = parseInt(ip.split(".")[1], 10);
    if (o2 >= 16 && o2 <= 31) return false;
  }
  return true;
}

// isLANIP reports whether a host candidate's address is a real local interface
// IP worth showing (excludes mDNS .local names, loopback, and link-local).
function isLANIP(ip) {
  if (!ip) return false;
  if (ip.endsWith(".local")) return false;
  if (ip.startsWith("127.") || ip === "::1" || ip.startsWith("fe80")) return false;
  if (ip.startsWith("169.254.")) return false;
  return true;
}

// candidateIP parses the address out of a raw candidate string as a fallback
// when RTCIceCandidate.address is undefined (older browsers / mDNS masking).
// A typical line: "candidate:842163049 1 udp 1677729535 203.0.113.7 49152 typ srflx"
function candidateIP(raw) {
  if (!raw) return null;
  const parts = raw.split(" ");
  if (parts.length >= 5) return parts[4];
  return null;
}

// gatherWebRTC opens peer connections against the STUN servers and collects
// every candidate seen. Resolves to an array of {ip, type}. Best-effort:
// resolves to [] when WebRTC is unavailable. Used for BOTH public (srflx) and
// LAN (host) addresses in a single gathering pass.
function gatherWebRTC(timeoutMs) {
  return new Promise((resolve) => {
    if (typeof RTCPeerConnection === "undefined") {
      resolve([]);
      return;
    }
    const found = [];
    let pending = STUN_SERVERS.length;
    let settled = false;
    const done = () => {
      if (settled) return;
      settled = true;
      resolve(found);
    };
    if (pending === 0) {
      done();
      return;
    }
    // Hard ceiling so a stuck gather can't outlive the dashboard's budget even
    // if onicecandidate(null) never fires for some server.
    const hardStop = setTimeout(done, timeoutMs);

    for (const stun of STUN_SERVERS) {
      let pc;
      try {
        pc = new RTCPeerConnection({ iceServers: [{ urls: stun }] });
      } catch (_) {
        if (--pending === 0) { clearTimeout(hardStop); done(); }
        continue;
      }
      let serverDone = false;
      const finishServer = () => {
        if (serverDone) return;
        serverDone = true;
        try { pc.close(); } catch (_) {}
        if (--pending === 0) { clearTimeout(hardStop); done(); }
      };
      pc.onicecandidate = (event) => {
        const c = event.candidate;
        if (!c) { finishServer(); return; }
        const ip = c.address || candidateIP(c.candidate);
        if (ip) found.push({ ip, type: c.type || "host" });
      };
      pc.createDataChannel("ipcheck");
      pc.createOffer()
        .then((offer) => pc.setLocalDescription(offer))
        .catch(() => finishServer());
    }
  });
}

// performDetection runs the actual Worker + WebRTC probes without consulting
// the cache. Factored out so detectClientIP can serve a cached result while
// still refreshing in the background.
async function performDetection(timeoutMs = 4000) {
  const publicSet = new Set();
  const lanSet = new Set();
  let geo = null;

  // Worker path: rich (IP + geo), but optional. Resolves to null if not
  // configured or unreachable.
  const worker = withTimeout(fetchWorkerGeo(timeoutMs), timeoutMs)
    .then((res) => {
      if (res) {
        if (isPublicIP(res.ip)) publicSet.add(res.ip);
        if (res.geo) geo = res.geo;
      }
      return res;
    })
    .catch(() => null);

  // WebRTC path: always on, yields both public (srflx) and LAN (host) IPs.
  const webRTC = withTimeout(gatherWebRTC(timeoutMs), timeoutMs)
    .then((cands) => {
      for (const { ip, type } of cands) {
        if (type === "srflx" && isPublicIP(ip)) publicSet.add(ip);
        else if (type === "host" && isLANIP(ip)) lanSet.add(ip);
      }
      return cands;
    })
    .catch(() => []);

  // Wait for BOTH tracks to finish (or time out) so LAN candidates from WebRTC
  // are collected even when the Worker path wins the public race early.
  await Promise.allSettled([webRTC, worker]);

  const out = { public: [...publicSet], lan: [...lanSet] };
  if (geo) out.geo = geo;
  return out;
}

// detectClientIP resolves the visitor's public and LAN addresses within
// timeoutMs, preferring the operator's Worker (which also yields geo) and
// falling back to WebRTC/STUN. Returns { public: string[], lan: string[] } —
// each deduped and possibly empty when the corresponding method is
// unavailable/blocked. When the Worker answered, the result also carries
// `geo` (same shape as /api/geo) so the dashboard can render a location chip
// without a second request.
//
// Cached results are returned immediately and refreshed in the background once
// per minute, so the dashboard never flashes "detecting" again after the first
// successful probe. The returned object includes a `background` promise that
// resolves to the refreshed result (or null) when a background refresh is in
// progress; callers can use it to update the UI silently.
//
// (Kept the historical name `detectPublicIP` as a re-export below so existing
// imports keep working; new callers should prefer detectClientIP.)
export async function detectClientIP(timeoutMs = 4000) {
  const cached = readIPCache();
  if (cached && isCacheFresh(cached)) {
    // Start a background refresh if one isn't already running, but return the
    // cached value immediately so the UI stays stable.
    if (!backgroundRefresh) {
      backgroundRefresh = performDetection(timeoutMs)
        .then((result) => {
          writeIPCache(result);
          return result;
        })
        .catch(() => null)
        .finally(() => {
          backgroundRefresh = null;
        });
    }
    return { ...cached, background: backgroundRefresh };
  }

  // No cache or stale cache: block on a fresh detection, write it, and return.
  const result = await performDetection(timeoutMs);
  writeIPCache(result);
  return { ...result, background: Promise.resolve(null) };
}

// detectPublicIP is the legacy name; login.js only needs the visitor's WAN
// address (for the GeoIP cookie), so it returns string[] like before. New code
// should call detectClientIP for both public and LAN (and geo).
export async function detectPublicIP(timeoutMs = 4000) {
  const { public: ips } = await detectClientIP(timeoutMs);
  return ips;
}
