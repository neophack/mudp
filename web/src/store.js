// Global reactive store: session identity, shared data sections, and the
// notification/jobs badges. Views read reactively; actions refresh sections
// from the API. Everything not session-scoped is fetched by the page that
// owns it, exactly like the old per-module state.

import { reactive } from "vue";
import { api } from "@/api";
import { isAdminUser, canMutateUser } from "@/lib/common.js";

// Every key read reactively by templates/computed must be declared here:
// Vue 2 reactivity cannot detect properties added after creation.
export const store = reactive({
  me: null,
  booted: false,
  setupNeeded: false,
  csrfToken: "",
  feishu: false,
  siteName: "",
  sidebarCollapsed: localStorage.getItem("mudp:sidebar") === "collapsed",
  route: "",
  dashboard: null,
  images: [],
  users: [],
  groups: [],
  containers: [],
  volumes: [],
  networks: [],
  stacks: [],
  forwards: null,
  disks: null,
  usage: [],
  audit: [],
  auditSearch: {},
  netdisk: { path: "", items: [], quota: null },
  mcpTokens: [],
  mcpRemote: null,
  gpuCount: 0,
  search: "",
  containerFilter: "all",
  // Phone-width flag (matchMedia ≤768px, live-updated from main.js). List
  // views use it to swap wide action columns for the bottom action sheet.
  isMobile: false,
  notifications: [],
  unreadCount: 0,
  jobs: [],
  runningJobs: 0,
  // lang is a monotonic counter touched on every language switch so templates
  // calling tt() re-render even though lib/i18n keeps its own plain variable.
  lang: 0,
  // Resolved appearance flag (theme pref may be "auto"); templates read this
  // to pick icons/tints without re-deriving it.
  isDark: false,
  theme: localStorage.getItem("mudp:theme") || "auto",
  // Theme pack (daylight / graphite / ocean); accentColor mirrors the pack's
  // accent for the current appearance, for consumers that can't read CSS
  // variables (ECharts options).
  pack: localStorage.getItem("mudp:pack") || "daylight",
  accentColor: "#3370ff",
  // Per-section load outcome: a failing section used to render as a silent
  // empty list, indistinguishable from genuinely no data. Views show a
  // "load failed" state when their flag is set.
  sectionErrors: {},
});

// Theme packs, in picker order. Each is a complete visual language (surface
// colors, radii, density, sidebar treatment) defined as CSS custom properties
// in index.css under html[data-pack="…"]; only the bits ECharts can't read
// from CSS live here. "accent"/"darkAccent" feed chart series colors in light
// and dark mode; "preview" drives the mini mockup in the Settings picker. The
// id doubles as the i18n key under "theme.pack.*".
export const PACKS = [
  {
    id: "daylight",
    accent: "#3370ff",
    darkAccent: "#3370ff",
    preview: { bg: "#f5f5f7", card: "#ffffff", side: "#eef0f4", brand: "#3370ff" },
  },
  {
    id: "graphite",
    accent: "#5e6ad2",
    darkAccent: "#7b86e8",
    preview: { bg: "#fafafa", card: "#ffffff", side: "#f1f1f3", brand: "#5e6ad2" },
  },
  {
    id: "ocean",
    accent: "#0f62fe",
    darkAccent: "#4589ff",
    preview: { bg: "#f3f6f9", card: "#ffffff", side: "#13233a", brand: "#0f62fe" },
  },
];

// Apply the resolved theme to <html>: data-theme="dark" drives our own tokens
// and the "dark" class switches Element Plus to its built-in dark palette,
// while data-pack selects the theme pack.
function applyTheme() {
  const dark = store.theme === "dark" || (store.theme === "auto" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  store.isDark = dark;
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  document.documentElement.classList.toggle("dark", dark);
  const pack = PACKS.find((p) => p.id === store.pack) || PACKS[0];
  store.accentColor = dark ? pack.darkAccent : pack.accent;
  document.documentElement.dataset.pack = pack.id;
}

export function setTheme(pref) {
  store.theme = pref;
  localStorage.setItem("mudp:theme", pref);
  applyTheme();
}

export function setPack(id) {
  store.pack = id;
  localStorage.setItem("mudp:pack", id);
  applyTheme();
}

// Keep "auto" in sync with the OS appearance while the tab is open.
export function initTheme() {
  applyTheme();
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (store.theme === "auto") applyTheme();
  });
}

export function isAdmin() {
  return isAdminUser(store.me);
}
export function canMutate() {
  return canMutateUser(store.me);
}

export function displayName(user) {
  return user?.displayName || user?.username || "";
}

export function displayNameForUsername(username) {
  if (!username) return "";
  if (store.me?.username === username) return displayName(store.me);
  const u = store.users.find((x) => x.username === username);
  return u ? displayName(u) : username;
}

export function applySiteName(name) {
  store.siteName = name || "";
  document.title = store.siteName || "MUDP";
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
  disks: "/api/admin/disks",
  forwards: "/api/admin/forwards",
};

// Refresh only the specified data sections. Faster than refreshAll() for
// post-operation updates where only one or two endpoints are affected.
export async function refreshSection(...keys) {
  const out = await Promise.all(keys.map((k) => api(SECTION_URLS[k]).catch(() => null)));
  keys.forEach((k, i) => {
    if (out[i] !== null) {
      store[k] = out[i] || [];
      delete store.sectionErrors[k];
    } else {
      store.sectionErrors[k] = true;
    }
  });
  if (keys.includes("dashboard") && store.dashboard?.usage) {
    store.usage = store.dashboard.usage;
  }
}

export async function refreshAll() {
  const jobs = [api("/api/images"), api("/api/containers"), api("/api/dashboard"), api("/api/volumes"), api("/api/networks")];
  const labels = ["images", "containers", "dashboard", "volumes", "networks"];
  // Stacks are closed for regular users by default; skip the call instead of
  // painting a phantom section error every refresh cycle.
  if (store.me?.stacksEnabled) {
    jobs.push(api("/api/stacks"));
    labels.push("stacks");
  } else {
    store.stacks = [];
  }
  fetchNotifications().catch(() => {});
  if (isAdmin()) {
    jobs.push(api("/api/users"), api("/api/groups"), api("/api/admin/audit?limit=200"));
    labels.push("users", "groups", "audit");
  }
  // Use allSettled so one failing endpoint (transient 5xx, timeout) does not
  // reject the whole batch and boot the user to the login screen. Each section
  // updates independently; a failed section keeps its previous state.
  const results = await Promise.allSettled(jobs);
  results.forEach((r, i) => {
    if (r.status !== "fulfilled") {
      store.sectionErrors[labels[i]] = true;
      return;
    }
    store[labels[i]] = r.value || [];
    delete store.sectionErrors[labels[i]];
  });
  if (store.dashboard?.usage) {
    store.usage = store.dashboard.usage;
  }
}

export async function fetchNotifications() {
  try {
    const res = await api("/api/notifications");
    store.notifications = res.notifications || [];
    store.unreadCount = res.unreadCount || 0;
  } catch {
    store.notifications = [];
    store.unreadCount = 0;
  }
}
