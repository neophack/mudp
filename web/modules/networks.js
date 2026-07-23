// Networks: list, create, delete + WRT gateway card. mudp-managed networks are
// namespaced per user; the WRT (ImmortalWrt) gateway is the platform router
// that NATs user containers on mudp-mesh out to the public Internet.

import { state, api, toast, refreshSection, renderView, canMutate, isAdmin, t, readCSRFCookie } from "../app.js";
import { showModal, closeModal, readSSE } from "./ui.js";
import { registerJob } from "./jobs.js";
import { openNetworkDetail } from "./network_details.js";

export function renderNetworks() {
  // Admin-only: lazily load the WRT gateway policy so the card shows live
  // running state + addressing. Non-admins never see the card.
  if (isAdmin() && !state.wrt.loaded) {
    loadWrt();
  }
  const wrtCard = isAdmin() ? wrtGatewayCard(state.wrt) : "";

  const rows = (state.networks || []).map(networkRow).join("") ||
    `<tr class="empty-row"><td colspan="6">No networks. Click “+ New Network” to create one.</td></tr>`;
  $("#view").innerHTML =
    wrtCard +
    `<div class="card">` +
      `<div class="card-head"><h2>Networks</h2>` +
        (isAdmin() ? `<button class="primary" id="newNetBtn">+ New Network</button>` : "") +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Driver</th><th>Subnet</th><th>Containers</th><th>Owner</th><th class="actions">Actions</th></tr></thead>` +
        `<tbody>${rows}</tbody>` +
      `</table>` +
    `</div>`;
  const nb = $("#newNetBtn");
  if (nb) nb.onclick = openCreateNetwork;
  const cfg = $("#wrtConfigBtn");
  if (cfg) cfg.onclick = openWrtConfig;
  const dep = $("#wrtDeployBtn");
  if (dep) dep.onclick = deployWrt;
  document.querySelectorAll("[data-net-inspect]").forEach((btn) => {
    btn.onclick = () => openNetworkDetail(btn.dataset.netFullname, btn.dataset.netName);
  });
  document.querySelectorAll("[data-net-delete]").forEach((btn) => {
    btn.onclick = () => deleteNetwork(btn.dataset.netFullname, btn.dataset.netName);
  });
}

// ---- WRT gateway card (admin-only) ----
//
// Summarises the ImmortalWrt router: running state, image, how many user
// containers are on its LAN (mudp-mesh), and a "配置 WRT 网关" button that opens
// a modal with the full LAN/WAN/image/enabled form (POST /api/wrt). The gateway
// container itself also shows up read-only in the admin container list.

// luciLine renders the LuCI web-admin row for the WRT status card. When a host
// port is configured and the gateway is up, it links to the live LuCI URL;
// otherwise it shows the configured port or "—" when publishing is off.
function luciLine(s) {
  const port = Number(s.luciHostPort || 0);
  if (!port) {
    return `<div><div class="secondary-line">LuCI Web 管理面板</div><div class="primary-line">未发布 / not published</div></div>`;
  }
  const label = `:${port}`;
  if (s.gatewayRunning) {
    const url = `${location.protocol}//${location.hostname}:${port}`;
    return `<div><div class="secondary-line">LuCI Web 管理面板</div><div class="primary-line"><a href="${escapeHtml(url)}" target="_blank" rel="noopener">打开 LuCI ${escapeHtml(label)}</a></div></div>`;
  }
  return `<div><div class="secondary-line">LuCI Web 管理面板</div><div class="primary-line mono">${escapeHtml(label)} <span class="hint">(网关未运行)</span></div></div>`;
}

function wrtGatewayCard(s) {
  if (!s.loaded) {
    return `<div class="card"><div class="card-head"><h2>WRT 网关 / ImmortalWrt Gateway</h2></div><div class="card-body"><p class="hint">Loading…</p></div></div>`;
  }
  const status = s.gatewayRunning
    ? `<span class="badge badge-ok"><span class="dot"></span>running</span>`
    : `<span class="badge badge-muted"><span class="dot"></span>not running</span>`;
  const hint = s.gatewayRunning
    ? `<p class="hint">用户容器经此网关出公网，DROP 目的为 RFC1918/回环/docker 网桥的流量。镜像缺失时 mudp 会自动拉取（首次启动可能耗时几分钟）。</p>`
    : `<p class="hint">⚠️ 网关未运行 — mudp-mesh 上的容器会被 LAN 隔离但无外网。请先 pull <code>hkbase/immortalwrt:latest</code> 再点配置启用。</p>`;
  return `<div class="card"><div class="card-head"><h2>WRT 网关 / ImmortalWrt Gateway</h2>` +
      `<button class="primary" id="wrtDeployBtn">一键部署 / Deploy</button>` +
      `<button class="ghost" id="wrtConfigBtn">配置 / Configure</button>` +
    `</div>` +
    `<div class="card-body">` +
      `<div class="kv">` +
        `<div><div class="secondary-line">Status</div><div class="primary-line">${status}</div></div>` +
        `<div><div class="secondary-line">Image</div><div class="primary-line mono">${escapeHtml(s.gatewayImage || s.image || "—")}</div></div>` +
        `<div><div class="secondary-line">LAN (mudp-mesh)</div><div class="primary-line mono">${escapeHtml(s.lanSubnet || "—")} · gw ${escapeHtml(s.lanGateway || "—")}</div></div>` +
        `<div><div class="secondary-line">WAN (mudp-wrt-wan)</div><div class="primary-line mono">${escapeHtml(s.wanSubnet || "—")} · gw ${escapeHtml(s.wanGateway || "—")}</div></div>` +
        luciLine(s) +
        `<div><div class="secondary-line">Containers on LAN</div><div class="primary-line">${s.meshContainers || 0}</div></div>` +
      `</div>` +
      hint +
    `</div>` +
  `</div>`;
}

// openWrtConfig shows the full gateway policy form in a modal: enabled toggle,
// image, LAN/WAN subnets + gateways. Mirrors the old settings panel but lives on
// the Networks page now (networks + gateway are managed together).
function openWrtConfig() {
  const s = state.wrt || {};
  showModal({
    kind: "wrt",
    title: "WRT 网关 / ImmortalWrt Gateway",
    body:
      `<form id="wrtForm" class="compact">` +
        `<label class="check"><input type="checkbox" name="enabled" ${s.enabled ? "checked" : ""}> <strong>启用 WRT 隔离</strong> (Enable ImmortalWrt gateway isolation)</label>` +
        `<p class="hint">关闭后 mudp 不再管理 mudp-mesh/mudp-wrt-wan 网络与网关容器；新建容器不再默认挂 mudp-mesh，隔离失效。</p>` +
        `<label>网关镜像 / Gateway image (ImmortalWrt)</label>` +
        `<input name="image" placeholder="hkbase/immortalwrt:latest" value="${escapeHtml(s.image || "")}">` +
        `<p class="hint">改镜像名会<strong>重建</strong>网关容器（mudp-wrt）并自动拉取新镜像（私有 registry 需先在 Images 页配好凭据）。</p>` +
        `<label>LAN (mudp-mesh) 子网 / Subnet</label>` +
        `<input name="lanSubnet" placeholder="172.31.252.0/22" value="${escapeHtml(s.lanSubnet || "")}">` +
        `<label>LAN 网关 IP / Gateway IP (wrt 的 LAN 地址)</label>` +
        `<input name="lanGateway" placeholder="172.31.252.2" value="${escapeHtml(s.lanGateway || "")}">` +
        `<label>WAN (mudp-wrt-wan) 子网 / Subnet</label>` +
        `<input name="wanSubnet" placeholder="172.31.240.0/22" value="${escapeHtml(s.wanSubnet || "")}">` +
        `<label>WAN 网关 / WAN next hop (mudp-wrt-wan 桥网关)</label>` +
        `<input name="wanGateway" placeholder="172.31.240.1" value="${escapeHtml(s.wanGateway || "")}">` +
        `<label>WAN IP (wrt 的 WAN 地址)</label>` +
        `<input name="wanIp" placeholder="172.31.240.2" value="${escapeHtml(s.wanIp || "")}">` +
        `<label>LuCI 网页管理端口 / Web admin host port</label>` +
        `<input name="luciHostPort" type="number" min="0" max="65535" placeholder="18080（0=不发布）" value="${escapeHtml(String(s.luciHostPort ?? 18080))}">` +
        `<p class="hint">把网关内置的 LuCI 网页管理（容器内 80 端口）映射到宿主机端口，访问 <code>http://宿主机IP:端口</code>。填 <code>0</code> 关闭。改端口需重建网关（点一键部署）。</p>` +
        `<p class="hint">⚠️ 子网变更不会立即生效：Docker 网络 IPAM 创建后不可变。改子网/网关后需在宿主机执行 <code>docker network rm mudp-mesh mudp-wrt-wan</code> 再重启 mudp。</p>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="wrtSubmit">Save</button>`,
  });
  $("#wrtSubmit").onclick = async () => {
    const form = $("#wrtForm");
    const fd = new FormData(form);
    const payload = {
      enabled: fd.has("enabled"),
      image: String(fd.get("image") || "").trim(),
      lanSubnet: String(fd.get("lanSubnet") || "").trim(),
      lanGateway: String(fd.get("lanGateway") || "").trim(),
      wanSubnet: String(fd.get("wanSubnet") || "").trim(),
      wanGateway: String(fd.get("wanGateway") || "").trim(),
      wanIp: String(fd.get("wanIp") || "").trim(),
      luciHostPort: Number(fd.get("luciHostPort") || 0),
    };
    try {
      await api("/api/wrt", { method: "POST", body: JSON.stringify(payload) });
      state.wrt.loaded = false;
      await loadWrt();
      closeModal();
      toast(t("settings.saved"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

// deployWrt opens a one-click deploy modal and streams the force-rebuild
// progress over SSE: remove existing mudp-wrt → pull image → create + start →
// apply UCI config. Uses the current policy; change it via "配置 / Configure"
// first. Mirrors the image-pull progress modal (registerJob + readSSE).
function deployWrt() {
  const s = state.wrt || {};
  if (!confirm("一键部署将强制重建 mudp-wrt 容器（停+删旧容器、重新拉取镜像、新建、重配 UCI）。部署期间网关会短暂中断（mudp-mesh 上的容器暂时无外网）。继续？")) {
    return;
  }
  state.wrtDeploy = { active: true, logs: `One-click deploy (${s.image || "hkbase/immortalwrt:latest"})...\n`, error: "" };
  showModal({
    kind: "wrt-deploy",
    title: "一键部署 WRT 网关 / Deploy",
    body:
      `<p class="hint">强制重建 <code>mudp-wrt</code>：拉镜像 → 建容器 → 注入 UCI。镜像缺失时自动拉取，可能耗时几分钟。</p>` +
      `<pre class="log-output" style="max-height:340px;overflow:auto;background:var(--bg-elevated,#111);padding:.6rem;border-radius:6px;white-space:pre-wrap;">${escapeHtml(state.wrtDeploy.logs)}</pre>`,
    foot: `<button class="ghost" data-close>Close</button>`,
  });
  streamWrtDeploy();
}

function renderWrtDeployProgress() {
  const log = document.querySelector(".modal-body .log-output");
  if (log) {
    log.textContent = state.wrtDeploy.logs;
    log.scrollTop = log.scrollHeight;
  }
}

// streamWrtDeploy POSTs /api/wrt/deploy with Accept: text/event-stream and
// consumes the progress/error/done events. Registers as a background job so the
// admin can navigate away and still see status in the jobs panel.
async function streamWrtDeploy() {
  const job = registerJob({ kind: "wrt.deploy", name: "mudp-wrt" });
  try {
    const res = await fetch("/api/wrt/deploy", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || state.csrfToken || "" },
      signal: job.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.wrtDeploy.error = data.error || `Request failed (${res.status})`;
      state.wrtDeploy.active = false;
      state.wrtDeploy.logs += `[error] ${state.wrtDeploy.error}\n`;
      job.error(state.wrtDeploy.error);
      renderWrtDeployProgress();
      toast(state.wrtDeploy.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        const line = data.message || "";
        state.wrtDeploy.logs += line + "\n";
        job.log(line);
        renderWrtDeployProgress();
      } else if (event === "error") {
        state.wrtDeploy.error = data.message || "Deploy failed";
        state.wrtDeploy.logs += `[error] ${state.wrtDeploy.error}\n`;
        state.wrtDeploy.active = false;
        job.error(state.wrtDeploy.error);
        renderWrtDeployProgress();
        toast(state.wrtDeploy.error);
      } else if (event === "done") {
        state.wrtDeploy.active = false;
        state.wrtDeploy.logs += `[done] Gateway deployed (${data.image || ""}).\n`;
        job.done("WRT gateway deployed");
        renderWrtDeployProgress();
        toast("WRT gateway deployed", true);
        // Refresh the card + the admin container list so the new wrt row shows.
        state.wrt.loaded = false;
        loadWrt();
        refreshSection("containers").then(() => renderView());
        // Auto-open LuCI Web 管理面板 after deploy.
        const luciPort = Number((state.wrt || {}).luciHostPort || 18080);
        if (luciPort > 0) {
          const luciUrl = `${location.protocol}//${location.hostname}:${luciPort}`;
          state.wrtDeploy.logs += `[info] Opening LuCI at ${luciUrl}\n`;
          renderWrtDeployProgress();
          setTimeout(() => { window.open(luciUrl, "_blank", "noopener"); }, 1500);
        }
        setTimeout(() => closeModal(), 1200);
      }
    });
  } catch (err) {
    state.wrtDeploy.active = false;
    if (job.signal.aborted) {
      state.wrtDeploy.logs += "[cancelled]\n";
      job.cancel();
    } else {
      state.wrtDeploy.error = err.message || String(err);
      state.wrtDeploy.logs += `[error] ${state.wrtDeploy.error}\n`;
      job.error(state.wrtDeploy.error);
      toast(state.wrtDeploy.error);
    }
    renderWrtDeployProgress();
  }
}

// loadWrt fetches the WRT gateway policy + live status into state.wrt.
export async function loadWrt() {
  if (!isAdmin()) {
    state.wrt = { loaded: true };
    return;
  }
  try {
    const data = await api("/api/wrt");
    state.wrt = {
      loaded: true,
      enabled: !!data.enabled,
      image: data.image || "",
      lanSubnet: data.lanSubnet || "",
      lanGateway: data.lanGateway || "",
      wanSubnet: data.wanSubnet || "",
      wanGateway: data.wanGateway || "",
      wanIp: data.wanIp || "",
      luciHostPort: Number(data.luciHostPort || 0),
      gatewayRunning: !!data.gatewayRunning,
      gatewayImage: data.gatewayImage || "",
      gatewayContainer: data.gatewayContainer || "",
      meshContainers: data.meshContainers || 0,
    };
  } catch {
    state.wrt = { loaded: true };
  }
  if (state.tab === "networks") renderView();
}

function networkRow(n) {
  const sys = !!n.system;
  const nameCell = sys
    ? `<div class="primary-line">${escapeHtml(n.name)} <span class="badge badge-muted">system</span></div>`
    : `<div class="primary-line">${escapeHtml(n.name)}</div>`;
  return (
    `<tr>` +
      `<td>${nameCell}</td>` +
      `<td><div class="secondary-line">${escapeHtml(n.driver)}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(n.subnet || "—")}</div></td>` +
      `<td>${n.containers || 0}</td>` +
      `<td><div class="secondary-line">${escapeHtml(n.owner || "system")}</div></td>` +
      `<td class="actions">` +
        `<button class="icon" title="Details" data-net-name="${escapeHtml(n.name)}" data-net-fullname="${escapeHtml(n.fullName || n.name)}" data-net-inspect>ℹ</button>` +
        (canMutate() && !sys ? `<button class="icon danger" title="Delete" data-net-name="${escapeHtml(n.name)}" data-net-fullname="${escapeHtml(n.fullName || n.name)}" data-net-delete>✕</button>` : "") +
      `</td>` +
    `</tr>`
  );
}

function openCreateNetwork() {
  showModal({
    kind: "network",
    title: "New Network",
    body:
      `<form id="netForm" class="compact">` +
        `<input name="name" placeholder="Network name, e.g. frontend" required>` +
        `<select name="driver"><option value="bridge">bridge</option><option value="overlay">overlay</option><option value="macvlan">macvlan</option></select>` +
        `<input name="subnet" placeholder="Subnet (optional), e.g. 172.20.0.0/16">` +
        `<details class="advanced-block"><summary>Advanced IPAM (optional)</summary>` +
          `<input name="gateway" placeholder="Gateway, e.g. 172.20.0.1">` +
          `<input name="ipRange" placeholder="IP range, e.g. 172.20.0.0/24">` +
          `<label class="check"><input type="checkbox" name="ipv6"> Enable IPv6</label>` +
        `</details>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="netSubmit">Create</button>`,
  });
  $("#netSubmit").onclick = async () => {
    const form = $("#netForm");
    const fd = new FormData(form);
    const payload = {
      name: fd.get("name"),
      driver: fd.get("driver") || "bridge",
      subnet: fd.get("subnet") || "",
      gateway: fd.get("gateway") || "",
      ipRange: fd.get("ipRange") || "",
      ipv6: form.querySelector('[name="ipv6"]').checked,
    };
    try {
      await api("/api/networks", { method: "POST", body: JSON.stringify(payload) });
      await refreshSection("networks");
      renderView();
      closeModal();
      toast("Network created", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteNetwork(fullName, name) {
  if (!confirm(`Delete network “${name}”?`)) return;
  try {
    await api("/api/networks/delete", { method: "POST", body: JSON.stringify({ name: fullName }) });
    await refreshSection("networks");
    renderView();
    toast("Network deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}

function $(selector) {
  return document.querySelector(selector);
}
