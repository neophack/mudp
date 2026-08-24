// fnOS-style system update UI: a dedicated update window (version transition,
// release notes, manual downloads) and — once the admin commits — a locked
// full-screen upgrade view whose circular progress ring walks through
// download → install → restart, then reloads the page onto the new version.
// The upgrade itself runs server-side (internal/server/selfupgrade.go); this
// module only starts it and paints its progress.

import { api, toast, escapeHtml, t } from "../app.js";
import { fmtBytes } from "../lib/common.js";
import { showModal, closeModal } from "./ui.js";

// upgrading is module state so the dashboard's upgrade button stays disabled
// across the poller's re-renders while an upgrade is in flight. The overlay
// locks the screen, so this is mostly belt-and-braces for the error path.
let upgrading = false;

export function isUpgrading() {
  return upgrading;
}

// openUpgradeModal renders the update window. `check` is the dashboard's
// already-fetched /api/update/check payload when available (the card passes
// its own copy); without one it fetches the server-cached answer itself.
export async function openUpgradeModal(check) {
  const res = check || (await api("/api/update/check").catch(() => null));
  if (!res) {
    toast(t("dash.checkFailed"));
    return;
  }
  if (res.error || !res.available) {
    openUpToDateModal(res);
    return;
  }
  const released = res.releasedAt && !isNaN(new Date(res.releasedAt))
    ? t("upgrade.releasedOn", { time: new Date(res.releasedAt).toLocaleDateString() })
    : "";
  showModal({
    kind: "upgrade",
    title: t("upgrade.title"),
    body:
      `<div class="upgrade-hero">` +
        `<div class="upgrade-hero-icon">⬇</div>` +
        `<div class="upgrade-hero-title">${t("upgrade.available")} <span class="mono">${escapeHtml(res.latest)}</span></div>` +
        `<div class="upgrade-hero-sub hint">${escapeHtml(res.current)} → ${escapeHtml(res.latest)}${released ? ` · ${released}` : ""}</div>` +
      `</div>` +
      `<div class="upgrade-notes">` +
        `<div class="upgrade-notes-title">${t("upgrade.releaseNotes")}</div>` +
        (notesList(res.notes) || `<p class="hint">${t("upgrade.noNotes")}</p>`) +
      `</div>` +
      manualDownloads(res),
    foot:
      `<button class="ghost" data-close>${t("upgrade.later")}</button>` +
      `<button class="primary" id="upgradeNowBtn">${t("upgrade.now")}</button>`,
  });
  const nowBtn = document.getElementById("upgradeNowBtn");
  if (nowBtn) nowBtn.onclick = () => startUpgrade(res.latest);
}

// openUpToDateModal is the same window with no action: already current, a
// failed check, or a dev build (every tag looks newer than "dev").
function openUpToDateModal(res) {
  const msg = res.error
    ? t("dash.checkFailed")
    : `${t("upgrade.upToDateTitle")} · ${escapeHtml(res.current || "dev")}`;
  showModal({
    kind: "upgrade",
    title: t("upgrade.title"),
    body: `<div class="upgrade-hero"><div class="upgrade-hero-icon">✓</div><div class="upgrade-hero-title">${msg}</div></div>`,
  });
}

// notesList turns a GitHub release body into a simple list: one <li> per
// non-empty line with markdown list markers and headings stripped. fnOS-style
// update notes are a plain bullet list, not rendered markdown.
function notesList(notes) {
  const lines = String(notes || "")
    .split("\n")
    .map((l) => l.trim().replace(/^[-*+]\s+/, "").replace(/^#+\s*/, ""))
    .filter(Boolean);
  if (!lines.length) return "";
  return `<ul>${lines.map((l) => `<li>${escapeHtml(l)}</li>`).join("")}</ul>`;
}

function manualDownloads(res) {
  if (!res.downloads) return "";
  const links = [
    ["windows", "win-x64"],
    ["windows-arm64", "win-arm64"],
    ["linux", "linux-x64"],
    ["linux-arm64", "linux-arm64"],
  ]
    .filter(([k]) => res.downloads[k])
    .map(([k, label]) => `<a href="${escapeHtml(res.downloads[k])}" download>${label}</a>`)
    .join(" · ");
  return `<div class="upgrade-manual hint">${t("upgrade.manualDownload")} ${links}</div>`;
}

// startUpgrade closes the update window, locks the screen behind a full-screen
// overlay, starts the server-side self-upgrade and polls it to completion.
function startUpgrade(tag) {
  if (upgrading) return;
  upgrading = true;
  closeModal();

  const overlay = document.createElement("div");
  overlay.className = "upgrade-overlay";
  overlay.innerHTML =
    `<div class="upgrade-panel">` +
      `<div class="upgrade-ring" id="upgradeRing"><div class="upgrade-ring-hole"><span id="upgradeRingText"></span></div></div>` +
      `<div class="upgrade-phase" id="upgradePhase"></div>` +
      `<div class="upgrade-detail hint" id="upgradeDetail"></div>` +
      `<div class="upgrade-actions" id="upgradeActions"></div>` +
    `</div>`;
  document.body.appendChild(overlay);
  paintPhase("upgrade.preparing", "", null);

  // The old process exits mid-upgrade, so expect a stretch of failed fetches:
  // connection refused simply means "restarting".
  const deadline = Date.now() + 4 * 60 * 1000;
  const stop = (message) => {
    clearInterval(poll);
    failUpgrade(message);
  };
  const poll = setInterval(pollTick, 1000);

  api("/api/admin/upgrade", { method: "POST", body: JSON.stringify({ tag }) })
    .catch(async (err) => {
      // A second click may race an in-flight upgrade (409): adopt it instead
      // of failing — the poll below re-attaches to its progress.
      const st = await api("/api/admin/upgrade").catch(() => null);
      if (!st || (st.phase !== "running:download" && st.phase !== "running:restarting")) {
        stop(err.message);
      }
    });

  async function pollTick() {
    try {
      const st = await api("/api/admin/upgrade");
      if (st.phase === "error") {
        stop(st.message ? `${t("upgrade.failed")}: ${st.message}` : t("upgrade.failed"));
        return;
      }
      if (st.phase === "running:download") {
        const read = st.read || 0;
        const total = st.total || 0;
        const pct = total > 0 ? (read / total) * 100 : null;
        const detail = total > 0
          ? `${fmtBytes(read)} / ${fmtBytes(total)}`
          : fmtBytes(read);
        paintPhase("upgrade.downloading", detail, pct);
      } else if (st.phase === "running:restarting") {
        paintPhase("upgrade.installing", "", null);
      }
    } catch {
      paintPhase("upgrade.restarting", "", null);
    }
    try {
      const me = await api("/api/me");
      if (me && me.version === tag) {
        clearInterval(poll);
        finishUpgrade();
      }
    } catch { /* still restarting */ }
    if (Date.now() > deadline) {
      stop(t("upgrade.timeout"));
    }
  }
}

// paintPhase updates the overlay's ring, phase line, and detail line in one
// go. pct null renders the indeterminate spinner ring.
function paintPhase(phaseKey, detail, pct) {
  const phase = document.getElementById("upgradePhase");
  if (phase) phase.textContent = t(phaseKey);
  const detailEl = document.getElementById("upgradeDetail");
  if (detailEl) detailEl.textContent = detail;
  const ring = document.getElementById("upgradeRing");
  const text = document.getElementById("upgradeRingText");
  if (!ring || !text) return;
  ring.classList.remove("is-spin");
  if (pct === null || pct === undefined || isNaN(pct)) {
    ring.classList.add("is-spin");
    ring.style.background = "";
    text.textContent = "";
    return;
  }
  const clamped = Math.max(0, Math.min(100, pct));
  ring.style.background = `conic-gradient(var(--accent) ${clamped}%, var(--line-soft) 0)`;
  text.textContent = `${clamped.toFixed(0)}%`;
}

// finishUpgrade paints the success state and reloads onto the new version.
function finishUpgrade() {
  upgrading = false;
  paintRingDone("is-ok", "✓");
  const phase = document.getElementById("upgradePhase");
  if (phase) phase.textContent = t("upgrade.success");
  const detail = document.getElementById("upgradeDetail");
  if (detail) detail.textContent = "";
  toast(t("upgrade.success"), true);
  setTimeout(() => location.reload(), 1200);
}

// failUpgrade paints the error state — the server has already rolled the old
// version back into place — and offers the only close button the overlay ever
// shows (the screen stays locked while an upgrade is in flight).
function failUpgrade(message) {
  upgrading = false;
  paintRingDone("is-err", "✕");
  const phase = document.getElementById("upgradePhase");
  if (phase) phase.textContent = message;
  const detail = document.getElementById("upgradeDetail");
  if (detail) detail.textContent = t("upgrade.rolledBack");
  const actions = document.getElementById("upgradeActions");
  if (actions) {
    actions.innerHTML = `<button class="ghost" id="upgradeCloseBtn">${t("upgrade.close")}</button>`;
    const btn = document.getElementById("upgradeCloseBtn");
    if (btn) btn.onclick = closeOverlay;
  }
}

function paintRingDone(mode, glyph) {
  const ring = document.getElementById("upgradeRing");
  const text = document.getElementById("upgradeRingText");
  if (!ring || !text) return;
  ring.classList.remove("is-spin");
  ring.classList.add(mode);
  ring.style.background = "";
  text.textContent = glyph;
}

function closeOverlay() {
  document.querySelector(".upgrade-overlay")?.remove();
}
