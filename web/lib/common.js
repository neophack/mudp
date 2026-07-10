// Pure/shared utilities used across the MUDP frontend.
// These functions have no DOM or fetch side effects and are easy to unit test.

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

export function isAdminUser(me) {
  return roleRank(me?.role) >= ROLE_RANK.admin;
}

// canMutateUser mirrors the backend: only admin/operator/user may write.
export function canMutateUser(me) {
  const r = roleRank(me?.role);
  return r === ROLE_RANK.admin || r === ROLE_RANK.operator || r === ROLE_RANK.user;
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

export function fmtBytes(n) {
  if (n == null || Number.isNaN(n) || n < 0) return "-";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  let v = Number(n);
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 2)} ${units[i]}`;
}

export function fmtMB(mb) {
  if (mb == null || Number.isNaN(mb)) return "-";
  if (mb < 1024) return `${Math.round(mb)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
}

export function fmtPercent(n, digits = 1) {
  if (n == null || Number.isNaN(n)) return "-";
  return `${n.toFixed(digits)}%`;
}

export function formatDuration(seconds) {
  if (seconds == null || Number.isNaN(seconds) || seconds < 0) return "-";
  const s = Math.floor(seconds);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

export function formatDate(iso) {
  if (!iso) return "-";
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}
