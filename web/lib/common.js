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

// resolveNextRedirect validates a forward-auth redirect target before
// honouring it (used by the login page). A forward-auth redirect sends the
// browser to login with ?next=<original URL> so login can send it back to
// the forwarded port it came from; that port lives on the same host as the
// console but a different port (see forward_auth.go's forwardLoginTarget),
// so the check is against hostname, not full origin. Checking only the
// scheme (as an earlier version of this code did) let ?next= redirect to any
// http(s) URL at all, including an attacker's own domain -- an open
// redirect. Returns the safe absolute URL to redirect to, or null if next is
// absent, malformed, or points off-host.
export function resolveNextRedirect(next, origin) {
  if (!next) return null;
  try {
    const target = new URL(next, origin);
    if ((target.protocol === "http:" || target.protocol === "https:") && target.hostname === new URL(origin).hostname) {
      return target.href;
    }
  } catch {
    // Malformed URL: not a redirect target.
  }
  return null;
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

// joinPath joins two non-empty path segments with "/".
export function joinPath(a, b) {
  return [a, b].filter(Boolean).join("/");
}

export function formatDate(iso) {
  if (!iso) return "-";
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}
