import { isAdmin, canMutate, state, t } from "../app.js";

export function renderHelp() {
  const admin = isAdmin();
  const editable = canMutate();
  const version = state.me?.version || "dev";

  const userBasics = [
    t("help.b1"),
    t("help.b2"),
    t("help.b3"),
    t("help.b4"),
    t("help.b5"),
    t("help.b6"),
  ];

  const adminBasics = [
    t("help.a1"),
    t("help.a2"),
    t("help.a3"),
    t("help.a4"),
  ];

  const quickTroubleshooting = [
    t("help.t1"),
    t("help.t2"),
    t("help.t3"),
    t("help.t4"),
  ];

  const tips = [
    editable ? t("help.tipMutable") : t("help.tipReadonly"),
    admin ? t("help.tipAdmin") : t("help.tipNonAdmin"),
  ];

  // The version chip is platform information — admins only (meUser.Version is
  // empty for every other role).
  const versionChip = admin ? `<span class="hint">v${escapeHtml(version)}</span>` : "";

  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="card"><div class="card-head"><h2>${t("help.gettingStarted")}</h2>${versionChip}</div><div class="card-body">` +
        `<p class="hint" style="margin:0 0 10px">${t("help.intro")}</p>` +
        `<div class="grid two">` +
          `<section class="stack">` +
            `<div class="card">` +
              `<div class="card-head"><h2>${t("help.forAllUsers")}</h2></div>` +
              `<div class="card-body"><ol>${userBasics.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ol></div>` +
            `</div>` +
            `<div class="card">` +
              `<div class="card-head"><h2>${t("help.tipsAccount")}</h2></div>` +
              `<div class="card-body"><ul>${tips.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ul></div>` +
            `</div>` +
          `</section>` +
          `<section class="stack">` +
            `<div class="card">` +
              `<div class="card-head"><h2>${t("help.troubleshooting")}</h2></div>` +
              `<div class="card-body"><ul>${quickTroubleshooting.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ul></div>` +
            `</div>` +
            (admin
              ? `<div class="card">` +
                `<div class="card-head"><h2>${t("help.adminGuide")}</h2></div>` +
                `<div class="card-body"><ol>${adminBasics.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ol></div>` +
              `</div>`
              : `<div class="card"><div class="card-head"><h2>${t("help.needAdminHelp")}</h2></div><div class="card-body"><p class="hint" style="margin:0">${t("help.needAdminHint")}</p></div></div>`
            ) +
          `</section>` +
        `</div>` +
      `</div></div>` +
    `</div>`;
}

function $(selector) {
  return document.querySelector(selector);
}

function escapeHtml(value) {
  return String(value || "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
