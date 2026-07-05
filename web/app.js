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

// ---------------- Shared state ----------------

export const state = {
  me: null,
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
  disks: [],
  scripts: { sshScript: "", vscodeScript: "" },
  feishuAdmin: { appId: "", appSecret: "", enabled: false, loaded: false },
  search: "",
  logViewer: { open: false, title: "", content: "", id: "", tail: 300 },
  create: { active: false, steps: [], logs: "", error: "" },
  pull: { active: false, logs: "", error: "", name: "" },
  stackRun: { title: "", logs: "", error: "" },
  pending: new Set(),
  terminal: { open: false, id: "", name: "", term: null, ws: null, fitAddon: null },
  modal: { open: false, kind: "", data: null },
  tab: "dashboard",
};

// Role helpers mirror the backend ranks. Higher rank = more powerful.
export const ROLE_RANK = {
  readonly: 10,
  helpdesk: 20,
  user: 30,
  operator: 40,
  admin: 50,
};
export function roleRank(r) {
  return ROLE_RANK[r] || 0;
}
export function isAdmin() {
  return roleRank(state.me?.role) >= ROLE_RANK.admin;
}
// canMutate mirrors the backend: only admin/operator/user may write.
export function canMutate() {
  const r = roleRank(state.me?.role);
  return r === ROLE_RANK.admin || r === ROLE_RANK.operator || r === ROLE_RANK.user;
}

// ---------------- Core helpers ----------------

export const $ = (selector) => document.querySelector(selector);

export async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
    ...opts,
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

export function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}

// ---------------- Boot sequence ----------------

export async function load() {
  try {
    const me = await api("/api/me");
    if (!me || me.authenticated === false) {
      state.me = null;
      renderLogin();
      return;
    }
    state.me = me;
    state.feishu = (await api("/api/feishu/config").catch(() => ({ enabled: false }))).enabled;
    if (me.pending) {
      renderPending();
      return;
    }
    await refreshAll();
    render();
  } catch {
    renderLogin();
  }
}

export async function refreshAll() {
  const jobs = [api("/api/images"), api("/api/containers"), api("/api/dashboard"), api("/api/volumes"), api("/api/networks"), api("/api/stacks")];
  const labels = ["images", "containers", "dashboard", "volumes", "networks", "stacks"];
  if (isAdmin()) {
    jobs.push(api("/api/users"), api("/api/groups"), api("/api/admin/usage"), api("/api/scripts"), api("/api/admin/audit?limit=200"));
    labels.push("users", "groups", "usage", "scripts", "audit");
  }
  const out = await Promise.all(jobs);
  out.forEach((v, i) => {
    state[labels[i]] = v || (labels[i] === "scripts" ? { sshScript: "", vscodeScript: "" } : []);
  });
}

// ---------------- Shell + tab router ----------------

const NAV_ICONS = {
  dashboard: "DB",
  netdisk: "FD",
  containers: "CT",
  images: "IM",
  volumes: "VL",
  networks: "NW",
  stacks: "ST",
  users: "US",
  usage: "RS",
  audit: "AU",
  disks: "DK",
  scripts: "SE",
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
    }[tab] || tab
  );
}

function subtitle(tab) {
  return (
    {
      dashboard: "Environment overview, resource counts, and your workspace at a glance.",
      netdisk: "Manage personal files, batch uploads, resumed uploads, and downloads.",
      containers: "Create and manage containers with optional SSH and VS Code access.",
      images: "Publish and share mudp-managed images with user groups.",
      volumes: "Persistent volumes scoped to your workspace.",
      networks: "Custom networks for service-to-service connectivity.",
      stacks: "Deploy and manage docker-compose projects with live progress.",
      users: "Manage users, groups, roles, port prefixes, and Feishu approvals.",
      usage: "Per-user and per-container CPU, memory, disk, GPU, and process usage.",
      audit: "Recent management actions across the platform.",
      disks: "Host disk overview, mount helpers, and database backups.",
      scripts: "Bootstrap scripts, Feishu SSO, and system settings.",
    }[tab] || ""
  );
}

export function render() {
  const admin = isAdmin();
  const tabs = ["dashboard", "netdisk", "containers", "usage", "images", "volumes", "networks", "stacks", ...(admin ? ["users", "audit", "disks", "scripts"] : [])];

  $("#app").innerHTML =
    `<section class="shell">` +
      `<aside>` +
        `<div class="brand"><span class="dot"></span> MUDP</div>` +
        `<nav>` +
          tabs
            .map(
              (tab) =>
                `<button class="${state.tab === tab ? "active" : ""}" data-tab="${tab}"><span class="ico">${NAV_ICONS[tab] || ""}</span>${label(tab)}</button>`
            )
            .join("") +
        `</nav>` +
        `<div class="profile">` +
          `<strong>${escapeHtml(state.me.username)}</strong>` +
          `<span>${escapeHtml(state.me.role)}${state.me.groups?.length ? " · " + escapeHtml(state.me.groups.join(", ")) : ""}</span>` +
          `<button id="logout">Logout</button>` +
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
              ? `<div class="search"><input id="searchBox" placeholder="Search containers…" value="${escapeHtml(state.search)}"></div>`
              : "") +
            (state.tab === "containers" && canMutate() ? `<button class="primary" id="newContainerBtn">+ New Container</button>` : "") +
            (state.tab === "images" && canMutate() ? `<button class="primary" id="pullImageBtn">+ Pull Image</button>` : "") +
            `<button class="ghost" id="refresh">↻ Refresh</button>` +
          `</div>` +
        `</header>` +
        `<div id="view"></div>` +
      `</section>` +
    `</section>`;

  document.querySelectorAll("[data-tab]").forEach((btn) => {
    btn.onclick = () => {
      state.tab = btn.dataset.tab;
      render();
    };
  });
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
  if (state.tab === "images") return renderImages();
  if (state.tab === "volumes") return renderVolumes();
  if (state.tab === "networks") return renderNetworks();
  if (state.tab === "stacks") return renderStacks();
  if (state.tab === "users") return renderUsers();
  if (state.tab === "usage") return renderUsage();
  if (state.tab === "audit") return renderAudit();
  if (state.tab === "disks") return renderDisks();
  if (state.tab === "scripts") return renderSettings();
}

// Re-exported so modules (login/pending) can drive transitions without a
// circular import on render/renderPending/renderView themselves.
export { renderLogin, renderPending };

load();

