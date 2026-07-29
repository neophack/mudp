// MCP (Model Context Protocol): generate per-container access tokens so AI tools
// can connect to one container over SSE and operate inside that scoped runtime.

import { $, state, api, toast, copyText, renderView, canMutate, isAdmin, displayNameForUsername, t } from "../app.js";
import { showModal } from "./ui.js";
import { escapeHtml, formatDate } from "../lib/common.js";

// Tool groups shown in the UI. These mirror the tools registered on the server
// (internal/mcp/tools.go) and are presentation-only.
const MCP_TOOL_GROUPS = [
  {
    title: () => t("mcp.flowSelect").includes("选择") ? "文件与文本" : "Files & text",
    tools: [
      { name: "read_file", desc: () => t("mcp.flowSelect").includes("选择") ? "读取文本文件（最大 10 MiB）。" : "Read a text file (up to 10 MiB)." },
      { name: "write_file", desc: () => t("mcp.flowSelect").includes("选择") ? "创建或覆盖文本文件；自动创建父目录。" : "Create or overwrite a text file; parent dirs auto-created." },
      { name: "edit_file", desc: () => t("mcp.flowSelect").includes("选择") ? "对已有文件进行精确的定向编辑（无需整体重写）。" : "Make precise targeted edits to an existing file (no full rewrite)." },
      { name: "upload_file", desc: () => t("mcp.flowSelect").includes("选择") ? "从 base64 内容写入二进制文件（图片/压缩包）。" : "Write a binary file (image/archive) from base64 content." },
      { name: "download_file", desc: () => t("mcp.flowSelect").includes("选择") ? "将二进制文件读取为 base64 内容。" : "Read a binary file as base64 content." },
      { name: "list_files", desc: () => t("mcp.flowSelect").includes("选择") ? "浏览目录条目。" : "Browse directory entries." },
      { name: "copy_file", desc: () => t("mcp.flowSelect").includes("选择") ? "将文件或目录树复制到其他路径。" : "Copy a file or directory tree to another path." },
    ],
  },
  {
    title: () => t("mcp.flowSelect").includes("选择") ? "搜索" : "Search",
    tools: [
      { name: "glob", desc: () => t("mcp.flowSelect").includes("选择") ? "按名称模式查找文件/目录。无需 find/shell。" : "Find files/dirs by name pattern. Works without find/shell." },
      { name: "search_files", desc: () => t("mcp.flowSelect").includes("选择") ? "跨文件搜索文本，返回文件+行结果。无需 grep/shell。" : "Search text across files with file+line results. Works without grep/shell." },
    ],
  },
  {
    title: () => t("nav.containers"),
    tools: [
      { name: "exec_command", desc: () => t("mcp.flowSelect").includes("选择") ? "运行 shell 命令；返回 stdout、stderr 与退出码。" : "Run a shell command; returns stdout, stderr, and exit code." },
      { name: "get_logs", desc: () => t("mcp.flowSelect").includes("选择") ? "获取最近的 stdout 和 stderr 日志。" : "Fetch recent stdout and stderr logs." },
      { name: "get_info", desc: () => t("mcp.flowSelect").includes("选择") ? "查看状态、镜像、IP、端口、环境变量、标签、挂载、GPU。" : "Inspect state, image, IP, ports, env, labels, mounts, GPU." },
      { name: "start / stop / restart", desc: () => t("mcp.flowSelect").includes("选择") ? "控制容器生命周期。" : "Control the container lifecycle." },
    ],
  },
];

export async function renderMCP() {
  // The refresh engine fetches tokens into state.mcpTokens before calling this;
  // only fetch on first entry (no cached data yet) to avoid a redundant request
  // on every signature-driven repaint.
  if (!state.mcpTokens) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("mcp.loadingTokens")}</p></div></div>`;
    await refreshMCPTokens();
  }

  if (!state.mcpTokens) state.mcpTokens = [];
  const mutable = canMutate();
  const admin = isAdmin();
  const tokens = state.mcpTokens || [];
  const containers = (state.containers || []).filter((c) => c && c.id);
  const ownerCol = admin ? `<th>${t("mcp.ownerCol")}</th>` : "";
  const rows =
    tokens.map((tk) => tokenRow(tk, admin)).join("") ||
    `<tr class="empty-row"><td colspan="${admin ? 7 : 6}">${t("mcp.noTokens")}</td></tr>`;

  $("#view").innerHTML =
    `<div class="mcp-page">` +
      `<section class="card mcp-hero">` +
        `<div>` +
          `<span class="mcp-eyebrow">${t("mcp.remoteAgent")}</span>` +
          `<h2>${t("mcp.heroTitle")}</h2>` +
          `<p>${t("mcp.heroDesc")}</p>` +
        `</div>` +
        `<div class="mcp-flow" aria-label="MCP connection flow">` +
          `<span>${t("mcp.flowSelect")}</span><strong>1</strong>` +
          `<span>${t("mcp.flowCreate")}</span><strong>2</strong>` +
          `<span>${t("mcp.flowCopy")}</span><strong>3</strong>` +
        `</div>` +
      `</section>` +

      remoteAccessCard() +

      `<div class="mcp-main-grid">` +
        `<section class="card">` +
          `<div class="card-head"><h2>${t("mcp.agentTools")}</h2><span class="mcp-card-note">${t("mcp.scopedNote")}</span></div>` +
          `<div class="mcp-tool-grid">` +
            MCP_TOOL_GROUPS.map(
              (g) =>
                `<div class="mcp-tool-group">` +
                  `<h3 class="mcp-tool-group-title">${escapeHtml(typeof g.title === "function" ? g.title() : g.title)}</h3>` +
                  g.tools
                    .map(
                      (tool) =>
                        `<div class="mcp-tool">` +
                          `<code class="mcp-tool-name">${escapeHtml(tool.name)}</code>` +
                          `<span class="mcp-tool-desc">${escapeHtml(typeof tool.desc === "function" ? tool.desc() : tool.desc)}</span>` +
                        `</div>`,
                    )
                    .join("") +
                `</div>`,
            ).join("") +
          `</div>` +
        `</section>` +

        (mutable
          ? `<section class="card">` +
              `<div class="card-head"><h2>${t("mcp.createToken")}</h2></div>` +
              `<form id="mcpCreateForm" class="mcp-create-form">` +
                `<label>${t("mcp.containerLabel")}` +
                  `<select name="containerId" required>` +
                    `<option value="">${t("mcp.selectContainer")}</option>` +
                    `${containerOptions(containers)}` +
                  `</select>` +
                `</label>` +
                `<label>${t("mcp.labelLabel")}` +
                  `<input name="label" placeholder="${t("mcp.labelPlaceholder")}" maxlength="64">` +
                `</label>` +
                `<label>${t("mcp.expiresLabel")}` +
                  `<select name="expiresInHours">` +
                    `<option value="0">${t("mcp.expiresNever")}</option>` +
                    `<option value="24">${t("mcp.expires24")}</option>` +
                    `<option value="168">${t("mcp.expires7")}</option>` +
                    `<option value="720">${t("mcp.expires30")}</option>` +
                  `</select>` +
                `</label>` +
                `<button type="submit" class="primary" id="mcpCreateBtn">${t("mcp.createToken")}</button>` +
              `</form>` +
            `</section>`
          : `<section class="card"><div class="card-body"><p class="hint">${t("mcp.readonlyHint")}</p></div></section>`) +
      `</div>` +

      `<section class="card">` +
        `<div class="card-head"><h2>${t("mcp.tokens")}</h2></div>` +
        `<div class="mcp-table-wrap">` +
          `<table class="data">` +
            `<thead><tr>` +
              `<th>${t("mcp.containerLabel")}</th><th>${t("mcp.labelLabel")}</th>${ownerCol}<th>${t("mcp.colCreated")}</th><th>${t("mcp.colLastUsed")}</th><th>${t("mcp.colExpires")}</th><th class="actions">${t("common.actions")}</th>` +
            `</tr></thead>` +
            `<tbody>${rows}</tbody>` +
          `</table>` +
        `</div>` +
      `</section>` +
    `</div>`;

  if (mutable) {
    const form = $("#mcpCreateForm");
    if (form) form.onsubmit = onCreateToken;
  }
  document.querySelectorAll("[data-mcp-view]").forEach((btn) => {
    btn.onclick = () => onShowConfig(btn.dataset.mcpToken, btn.dataset.mcpName, btn.dataset.mcpLabel);
  });
  document.querySelectorAll("[data-mcp-del]").forEach((btn) => {
    btn.onclick = () => onDeleteToken(btn.dataset.mcpDel, btn.dataset.mcpName);
  });
  bindCopyButtons();
}

// bindCopyButtons wires every [data-copy] button in the current DOM. Used by
// both the page and the config modal, which is re-rendered on every open.
function bindCopyButtons() {
  document.querySelectorAll(".mcp-copy-btn").forEach((btn) => {
    btn.onclick = async () => {
      let text = btn.dataset.copy;
      const targetId = btn.dataset.copyTarget;
      if (targetId) {
        const el = document.getElementById(targetId);
        if (el) text = el.value;
      }
      if (!text) return;
      try {
        await copyText(text);
        const old = btn.textContent;
        btn.textContent = t("common.copied");
        setTimeout(() => { btn.textContent = old; }, 1200);
        toast(t("common.copied"), true);
      } catch {
        toast(t("common.copyFailed"));
      }
    };
  });
}

// remoteAccessCard shows the public link an admin published for MCP clients that
// cannot reach the console's LAN address. The domain is safe to show to anyone:
// it is useless without a token, and only reaches containers sitting on the
// configured safe network.
function remoteAccessCard() {
  const remote = state.mcpRemote;
  if (!remote || !remote.enabled) {
    const hint = isAdmin()
      ? t("mcp.externalAdminHint")
      : t("mcp.externalUserHint");
    return (
      `<section class="card">` +
        `<div class="card-head"><h2>${t("mcp.externalAccess")}</h2><span class="mcp-card-note">${t("mcp.notConfigured")}</span></div>` +
        `<div class="card-body"><p class="hint">${t("mcp.externalHintOff", { hint })}</p></div>` +
      `</section>`
    );
  }
  const base = remote.baseUrl || "";
  return (
    `<section class="card">` +
      `<div class="card-head"><h2>${t("mcp.externalAccess")}</h2><span class="mcp-card-note">${t("mcp.safeNetworkNote", { net: escapeHtml(remote.safeNetwork || "-") })}</span></div>` +
      `<div class="card-body">` +
        `<p class="hint">${t("mcp.externalHintOn")}</p>` +
        `<div class="mcp-copy-row">` +
          `<code class="mcp-code mcp-code-inline">${escapeHtml(base)}</code>` +
          `<button class="ghost mcp-copy-btn" data-copy="${escapeHtml(base)}">${t("common.copy")}</button>` +
        `</div>` +
        `<p class="hint">${t("mcp.onlyContainers", { net: escapeHtml(remote.safeNetwork || "") })}</p>` +
      `</div>` +
    `</section>`
  );
}

function containerOptions(containers) {
  if (containers.length === 0) {
    return `<option value="" disabled>${t("mcp.noContainers")}</option>`;
  }
  return containers
    .map((c) => {
      const name = c.name || c.fullName || c.id.slice(0, 12);
      return `<option value="${escapeHtml(c.id)}">${escapeHtml(name)}</option>`;
    })
    .join("");
}

// externalUrlFor returns the token's SSE endpoint on the published domain, or
// "" when there is no domain (or the token predates cleartext storage, in which
// case there is no URL to build).
function externalUrlFor(t) {
  const remote = state.mcpRemote;
  if (!remote || !remote.enabled || !remote.baseUrl || !t.token) return "";
  return `${remote.baseUrl}/mcp/${t.token}/sse`;
}

function tokenRow(tk, admin) {
  const expired = tk.expiresAt && new Date(tk.expiresAt) < new Date();
  const expCell = tk.expiresAt
    ? `<span class="${expired ? "badge badge-warn" : "secondary-line"}">${formatDate(tk.expiresAt)}</span>`
    : `<span class="secondary-line">${t("mcp.never")}</span>`;
  const ownerCell = admin ? `<td>${escapeHtml(displayNameForUsername(tk.owner) || "-")}</td>` : "";
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(tk.containerName || "-")}</div><div class="secondary-line mono">${escapeHtml((tk.containerId || "").slice(0, 12))}</div></td>` +
      `<td>${escapeHtml(tk.label || "-")}</td>` +
      ownerCell +
      `<td><div class="secondary-line">${formatDate(tk.createdAt)}</div></td>` +
      `<td><div class="secondary-line">${tk.lastUsedAt ? formatDate(tk.lastUsedAt) : t("mcp.never")}</div></td>` +
      `<td>${expCell}</td>` +
      `<td class="actions">` +
        `<button class="icon" title="${t("mcp.viewConfig")}" data-mcp-view="${escapeHtml(tk.id)}" data-mcp-token="${escapeHtml(tk.token || "")}" data-mcp-name="${escapeHtml(tk.containerName)}" data-mcp-label="${escapeHtml(tk.label)}">CFG</button>` +
        (externalUrlFor(tk) ? `<button class="icon mcp-copy-btn" title="${t("mcp.copyExternal")}" data-copy="${escapeHtml(externalUrlFor(tk))}">WWW</button>` : "") +
        (canMutate() ? `<button class="icon danger" title="${t("mcp.deleteToken")}" data-mcp-del="${escapeHtml(tk.id)}" data-mcp-name="${escapeHtml(tk.containerName)}">DEL</button>` : "") +
      `</td>` +
    `</tr>`
  );
}

async function onCreateToken(e) {
  e.preventDefault();
  const fd = new FormData(e.target);
  const containerId = fd.get("containerId");
  if (!containerId) {
    toast(t("mcp.selectContainerFirst"));
    return;
  }
  const btn = $("#mcpCreateBtn");
  btn.disabled = true;
  btn.textContent = t("mcp.creating");
  try {
    const res = await api("/api/mcp/tokens", {
      method: "POST",
      body: JSON.stringify({
        containerId,
        label: fd.get("label") || "",
        expiresInHours: Number(fd.get("expiresInHours")) || 0,
      }),
    });
    toast(t("mcp.tokenCreated"), true);
    await refreshMCPTokens();
    renderView();
    onShowConfigRaw(res.token, res.label || "");
  } catch (err) {
    toast(err.message || t("mcp.createFail"));
  } finally {
    btn.disabled = false;
    btn.textContent = t("mcp.createToken");
  }
}

async function onDeleteToken(id, name) {
  if (!confirm(t("mcp.deleteConfirm", { name }))) return;
  try {
    await api(`/api/mcp/tokens/${id}`, { method: "DELETE" });
    toast(t("mcp.tokenDeleted"), true);
    await refreshMCPTokens();
    renderView();
  } catch (err) {
    toast(err.message || t("mcp.tokenDeletedFail"));
  }
}

function onShowConfig(token, name, label) {
  // Tokens created after the plaintext-storage change carry their cleartext, so
  // the full config can be shown again. Older tokens (created when only the hash
  // was stored) have no recoverable cleartext — tell the user to recreate it.
  if (token) {
    onShowConfigRaw(token, label, false);
    return;
  }
  showModal({
    kind: "mcp",
    title: t("mcp.configTitle"),
    body:
      `<p>${t("mcp.oldTokenBody", { name: escapeHtml(name || "") })}</p>` +
      `<p class="hint">${t("mcp.oldTokenHint")}</p>`,
    foot: `<button class="primary" data-close>${t("common.ok")}</button>`,
  });
}

function onShowConfigRaw(token, label, placeholder = false) {
  const remote = state.mcpRemote;
  const localBase = window.location.origin;
  // The published domain, when an admin has one. Everything below is written
  // against a base origin so the LAN address and the external domain produce
  // the same artifacts with nothing but the host swapped.
  const remoteBase = remote && remote.enabled ? remote.baseUrl || "" : "";
  const labelSlug = label ? label.replace(/[^a-zA-Z0-9_-]/g, "-") || "mudp-container" : "mudp-container";
  let scope = "local";
  let transport = "sse";
  const baseFor = (s) => (s === "remote" && remoteBase ? remoteBase : localBase);
  const sseUrlFor = (base) => `${base}/mcp/${token}/sse`;
  const httpUrlFor = (base) => `${base}/mcp/${token}`;

  // Build the config object for a given transport and base origin.
  const buildConfig = (transport, base) =>
    JSON.stringify(
      {
        mcpServers: {
          [labelSlug]: {
            type: transport === "http" ? "http" : "sse",
            url: transport === "http" ? httpUrlFor(base) : sseUrlFor(base),
          },
        },
      },
      null,
      2,
    );

  const buildCurlExample = (transport, base) =>
    transport === "http"
      ? `# Streamable HTTP: one POST per JSON-RPC request.\n` +
        `curl -X POST "${httpUrlFor(base)}" \\\n` +
        `  -H "Content-Type: application/json" \\\n` +
        `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`
      : `# 1. Open the SSE stream to get the message endpoint:\n` +
        `curl -N ${sseUrlFor(base)}\n\n` +
        `# 2. POST a JSON-RPC request to the endpoint URL printed above, e.g.:\n` +
        `curl -X POST "${base}/mcp/${token}/messages?session=SESSION_ID" \\\n` +
        `  -H "Content-Type: application/json" \\\n` +
        `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`;

  const endpointFor = (transport, base) =>
    transport === "http" ? httpUrlFor(base) : sseUrlFor(base);

  const transportHint = (transport) =>
    transport === "http"
      ? t("mcp.transportHintHttp")
      : t("mcp.transportHintSse");

  const scopeHint = (scope) =>
    scope === "remote"
      ? t("mcp.scopeRemoteHint", { domain: escapeHtml(remote?.domain || ""), safe: escapeHtml(remote?.safeNetwork || "safe") })
      : t("mcp.scopeLocalHint");

  // Offered only when an admin published a domain; otherwise there is one
  // possible address and a toggle would just be a dead control.
  const scopeToggle = remoteBase
    ? `<div class="mcp-transport-row">` +
        `<h4>${t("mcp.whereFrom")}</h4>` +
        `<div class="mcp-transport-toggle" role="tablist" aria-label="Access scope">` +
          `<button class="mcp-scope-btn active" data-scope="local" role="tab" aria-selected="true">${t("mcp.thisNetwork")}</button>` +
          `<button class="mcp-scope-btn" data-scope="remote" role="tab" aria-selected="false">${t("mcp.external")}</button>` +
        `</div>` +
      `</div>` +
      `<p class="hint" id="mcpScopeHint">${scopeHint("local")}</p>`
    : "";

  const note = placeholder
    ? `<p class="hint">${t("mcp.notePlaceholder")}</p>`
    : `<p class="hint">${t("mcp.noteDone")}</p>`;

  const body =
    note +
    (scopeToggle ? `<div class="mcp-config-section">${scopeToggle}</div>` : "") +
    `<div class="mcp-config-section">` +
      `<div class="mcp-transport-row">` +
        `<h4>${t("mcp.mcpConfig")}</h4>` +
        `<div class="mcp-transport-toggle" role="tablist" aria-label="Transport">` +
          `<button class="mcp-transport-btn active" data-transport="sse" role="tab" aria-selected="true">SSE</button>` +
          `<button class="mcp-transport-btn" data-transport="http" role="tab" aria-selected="false">HTTP</button>` +
        `</div>` +
      `</div>` +
      `<textarea class="mcp-code mcp-config-editor" id="mcpConfigEditor" spellcheck="false">${escapeHtml(buildConfig("sse", localBase))}</textarea>` +
      `<div class="mcp-config-actions">` +
        `<button class="primary mcp-copy-btn" data-copy-target="mcpConfigEditor">${t("mcp.copyConfig")}</button>` +
      `</div>` +
      `<p class="hint" id="mcpTransportHint">${transportHint("sse")}</p>` +
    `</div>` +
    `<div class="mcp-config-section">` +
      `<h4>${t("mcp.endpoint")}</h4>` +
      `<div class="mcp-copy-row">` +
        `<code class="mcp-code mcp-code-inline" id="mcpEndpointLabel">${escapeHtml(sseUrlFor(localBase))}</code>` +
        `<button class="ghost mcp-copy-btn" data-copy="${escapeHtml(sseUrlFor(localBase))}" id="mcpEndpointCopy">${t("common.copy")}</button>` +
      `</div>` +
    `</div>` +
    `<div class="mcp-config-section">` +
      `<h4>${t("mcp.testCurl")}</h4>` +
      `<div class="mcp-copy-row">` +
        `<pre class="mcp-code" id="mcpCurlExample">${escapeHtml(buildCurlExample("sse", localBase))}</pre>` +
        `<button class="ghost mcp-copy-btn" data-copy="${escapeHtml(buildCurlExample("sse", localBase))}" id="mcpCurlCopy">${t("common.copy")}</button>` +
      `</div>` +
    `</div>`;

  showModal({
    kind: "mcp",
    title: t("mcp.configTitle"),
    body,
    foot: `<button class="primary" data-close>${t("common.done")}</button>`,
  });

  // Rewrite the config editor, endpoint, and curl example from the current
  // transport + scope, so every copyable artifact in the dialog agrees with the
  // buttons above it.
  const applySelection = () => {
    const base = baseFor(scope);
    const editor = document.getElementById("mcpConfigEditor");
    if (editor) editor.value = buildConfig(transport, base);
    const hint = document.getElementById("mcpTransportHint");
    if (hint) hint.textContent = transportHint(transport);
    const scopeEl = document.getElementById("mcpScopeHint");
    if (scopeEl) scopeEl.innerHTML = scopeHint(scope);
    const endpointLabel = document.getElementById("mcpEndpointLabel");
    const endpointCopy = document.getElementById("mcpEndpointCopy");
    if (endpointLabel) endpointLabel.textContent = `${transport.toUpperCase()}: ${endpointFor(transport, base)}`;
    if (endpointCopy) endpointCopy.dataset.copy = endpointFor(transport, base);
    const curlEl = document.getElementById("mcpCurlExample");
    const curlCopy = document.getElementById("mcpCurlCopy");
    const curlText = buildCurlExample(transport, base);
    if (curlEl) curlEl.textContent = curlText;
    if (curlCopy) curlCopy.dataset.copy = curlText;
  };

  // selectIn marks the clicked button active within its own toggle group.
  const selectIn = (selector, btn) => {
    document.querySelectorAll(selector).forEach((b) => {
      const active = b === btn;
      b.classList.toggle("active", active);
      b.setAttribute("aria-selected", active ? "true" : "false");
    });
  };

  document.querySelectorAll(".mcp-transport-btn").forEach((btn) => {
    btn.onclick = () => {
      transport = btn.dataset.transport;
      selectIn(".mcp-transport-btn", btn);
      applySelection();
    };
  });
  document.querySelectorAll(".mcp-scope-btn").forEach((btn) => {
    btn.onclick = () => {
      scope = btn.dataset.scope;
      selectIn(".mcp-scope-btn", btn);
      applySelection();
    };
  });

  bindCopyButtons();
}

export async function refreshMCPTokens() {
  try {
    state.mcpTokens = (await api("/api/mcp/tokens")) || [];
  } catch {
    state.mcpTokens = [];
  }
  // The public hostname an admin bound to the MCP-only listener, if any. Kept
  // next to the tokens because every link the page offers is built from it.
  try {
    state.mcpRemote = (await api("/api/mcp/remote")) || null;
  } catch {
    state.mcpRemote = null;
  }
}
