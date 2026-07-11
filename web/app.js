// MUDP entry point. Owns global state, core helpers, the boot sequence, and the
// shell/tab router. Feature views live in ./modules/*.js and import these.

import { renderLogin } from "./modules/login.js";
import { renderPending } from "./modules/pending.js";
import { renderDashboard } from "./modules/dashboard.js";
import { renderContainers } from "./modules/containers.js";
import { openCreateModal } from "./modules/create.js";
import { renderImages, openPullModal } from "./modules/images.js";
import { renderVolumes } from "./modules/volumes.js";
import { renderNetworks } from "./modules/networks.js";
import { renderStacks } from "./modules/stacks.js";
import { renderUsers } from "./modules/users.js";
import { renderUsage } from "./modules/usage.js";
import { renderAudit } from "./modules/audit.js";
import { renderSettings } from "./modules/settings.js";
import { renderNetdisk } from "./modules/netdisk.js";
import { renderDisks } from "./modules/disks.js";
import { renderHardware, stopPolling as stopHardwarePolling } from "./modules/hardware.js";
import { renderMCP } from "./modules/mcp.js";
import { escapeHtml, fmtBytes, fmtMB, roleRank, isAdminUser, canMutateUser } from "./lib/common.js";

// Re-export shared utilities so existing modules can keep importing them from app.js.
export { escapeHtml, fmtBytes, fmtMB, roleRank };

// displayName returns the human-readable name for a user: the Feishu-provided
// display name when present, otherwise the system username.
export function displayName(user) {
  return user?.displayName || user?.username || "";
}

// displayNameForUsername resolves a system username to its human-readable
// display name using the cached user list. Falls back to the raw username.
export function displayNameForUsername(username) {
  if (!username) return "";
  if (state.me?.username === username) return displayName(state.me);
  const u = state.users.find((x) => x.username === username);
  return u ? displayName(u) : username;
}

// ---------------- Shared state ----------------

export const state = {
  me: null,
  csrfToken: "",
  feishu: false,
  dashboard: null,
  images: [],
  users: [],
  groups: [],
  containers: [],
  volumes: [],
  networks: [],
  stacks: [],
  usage: [],
  audit: [],
  netdisk: { path: "", items: [], quota: null },
  mcpTokens: [],
  disks: [],
  feishuAdmin: { appId: "", appSecret: "", enabled: false, loaded: false },
  search: "",
  // containerFilter narrows the containers list by state (all/running/stopped/paused).
  containerFilter: "all",
  // containerSelection holds the selected container IDs for batch operations.
  containerSelection: new Set(),
  logViewer: { open: false, title: "", content: "", id: "", tail: 300 },
  create: { active: false, steps: [], logs: "", error: "" },
  pull: { active: false, logs: "", error: "", name: "" },
  stackRun: { title: "", logs: "", error: "" },
  pending: new Set(),
  terminal: { open: false, id: "", name: "", term: null, ws: null, fitAddon: null },
  modal: { open: false, kind: "", data: null },
  // gpuCount is the host's GPU count, loaded at boot so the create-container
  // modal can render the correct GPU options. Zero means "unknown or no GPU".
  gpuCount: 0,
  tab: "dashboard",
  sidebarCollapsed: localStorage.getItem("mudp:sidebar") === "collapsed",
};

// Backward-compatible helpers that operate on global state. New code should use
// the pure versions from ./lib/common.js when possible.
export function isAdmin() {
  return isAdminUser(state.me);
}
export function canMutate() {
  return canMutateUser(state.me);
}

// ---------------- Core helpers ----------------

export const $ = (selector) => document.querySelector(selector);

export async function api(path, opts = {}) {
  // Pull headers out of opts so the merged Content-Type below is not clobbered
  // by a trailing ...opts spread re-applying the caller's original headers.
  const { headers, ...rest } = opts;
  const method = (opts.method || "GET").toUpperCase();
  const mergedHeaders = { "Content-Type": "application/json", ...(headers || {}) };
  if (state.csrfToken && method !== "GET" && method !== "HEAD") {
    mergedHeaders["X-CSRF-Token"] = state.csrfToken;
  }
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: mergedHeaders,
    ...rest,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.pending = data.pending === true;
    throw err;
  }
  return data;
}

export function toast(msg, ok = false) {
  const el = document.createElement("div");
  el.className = `toast ${ok ? "ok" : ""}`;
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 3400);
}



// ---------------- Boot sequence ----------------

export function readCSRFCookie() {
  const m = document.cookie.match(/(?:^|; )mudp_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

export async function load() {
  try {
    state.csrfToken = readCSRFCookie();
    const me = await api("/api/me");
    if (!me || me.authenticated === false) {
      state.me = null;
      state.csrfToken = "";
      renderLogin();
      return;
    }
    // Use the server-provided token as a fallback in case the cookie is not
    // readable (e.g. older browsers or unusual cookie policies).
    state.csrfToken = me.csrfToken || state.csrfToken || "";
    state.me = me;
    state.feishu = (await api("/api/feishu/config").catch(() => ({ enabled: false }))).enabled;
    if (me.pending) {
      renderPending();
      return;
    }
    await refreshAll();
    // Best-effort GPU count probe so the create modal renders the right GPU options. Failure (non-GPU host, offline) leaves gpuCount at 0 and the modal falls back to none/all.
    api("/api/hardware/gpus").then((r) => { state.gpuCount = r.count || 0; }).catch(() => {});
    // MCP tokens are fetched on demand when the user opens the MCP tab
    // (renderMCP), so there's no boot-time background load racing the render.
    render();
  } catch {
    renderLogin();
  }
}

const SECTION_URLS = {
  images: "/api/images",
  containers: "/api/containers",
  dashboard: "/api/dashboard",
  volumes: "/api/volumes",
  networks: "/api/networks",
  stacks: "/api/stacks",
  users: "/api/users",
  groups: "/api/groups",
  audit: "/api/admin/audit?limit=200",
};

// Refresh only the specified data sections. Faster than refreshAll() for
// post-operation updates where only one or two endpoints are affected.
export async function refreshSection(...keys) {
  const out = await Promise.all(keys.map((k) => api(SECTION_URLS[k]).catch(() => null)));
  keys.forEach((k, i) => {
    if (out[i] !== null) {
      state[k] = out[i] || [];
    }
  });
  if (keys.includes("dashboard") && state.dashboard?.usage) {
    state.usage = state.dashboard.usage;
  }
}

export async function refreshAll() {
  const jobs = [api("/api/images"), api("/api/containers"), api("/api/dashboard"), api("/api/volumes"), api("/api/networks"), api("/api/stacks")];
  const labels = ["images", "containers", "dashboard", "volumes", "networks", "stacks"];
  if (isAdmin()) {
    jobs.push(api("/api/users"), api("/api/groups"), api("/api/admin/audit?limit=200"));
    labels.push("users", "groups", "audit");
  }
  // Use allSettled so one failing endpoint (transient 5xx, timeout) does not
  // reject the whole batch and boot the user to the login screen. Each section
  // updates independently; a failed section keeps its previous state.
  const results = await Promise.allSettled(jobs);
  results.forEach((r, i) => {
    if (r.status !== "fulfilled") return;
    const v = r.value;
    state[labels[i]] = v || [];
  });
  if (state.dashboard?.usage) {
    state.usage = state.dashboard.usage;
  }
}

// ---------------- Shell + tab router ----------------

const svgIcon = (body) =>
  `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;

const ICONS = {
  dashboard: svgIcon('<rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/>'),
  netdisk: svgIcon('<path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/>'),
  containers: svgIcon('<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>'),
  images: svgIcon('<rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21"/>'),
  volumes: svgIcon('<path d="M10 16h.01"/><path d="M2.212 11.577a2 2 0 0 0-.212.896V18a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-5.527a2 2 0 0 0-.212-.896L18.55 5.11A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><path d="M21.946 12.013H2.054"/><path d="M6 16h.01"/>'),
  networks: svgIcon('<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><line x1="8.59" x2="15.42" y1="13.51" y2="17.49"/><line x1="15.41" x2="8.59" y1="6.51" y2="10.49"/>'),
  stacks: svgIcon('<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="M2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12"/><path d="M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17"/>'),
  users: svgIcon('<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><path d="M16 3.128a4 4 0 0 1 0 7.744"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><circle cx="9" cy="7" r="4"/>'),
  usage: svgIcon('<path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>'),
  audit: svgIcon('<path d="M15 12h-5"/><path d="M15 8h-5"/><path d="M19 17V5a2 2 0 0 0-2-2H4"/><path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3"/>'),
  disks: svgIcon('<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>'),
  scripts: svgIcon('<path d="M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915"/><circle cx="12" cy="12" r="3"/>'),
  logout: svgIcon('<path d="m16 17 5-5-5-5"/><path d="M21 12H9"/><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/>'),
  collapse: svgIcon('<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/><path d="m16 15-3-3 3-3"/>'),
  expand: svgIcon('<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/>'),
  hardware: svgIcon('<path d="M9 3v18"/><path d="M15 3v18"/><rect width="18" height="18" x="3" y="3" rx="2"/>'),
  mcp: svgIcon('<path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2"/><path d="M20 14h2"/><path d="M15 13v2"/><path d="M9 13v2"/>'),
};

function label(tab) {
  return (
    {
      dashboard: "Dashboard",
      netdisk: "Netdisk",
      containers: "Containers",
      images: "Images",
      volumes: "Volumes",
      networks: "Networks",
      stacks: "Stacks",
      users: "Users & Groups",
      usage: "Usage",
      audit: "Activity Log",
      disks: "Disks",
      scripts: "Settings",
      hardware: "Hardware",
      mcp: "MCP",
    }[tab] || tab
  );
}

function subtitle(tab) {
  return (
    {
      dashboard: "Environment overview, resource counts, and your workspace at a glance.",
      netdisk: "Manage personal files, batch uploads, resumed uploads, and downloads.",
      containers: "Create and manage containers with a built-in web terminal.",
      images: "Publish and share mudp-managed images with user groups.",
      volumes: "Persistent volumes scoped to your workspace.",
      networks: "Custom networks for service-to-service connectivity.",
      stacks: "Deploy and manage docker-compose projects with live progress.",
      users: "Manage users, groups, roles, port prefixes, and Feishu approvals.",
      usage: "Per-user and per-container CPU, memory, disk, GPU, and process usage.",
      audit: "Recent management actions across the platform.",
      disks: "Host disk overview, mount helpers, and database backups.",
      scripts: "Configure registries, Feishu SSO, and system settings.",
      hardware: "Real-time CPU, memory, temperature, and per-GPU monitoring.",
      mcp: "Generate per-container tokens so AI tools (Claude Code, Codex, Kimi) can connect and work.",
    }[tab] || ""
  );
}

export function render() {
  const admin = isAdmin();
  const tabs = ["dashboard", "netdisk", "containers", "mcp", "usage", "images", "volumes", "networks", "stacks", "hardware", ...(admin ? ["users", "audit", "disks", "scripts"] : [])];

  const collapsed = state.sidebarCollapsed;
  $("#app").innerHTML =
    `<section class="shell${collapsed ? " sidebar-collapsed" : ""}">` +
      `<aside class="${collapsed ? "collapsed" : ""}">` +
        `<div class="brand">` +
          `<span class="dot"></span>` +
          `<span class="brand-text">MUDP</span>` +
          `<button id="sidebarToggle" class="sidebar-toggle" title="${collapsed ? "Expand" : "Collapse"} sidebar" aria-label="Toggle sidebar">${collapsed ? ICONS.expand : ICONS.collapse}</button>` +
        `</div>` +
        `<nav>` +
          tabs
            .map(
              (tab) =>
                `<button class="${state.tab === tab ? "active" : ""}" data-tab="${tab}" title="${label(tab)}"><span class="ico">${ICONS[tab] || ""}</span><span class="nav-label">${label(tab)}</span></button>`
            )
            .join("") +
        `</nav>` +
        `<div class="profile">` +
          `<strong>${escapeHtml(displayName(state.me))}</strong>` +
          `<span>${escapeHtml(state.me.role)}${state.me.groups?.length ? " - " + escapeHtml(state.me.groups.join(", ")) : ""}</span>` +
          `<button id="logout" title="Logout">${ICONS.logout}<span class="nav-label">Logout</span></button>` +
        `</div>` +
      `</aside>` +
      `<section class="work">` +
        `<header>` +
          `<div class="titles">` +
            `<h1>${label(state.tab)}</h1>` +
            `<p>${subtitle(state.tab)}</p>` +
          `</div>` +
          `<div class="head-actions">` +
            (state.tab === "containers"
              ? `<div class="search"><input id="searchBox" placeholder="Search containers" value="${escapeHtml(state.search)}"></div>`
              : "") +
            (state.tab === "containers" && canMutate() ? `<button class="primary" id="newContainerBtn">+ New Container</button>` : "") +
            (state.tab === "images" && isAdmin() ? `<button class="primary" id="pullImageBtn">+ Pull Image</button>` : "") +
            `<button class="ghost" id="refresh">Refresh</button>` +
          `</div>` +
        `</header>` +
        `<div id="view"></div>` +
      `</section>` +
    `</section>`;

  document.querySelectorAll("[data-tab]").forEach((btn) => {
    btn.onclick = () => {
      // Stop the hardware monitoring poll loop when navigating away so it
      // doesn't run in the background.
      if (state.tab === "hardware" && btn.dataset.tab !== "hardware") stopHardwarePolling();
      state.tab = btn.dataset.tab;
      render();
    };
  });
  const toggle = $("#sidebarToggle");
  if (toggle) {
    toggle.onclick = () => {
      state.sidebarCollapsed = !state.sidebarCollapsed;
      localStorage.setItem("mudp:sidebar", state.sidebarCollapsed ? "collapsed" : "expanded");
      render();
    };
  }
  $("#logout").onclick = async () => {
    await api("/api/logout", { method: "POST" });
    state.me = null;
    renderLogin();
  };
  $("#refresh").onclick = async () => {
    try {
      await refreshAll();
      render();
      toast("Refreshed", true);
    } catch (err) {
      toast(err.message);
    }
  };
  const newBtn = $("#newContainerBtn");
  if (newBtn) newBtn.onclick = openCreateModal;
  const pullBtn = $("#pullImageBtn");
  if (pullBtn) pullBtn.onclick = openPullModal;
  const search = $("#searchBox");
  if (search) {
    search.oninput = (e) => {
      state.search = e.target.value;
      renderView();
    };
  }

  renderView();
}

export function renderView() {
  if (state.tab === "dashboard") return renderDashboard();
  if (state.tab === "netdisk") return renderNetdisk();
  if (state.tab === "containers") return renderContainers();
  if (state.tab === "mcp") return renderMCP();
  if (state.tab === "images") return renderImages();
  if (state.tab === "volumes") return renderVolumes();
  if (state.tab === "networks") return renderNetworks();
  if (state.tab === "stacks") return renderStacks();
  if (state.tab === "users") return renderUsers();
  if (state.tab === "usage") return renderUsage();
  if (state.tab === "audit") return renderAudit();
  if (state.tab === "disks") return renderDisks();
  if (state.tab === "scripts") return renderSettings();
  if (state.tab === "hardware") return renderHardware();
}

export { renderLogin, renderPending };

load();
