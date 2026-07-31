/**
 * mudp IP-detection Worker — 仅 /whoami,零配置
 * ===========================================================================
 *
 * 作用:让浏览器在 mudp 页面里拿到"自己的公网 IP + 地理位置"。Cloudflare 边缘
 * 会在每个请求上附带 request.cf(基于请求方 IP 解析的城市/国家/经纬度/时区/ISP),
 * 直接回传即可 —— HTTPS、全球可达、无限流,等于白送一份 GeoIP。
 *
 * 为什么只有 /whoami、没有任何鉴权:
 *   - 这个端点只返回"当前请求者自己"的信息。外人调它只能看到自己的 IP/位置,
 *     对别人毫无价值,因此不需要密码/TOTP。
 *   - mudp 服务端记录访问日志时需要查"任意 IP"的地理位置(比如某个攻击者),
 *     那是服务端直接调 ip-api.com 的本职工作,和本 Worker 无关 —— 所以这里
 *     没有 /lookup,也就没有曾经导致 502 的后端转发,和会两边对不上的密钥。
 *
 * 部署:
 *   1. Cloudflare Dashboard → Workers & Pages → Create → 粘贴本文件 → Deploy
 *   2. 拿到 https://<name>.workers.dev,填进 mudp
 *      "安全监控 → 设置 → IP 检测 Worker" 的域名框,保存。
 *   无需配置任何环境变量、密码或密钥。
 *
 * 返回 JSON(ipAPIResponse 形状):
 *   status / ip / country / countryCode / regionName / region / city /
 *   lat / lon / latitude / longitude / timezone / isp / asOrganization /
 *   postalCode / colo / proxy / hosting / continent / metroCode
 */

export default {
  async fetch(request) {
    const url = new URL(request.url);

    // CORS:/whoami 由浏览器跨域调用(mudp 页面 → worker),必须放行;预检也放行。
    if (request.method === "OPTIONS") {
      return cors(new Response(null, { status: 204 }));
    }

    try {
      if (url.pathname === "/whoami") {
        return cors(await handleWhoami(request));
      }
      return cors(json({ status: "fail", message: "not found" }, 404));
    } catch (err) {
      return cors(json({ status: "fail", message: String(err && err.message || err) }, 500));
    }
  },
};

// handleWhoami returns the requester's own IP + geo from Cloudflare's request.cf.
// A simple per-IP rate limit blunts abuse (no auth, so this is the only guard).
async function handleWhoami(request) {
  const cf = request.cf || {};
  const ip = request.headers.get("cf-connecting-ip") || "";

  // 每 IP 每 60 秒最多 60 次 /whoami,挡住滥用。纯内存计数(单实例近似)。
  if (!rateAllow(request, 60, 60)) {
    return json({ status: "fail", message: "rate limited" }, 429);
  }

  return json(geoEnvelope(ip, cf));
}

// geoEnvelope maps request.cf to the ipAPIResponse shape mudp expects.
// Note: Cloudflare's free cf object does not carry proxy/hosting classification,
// so those stay false here (VPN detection remains a server-side ip-api concern).
function geoEnvelope(ip, cf) {
  const lat = num(cf.latitude);
  const lon = num(cf.longitude);
  return {
    status: "success",
    ip,
    country: cf.country || "",
    countryCode: cf.country || "",
    regionName: cf.region || "",
    region: cf.region || "",
    city: cf.city || "",
    lat,
    lon,
    latitude: lat,
    longitude: lon,
    timezone: cf.timezone || "",
    isp: cf.asOrganization || "",
    asOrganization: cf.asOrganization || "",
    postalCode: cf.postalCode || "",
    colo: cf.colo || "",
    proxy: false,
    hosting: false,
    continent: cf.continent || "",
    metroCode: cf.metroCode || "",
  };
}

// ---------------------------------------------------------------------------
// 速率限制(纯内存,单实例近似)。无鉴权端点的唯一防滥用手段。
// ---------------------------------------------------------------------------

const rateState = new Map(); // ip -> { count, reset }

function rateAllow(request, max, windowSec) {
  const id = request.headers.get("cf-connecting-ip") || "anon";
  const now = Date.now();
  let st = rateState.get(id);
  if (!st || now > st.reset) st = { count: 0, reset: now + windowSec * 1000 };
  st.count++;
  rateState.set(id, st);
  if (rateState.size > 10000) rateState.clear(); // 防泄漏
  return st.count <= max;
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

function num(v) { const n = parseFloat(v); return isNaN(n) ? 0 : n; }

function json(obj, status = 200) {
  return new Response(JSON.stringify(obj), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

function cors(resp) {
  resp.headers.set("Access-Control-Allow-Origin", "*");
  resp.headers.set("Access-Control-Allow-Methods", "GET, OPTIONS");
  resp.headers.set("Access-Control-Allow-Headers", "Content-Type");
  resp.headers.set("Access-Control-Max-Age", "86400");
  return resp;
}
