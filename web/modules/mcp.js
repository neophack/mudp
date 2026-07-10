// MCP (Model Context Protocol): generate per-container access tokens so AI tools
// can connect to one container over SSE and operate inside that scoped runtime.

import { $, state, api, toast, renderView, canMutate, isAdmin } from "../app.js";
import { showModal } from "./ui.js";
import { escapeHtml, formatDate } from "../lib/common.js";

// Tool groups shown in the UI. These mirror the tools registered on the server
// (internal/mcp/tools.go) and are presentation-only.
const MCP_TOOL_GROUPS = [
  {
    title: "Files & text",
    tools: [
      { name: "read_file", desc: "Read a text file (up to 10 MiB)." },
      { name: "write_file", desc: "Create or overwrite a text file; parent dirs auto-created." },
      { name: "edit_file", desc: "Make precise targeted edits to an existing file (no full rewrite)." },
      { name: "upload_file", desc: "Write a binary file (image/archive) from base64 content." },
      { name: "download_file", desc: "Read a binary file as base64 content." },
      { name: "list_files", desc: "Browse directory entries." },
      { name: "copy_file", desc: "Copy a file or directory tree to another path." },
    ],
  },
  {
    title: "Search",
    tools: [
      { name: "glob", desc: "Find files/dirs by name pattern. Works without find/shell." },
      { name: "search_files", desc: "Search text across files with file+line results. Works without grep/shell." },
    ],
  },
  {
    title: "Container",
    tools: [
      { name: "exec_command", desc: "Run a shell command; returns stdout, stderr, and exit code." },
      { name: "get_logs", desc: "Fetch recent stdout and stderr logs." },
      { name: "get_info", desc: "Inspect state, image, IP, ports, env, labels, mounts, GPU." },
      { name: "start / stop / restart", desc: "Control the container lifecycle." },
    ],
  },
];

export async function renderMCP() {
  // Show a loading placeholder, then fetch the latest tokens so the view always
  // reflects the database — not whatever the boot-time background load happened
  // to populate (which races with rendering and caused tokens to appear only
  // after a few manual refreshes).
  $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">Loading MCP tokens…</p></div></div>`;
  await refreshMCPTokens();

  if (!state.mcpTokens) state.mcpTokens = [];
  const mutable = canMutate();
  const admin = isAdmin();
  const tokens = state.mcpTokens || [];
  const containers = (state.containers || []).filter((c) => c && c.id);
  const ownerCol = admin ? "<th>Owner</th>" : "";
  const rows =
    tokens.map((t) => tokenRow(t, admin)).join("") ||
    `<tr class="empty-row"><td colspan="${admin ? 7 : 6}">No MCP tokens yet. Create one to let an AI tool connect to a container.</td></tr>`;

  $("#view").innerHTML =
    `<div class="mcp-page">` +
      `<section class="card mcp-hero">` +
        `<div>` +
          `<span class="mcp-eyebrow">Remote agent access</span>` +
          `<h2>Connect an AI tool to exactly one container</h2>` +
          `<p>Generate a per-container token, paste the SSE config into Codex, Claude Code, Kimi, Cursor, or another MCP client, then let the agent work inside that container. Runtime hints come from container labels such as mcp.capability=go,nodejs.</p>` +
        `</div>` +
        `<div class="mcp-flow" aria-label="MCP connection flow">` +
          `<span>Select container</span><strong>1</strong>` +
          `<span>Create token</span><strong>2</strong>` +
          `<span>Copy SSE config</span><strong>3</strong>` +
        `</div>` +
      `</section>` +

      `<div class="mcp-main-grid">` +
        `<section class="card">` +
          `<div class="card-head"><h2>Agent tools</h2><span class="mcp-card-note">Scoped to the selected container</span></div>` +
          `<div class="mcp-tool-grid">` +
            MCP_TOOL_GROUPS.map(
              (g) =>
                `<div class="mcp-tool-group">` +
                  `<h3 class="mcp-tool-group-title">${escapeHtml(g.title)}</h3>` +
                  g.tools
                    .map(
                      (t) =>
                        `<div class="mcp-tool">` +
                          `<code class="mcp-tool-name">${escapeHtml(t.name)}</code>` +
                          `<span class="mcp-tool-desc">${escapeHtml(t.desc)}</span>` +
                        `</div>`,
                    )
                    .join("") +
                `</div>`,
            ).join("") +
          `</div>` +
        `</section>` +

        (mutable
          ? `<section class="card">` +
              `<div class="card-head"><h2>Create token</h2></div>` +
              `<form id="mcpCreateForm" class="mcp-create-form">` +
                `<label>Container` +
                  `<select name="containerId" required>` +
                    `<option value="">Select a container...</option>` +
                    `${containerOptions(containers)}` +
                  `</select>` +
                `</label>` +
                `<label>Label` +
                  `<input name="label" placeholder="e.g. codex-java-lab" maxlength="64">` +
                `</label>` +
                `<label>Expires` +
                  `<select name="expiresInHours">` +
                    `<option value="0">Never</option>` +
                    `<option value="24">24 hours</option>` +
                    `<option value="168">7 days</option>` +
                    `<option value="720">30 days</option>` +
                  `</select>` +
                `</label>` +
                `<button type="submit" class="primary" id="mcpCreateBtn">Create token</button>` +
              `</form>` +
            `</section>`
          : `<section class="card"><div class="card-body"><p class="hint">Your role is read-only and cannot create MCP tokens. Ask an admin or operator.</p></div></section>`) +
      `</div>` +

      `<section class="card">` +
        `<div class="card-head"><h2>Tokens</h2></div>` +
        `<div class="mcp-table-wrap">` +
          `<table class="data">` +
            `<thead><tr>` +
              `<th>Container</th><th>Label</th>${ownerCol}<th>Created</th><th>Last used</th><th>Expires</th><th class="actions">Actions</th>` +
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
}

function containerOptions(containers) {
  if (containers.length === 0) {
    return `<option value="" disabled>No containers available</option>`;
  }
  return containers
    .map((c) => {
      const name = c.name || c.fullName || c.id.slice(0, 12);
      return `<option value="${escapeHtml(c.id)}">${escapeHtml(name)}</option>`;
    })
    .join("");
}

function tokenRow(t, admin) {
  const expired = t.expiresAt && new Date(t.expiresAt) < new Date();
  const expCell = t.expiresAt
    ? `<span class="${expired ? "badge badge-warn" : "secondary-line"}">${formatDate(t.expiresAt)}</span>`
    : `<span class="secondary-line">never</span>`;
  const ownerCell = admin ? `<td>${escapeHtml(t.owner || "-")}</td>` : "";
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(t.containerName || "-")}</div><div class="secondary-line mono">${escapeHtml((t.containerId || "").slice(0, 12))}</div></td>` +
      `<td>${escapeHtml(t.label || "-")}</td>` +
      ownerCell +
      `<td><div class="secondary-line">${formatDate(t.createdAt)}</div></td>` +
      `<td><div class="secondary-line">${t.lastUsedAt ? formatDate(t.lastUsedAt) : "never"}</div></td>` +
      `<td>${expCell}</td>` +
      `<td class="actions">` +
        `<button class="icon" title="View config" data-mcp-view="${escapeHtml(t.id)}" data-mcp-token="${escapeHtml(t.token || "")}" data-mcp-name="${escapeHtml(t.containerName)}" data-mcp-label="${escapeHtml(t.label)}">CFG</button>` +
        (canMutate() ? `<button class="icon danger" title="Delete" data-mcp-del="${escapeHtml(t.id)}" data-mcp-name="${escapeHtml(t.containerName)}">DEL</button>` : "") +
      `</td>` +
    `</tr>`
  );
}

async function onCreateToken(e) {
  e.preventDefault();
  const fd = new FormData(e.target);
  const containerId = fd.get("containerId");
  if (!containerId) {
    toast("Select a container first");
    return;
  }
  const btn = $("#mcpCreateBtn");
  btn.disabled = true;
  btn.textContent = "Creating...";
  try {
    const res = await api("/api/mcp/tokens", {
      method: "POST",
      body: JSON.stringify({
        containerId,
        label: fd.get("label") || "",
        expiresInHours: Number(fd.get("expiresInHours")) || 0,
      }),
    });
    toast("Token created", true);
    await refreshMCPTokens();
    renderView();
    onShowConfigRaw(res.token, res.label || "");
  } catch (err) {
    toast(err.message || "Failed to create token");
  } finally {
    btn.disabled = false;
    btn.textContent = "Create token";
  }
}

async function onDeleteToken(id, name) {
  if (!confirm(`Delete the MCP token for "${name}"? The connected AI tool will lose access immediately.`)) return;
  try {
    await api(`/api/mcp/tokens/${id}`, { method: "DELETE" });
    toast("Token deleted", true);
    await refreshMCPTokens();
    renderView();
  } catch (err) {
    toast(err.message || "Failed to delete token");
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
    title: "MCP Configuration",
    body:
      `<p>This token (for <strong>${escapeHtml(name || "")}</strong>) was created before tokens were stored in cleartext, so its full value can't be shown again.</p>` +
      `<p class="hint">Delete this token and create a new one to get a copyable config.</p>`,
    foot: `<button class="primary" data-close>OK</button>`,
  });
}

function onShowConfigRaw(token, label, placeholder = false) {
  const host = window.location.origin;
  const labelSlug = label ? label.replace(/[^a-zA-Z0-9_-]/g, "-") || "mudp-container" : "mudp-container";
  const sseUrl = `${host}/mcp/${token}/sse`;
  const httpUrl = `${host}/mcp/${token}`;

  // Build the config object for a given transport.
  const buildConfig = (transport) => {
    if (transport === "http") {
      return JSON.stringify(
        {
          mcpServers: {
            [labelSlug]: {
              type: "http",
              url: httpUrl,
            },
          },
        },
        null,
        2,
      );
    }
    return JSON.stringify(
      {
        mcpServers: {
          [labelSlug]: {
            type: "sse",
            url: sseUrl,
          },
        },
      },
      null,
      2,
    );
  };

  const buildCurlExample = (transport) =>
    transport === "http"
      ? `# Streamable HTTP: one POST per JSON-RPC request.\n` +
        `curl -X POST "${httpUrl}" \\\n` +
        `  -H "Content-Type: application/json" \\\n` +
        `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`
      : `# 1. Open the SSE stream to get the message endpoint:\n` +
        `curl -N ${sseUrl}\n\n` +
        `# 2. POST a JSON-RPC request to the endpoint URL printed above, e.g.:\n` +
        `curl -X POST "${host}/mcp/${token}/messages?session=SESSION_ID" \\\n` +
        `  -H "Content-Type: application/json" \\\n` +
        `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`;

  const endpointFor = (transport) =>
    transport === "http" ? httpUrl : sseUrl;

  const note = placeholder
    ? `<p class="hint">Replace <code>YOUR_TOKEN_HERE</code> with the token from this row.</p>`
    : `<p class="hint">Copy the config below into your AI tool's MCP settings. You can reopen this dialog anytime from the token's CFG button.</p>`;

  const body =
    note +
    `<div class="mcp-config-section">` +
      `<div class="mcp-transport-row">` +
        `<h4>MCP config</h4>` +
        `<div class="mcp-transport-toggle" role="tablist" aria-label="Transport">` +
          `<button class="mcp-transport-btn active" data-transport="sse" role="tab" aria-selected="true">SSE</button>` +
          `<button class="mcp-transport-btn" data-transport="http" role="tab" aria-selected="false">HTTP</button>` +
        `</div>` +
      `</div>` +
      `<textarea class="mcp-code mcp-config-editor" id="mcpConfigEditor" spellcheck="false">${escapeHtml(buildConfig("sse"))}</textarea>` +
      `<div class="mcp-config-actions">` +
        `<button class="primary mcp-copy-btn" data-copy-target="mcpConfigEditor">Copy config</button>` +
      `</div>` +
      `<p class="hint" id="mcpTransportHint">Paste this into your AI tool's MCP settings. SSE is the classic remote transport; some clients support it out of the box.</p>` +
    `</div>` +
    `<div class="mcp-config-section">` +
      `<h4>Endpoint</h4>` +
      `<div class="mcp-copy-row">` +
        `<code class="mcp-code mcp-code-inline" id="mcpEndpointLabel">SSE: ${escapeHtml(sseUrl)}</code>` +
        `<button class="ghost mcp-copy-btn" data-copy="${escapeHtml(sseUrl)}" id="mcpEndpointCopy">Copy</button>` +
      `</div>` +
    `</div>` +
    `<div class="mcp-config-section">` +
      `<h4>Test with curl</h4>` +
      `<div class="mcp-copy-row">` +
        `<pre class="mcp-code" id="mcpCurlExample">${escapeHtml(buildCurlExample("sse"))}</pre>` +
        `<button class="ghost mcp-copy-btn" data-copy="${escapeHtml(buildCurlExample("sse"))}" id="mcpCurlCopy">Copy</button>` +
      `</div>` +
    `</div>`;

  showModal({
    kind: "mcp",
    title: "MCP Configuration",
    body,
    foot: `<button class="primary" data-close>Done</button>`,
  });

  // Switching transport rewrites the config editor, endpoint, and curl example
  // so the copied artifacts always match the selected transport.
  document.querySelectorAll(".mcp-transport-btn").forEach((btn) => {
    btn.onclick = () => {
      const transport = btn.dataset.transport;
      document.querySelectorAll(".mcp-transport-btn").forEach((b) => {
        const active = b === btn;
        b.classList.toggle("active", active);
        b.setAttribute("aria-selected", active ? "true" : "false");
      });
      const editor = document.getElementById("mcpConfigEditor");
      if (editor) editor.value = buildConfig(transport);
      const hint = document.getElementById("mcpTransportHint");
      if (hint) {
        hint.textContent =
          transport === "http"
            ? "Streamable HTTP is the modern transport: one POST per request, no session needed. Works with clients that support the http MCP type."
            : "Paste this into your AI tool's MCP settings. SSE is the classic remote transport; some clients support it out of the box.";
      }
      const endpointLabel = document.getElementById("mcpEndpointLabel");
      const endpointCopy = document.getElementById("mcpEndpointCopy");
      if (endpointLabel) endpointLabel.textContent = `${transport.toUpperCase()}: ${endpointFor(transport)}`;
      if (endpointCopy) endpointCopy.dataset.copy = endpointFor(transport);
      const curlEl = document.getElementById("mcpCurlExample");
      const curlCopy = document.getElementById("mcpCurlCopy");
      const curlText = buildCurlExample(transport);
      if (curlEl) curlEl.textContent = curlText;
      if (curlCopy) curlCopy.dataset.copy = curlText;
    };
  });

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
        await navigator.clipboard.writeText(text);
        const old = btn.textContent;
        btn.textContent = "Copied";
        setTimeout(() => { btn.textContent = old; }, 1200);
        toast("Copied", true);
      } catch {
        toast("Copy failed; select the text manually");
      }
    };
  });
}

export async function refreshMCPTokens() {
  try {
    state.mcpTokens = (await api("/api/mcp/tokens")) || [];
  } catch {
    state.mcpTokens = [];
  }
}
