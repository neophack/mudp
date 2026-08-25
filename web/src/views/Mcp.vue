<template>
  <div class="mcp-page">
    <section class="card mcp-hero">
      <div>
        <span class="mcp-eyebrow">{{ tt("mcp.remoteAgent") }}</span>
        <h2>{{ tt("mcp.heroTitle") }}</h2>
        <p>{{ tt("mcp.heroDesc") }}</p>
      </div>
      <div class="mcp-flow" aria-label="MCP connection flow">
        <span>{{ tt("mcp.flowSelect") }}</span><strong>1</strong>
        <span>{{ tt("mcp.flowCreate") }}</span><strong>2</strong>
        <span>{{ tt("mcp.flowCopy") }}</span><strong>3</strong>
      </div>
    </section>

    <!-- Public link for MCP clients that cannot reach the LAN address. The
         domain is safe to show: it is useless without a token and only
         reaches containers on the configured safe network. -->
    <section class="card">
      <div class="card-head">
        <h2>{{ tt("mcp.externalAccess") }}</h2>
        <span class="mcp-card-note">{{ remote && remote.enabled ? tt("mcp.safeNetworkNote", { net: remote.safeNetwork || "-" }) : tt("mcp.notConfigured") }}</span>
      </div>
      <template v-if="remote && remote.enabled">
        <p class="hint">{{ tt("mcp.externalHintOn") }}</p>
        <div class="mcp-copy-row">
          <code class="mcp-code mcp-code-inline">{{ remote.baseUrl || "" }}</code>
          <el-button size="small" @click="copy(remote.baseUrl || '')">{{ tt("common.copy") }}</el-button>
        </div>
        <p class="hint">{{ tt("mcp.onlyContainers", { net: remote.safeNetwork || "" }) }}</p>
      </template>
      <p v-else class="hint">{{ tt("mcp.externalHintOff", { hint: isAdmin() ? tt("mcp.externalAdminHint") : tt("mcp.externalUserHint") }) }}</p>
    </section>

    <div class="mcp-main-grid">
      <section class="card">
        <div class="card-head">
          <h2>{{ tt("mcp.agentTools") }}</h2>
          <span class="mcp-card-note">{{ tt("mcp.scopedNote") }}</span>
        </div>
        <div class="mcp-tool-grid">
          <div v-for="g in toolGroups" :key="g.title" class="mcp-tool-group">
            <h3 class="mcp-tool-group-title">{{ g.title }}</h3>
            <div v-for="tool in g.tools" :key="tool.name" class="mcp-tool">
              <code class="mcp-tool-name">{{ tool.name }}</code>
              <span class="mcp-tool-desc">{{ tool.desc }}</span>
            </div>
          </div>
        </div>
      </section>

      <section v-if="canMutate()" class="card">
        <div class="card-head"><h2>{{ tt("mcp.createToken") }}</h2></div>
        <el-form label-position="top" size="small" @submit.prevent="createToken">
          <el-form-item :label="tt('mcp.containerLabel')">
            <el-select v-model="form.containerId" style="width: 100%" required>
              <el-option value="" :label="tt('mcp.selectContainer')" :disabled="!containers.length" />
              <el-option v-for="c in containers" :key="c.id" :value="c.id" :label="c.name || c.fullName || c.id.slice(0, 12)" />
            </el-select>
          </el-form-item>
          <el-form-item :label="tt('mcp.labelLabel')">
            <el-input v-model="form.label" :placeholder="tt('mcp.labelPlaceholder')" maxlength="64" />
          </el-form-item>
          <el-form-item :label="tt('mcp.expiresLabel')">
            <el-select v-model="form.expiresInHours" style="width: 100%">
              <el-option :value="0" :label="tt('mcp.expiresNever')" />
              <el-option :value="24" :label="tt('mcp.expires24')" />
              <el-option :value="168" :label="tt('mcp.expires7')" />
              <el-option :value="720" :label="tt('mcp.expires30')" />
            </el-select>
          </el-form-item>
          <el-button type="primary" native-type="submit" :loading="creating">{{ tt("mcp.createToken") }}</el-button>
        </el-form>
      </section>
      <section v-else class="card">
        <p class="hint">{{ tt("mcp.readonlyHint") }}</p>
      </section>
    </div>

    <section class="card">
      <div class="card-head"><h2>{{ tt("mcp.tokens") }}</h2></div>
      <el-table
        :data="s.mcpTokens"
        size="small"
        :empty-text="tt('mcp.noTokens')"
        :row-class-name="s.isMobile ? 'row-tappable' : ''"
        @row-click="onRowClick"
      >
        <el-table-column :label="tt('mcp.containerLabel')" :min-width="s.isMobile ? 150 : 170">
          <template #default="{ row }">
            <div class="primary-line">
              <span v-if="row.inUse" class="mcp-live-dot" :title="tt('mcp.inUse')"></span>
              {{ row.containerName || "-" }}
            </div>
            <div class="secondary-line mono">{{ (row.containerId || "").slice(0, 12) }}</div>
            <div v-if="s.isMobile && row.label" class="secondary-line">{{ row.label }}</div>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('mcp.labelLabel')" prop="label" min-width="110">
          <template #default="{ row }">{{ row.label || "-" }}</template>
        </el-table-column>
        <el-table-column v-if="isAdmin() && !s.isMobile" :label="tt('mcp.ownerCol')" width="120">
          <template #default="{ row }">{{ displayNameForUsername(row.owner) || "-" }}</template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('mcp.colCreated')" width="150">
          <template #default="{ row }"><span class="secondary-line">{{ formatDate(row.createdAt) }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('mcp.colLastUsed')" width="150">
          <template #default="{ row }"><span class="secondary-line">{{ row.lastUsedAt ? formatDate(row.lastUsedAt) : tt("mcp.never") }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('mcp.colExpires')" :width="s.isMobile ? 92 : 130">
          <template #default="{ row }">
            <el-tag v-if="row.expiresAt && expired(row)" size="small" type="warning">{{ formatDate(row.expiresAt) }}</el-tag>
            <span v-else class="secondary-line">{{ row.expiresAt ? formatDate(row.expiresAt) : tt("mcp.never") }}</span>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" width="210" fixed="right">
          <template #default="{ row }">
            <el-button link size="small" :title="tt('mcp.viewConfig')" @click="showConfig(row)">CFG</el-button>
            <el-button link size="small" :title="tt('mcp.usageLog')" @click="showUsage(row)">LOG</el-button>
            <el-button v-if="externalUrlFor(row)" link size="small" :title="tt('mcp.copyExternal')" @click="copy(externalUrlFor(row))">WWW</el-button>
            <!-- The external key only matters once an admin published a remote
                 domain; on a LAN deployment there is nothing to authenticate. -->
            <el-button v-if="canMutate() && remotePublished" link size="small" :title="tt('mcp.generateExternalKey')" @click="generateKey(row)">GEN</el-button>
            <el-button v-if="canMutate()" link size="small" class="danger-text" :title="tt('mcp.deleteToken')" @click="deleteToken(row)">DEL</el-button>
          </template>
        </el-table-column>
      </el-table>

      <!-- Phone-width rows: tap for the bottom action sheet. -->
      <action-sheet
        v-model:visible="sheet.visible"
        :title="sheet.row?.containerName || ''"
        :subtitle="sheetSubtitle"
        :items="sheetItems"
        :columns="4"
        @select="onSheetSelect"
      />
    </section>

    <!-- Copyable config dialog -->
    <el-dialog v-model="config.visible" :title="tt('mcp.configTitle')" width="640px" top="4vh" append-to-body>
      <template v-if="config.oldToken">
        <p>{{ tt("mcp.oldTokenBody", { name: config.name || "" }) }}</p>
        <p class="hint">{{ tt("mcp.oldTokenHint") }}</p>
      </template>
      <template v-else>
        <p class="hint">{{ config.placeholder ? tt("mcp.notePlaceholder") : tt("mcp.noteDone") }}</p>
        <!-- Scope toggle offered only when an admin published a domain AND this
             token's container is on the safe network. -->
        <template v-if="config.remoteBase">
          <div class="mcp-config-section">
            <h4>{{ tt("mcp.whereFrom") }}</h4>
            <el-radio-group v-model="config.scope" size="small">
              <el-radio-button value="local">{{ tt("mcp.thisNetwork") }}</el-radio-button>
              <el-radio-button value="remote">{{ tt("mcp.external") }}</el-radio-button>
            </el-radio-group>
            <p class="hint">{{ scopeHint }}</p>
          </div>
        </template>
        <p v-else-if="domainPublished" class="hint">{{ tt("mcp.scopeLocalOnlyHint", { net: remote?.safeNetwork || "" }) }}</p>
        <div class="mcp-config-section">
          <div class="mcp-transport-row">
            <h4>{{ tt("mcp.mcpConfig") }}</h4>
            <el-radio-group v-model="config.transport" size="small">
              <el-radio-button value="sse">SSE</el-radio-button>
              <el-radio-button value="http">HTTP</el-radio-button>
            </el-radio-group>
          </div>
          <el-input v-model="configText" type="textarea" :rows="8" class="mono" spellcheck="false" />
          <div class="mcp-config-actions">
            <el-button type="primary" size="small" @click="copy(configText)">{{ tt("mcp.copyConfig") }}</el-button>
          </div>
          <p class="hint">{{ transportHint }}</p>
        </div>
        <div v-if="config.scope === 'remote'" class="mcp-config-section">
          <h4>{{ tt("mcp.authHeader") }}</h4>
          <template v-if="config.externalKey">
            <p class="hint">{{ tt("mcp.authHeaderHint") }}</p>
            <div class="mcp-copy-row">
              <pre class="mcp-code">{{ authHeaderJson }}</pre>
              <el-button size="small" @click="copy(authHeaderJson)">{{ tt("common.copy") }}</el-button>
            </div>
            <div class="mcp-copy-row">
              <code class="mcp-code mcp-code-inline">Bearer {{ config.externalKey }}</code>
              <el-button size="small" @click="copy(`Bearer ${config.externalKey}`)">{{ tt("common.copy") }}</el-button>
            </div>
          </template>
          <p v-else class="hint">{{ tt("mcp.noExternalKeyHint") }}</p>
        </div>
        <div class="mcp-config-section">
          <h4>{{ tt("mcp.endpoint") }}</h4>
          <div class="mcp-copy-row">
            <code class="mcp-code mcp-code-inline">{{ config.transport.toUpperCase() }}: {{ endpointFor(config.transport, activeBase) }}</code>
            <el-button size="small" @click="copy(endpointFor(config.transport, activeBase))">{{ tt("common.copy") }}</el-button>
          </div>
        </div>
        <div class="mcp-config-section">
          <h4>{{ tt("mcp.testCurl") }}</h4>
          <div class="mcp-copy-row">
            <pre class="mcp-code">{{ curlExample }}</pre>
            <el-button size="small" @click="copy(curlExample)">{{ tt("common.copy") }}</el-button>
          </div>
        </div>
      </template>
      <template #footer>
        <el-button type="primary" @click="config.visible = false">{{ tt("common.done") }}</el-button>
      </template>
    </el-dialog>

    <!-- Usage log -->
    <el-dialog v-model="usage.visible" :title="tt('mcp.usageTitle', { name: usage.name || '', label: usage.label || '' })" width="560px" append-to-body>
      <div v-if="usage.loading" class="empty-state">{{ tt("mcp.loadingTokens") }}</div>
      <p v-else-if="usage.error" class="hint">{{ usage.error }}</p>
      <p v-else-if="!usage.rows.length" class="hint">{{ tt("mcp.usageEmpty") }}</p>
      <ul v-else class="mcp-log-list">
        <li v-for="(r, i) in usage.rows" :key="i">
          <div class="mcp-log-head">
            <code class="mcp-log-tool">{{ r.tool }}</code>
            <span class="secondary-line">{{ formatDate(r.createdAt) }}</span>
          </div>
          <code class="mcp-log-args">{{ r.argsPreview || "—" }}</code>
        </li>
      </ul>
      <template #footer>
        <el-button type="primary" @click="usage.visible = false">{{ tt("common.done") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api, copyText } from "@/api";
import { store, canMutate, isAdmin, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import ActionSheet from "@/components/ActionSheet.vue";
import { registerRouteRefresh, unregisterRouteRefresh } from "@/refresh";
import { formatDate } from "@/lib/common.js";

// Presentation-only mirror of the server tool registry (internal/mcp/tools.go).
const TOOL_GROUPS_ZH = [
  {
    title: "文件与文本",
    tools: [
      { name: "read_file", desc: "读取文本文件（最大 10 MiB）。" },
      { name: "write_file", desc: "创建或覆盖文本文件；自动创建父目录。" },
      { name: "edit_file", desc: "对已有文件进行精确的定向编辑（无需整体重写）。" },
      { name: "upload_file", desc: "从 base64 内容写入二进制文件（图片/压缩包）。" },
      { name: "download_file", desc: "将二进制文件读取为 base64 内容。" },
      { name: "list_files", desc: "浏览目录条目。" },
      { name: "copy_file", desc: "将文件或目录树复制到其他路径。" },
    ],
  },
  {
    title: "搜索",
    tools: [
      { name: "glob", desc: "按名称模式查找文件/目录。无需 find/shell。" },
      { name: "search_files", desc: "跨文件搜索文本，返回文件+行结果。无需 grep/shell。" },
    ],
  },
  {
    title: "容器",
    tools: [
      { name: "exec_command", desc: "运行 shell 命令；返回 stdout、stderr 与退出码。" },
      { name: "get_logs", desc: "获取最近的 stdout 和 stderr 日志。" },
      { name: "get_info", desc: "查看状态、镜像、IP、端口、环境变量、标签、挂载、GPU。" },
      { name: "start / stop / restart", desc: "控制容器生命周期。" },
    ],
  },
];
const TOOL_GROUPS_EN = [
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
    title: "Containers",
    tools: [
      { name: "exec_command", desc: "Run a shell command; returns stdout, stderr, and exit code." },
      { name: "get_logs", desc: "Fetch recent stdout and stderr logs." },
      { name: "get_info", desc: "Inspect state, image, IP, ports, env, labels, mounts, GPU." },
      { name: "start / stop / restart", desc: "Control the container lifecycle." },
    ],
  },
];

export default {
  name: "Mcp",
  components: { ActionSheet },
  data() {
    return {
      s: store,
      form: { containerId: "", label: "", expiresInHours: 0 },
      creating: false,
      sheet: { visible: false, row: null },
      config: {
        visible: false, oldToken: false, placeholder: false,
        token: "", externalKey: "", name: "", label: "",
        onSafeNetwork: false, remoteBase: "", scope: "local", transport: "sse",
      },
      usage: { visible: false, loading: false, error: "", rows: [], name: "", label: "" },
    };
  },
  computed: {
    containers() {
      return (store.containers || []).filter((c) => c && c.id);
    },
    sheetSubtitle() {
      const r = this.sheet.row;
      if (!r) return "";
      return (r.label ? r.label + " · " : "") + (r.expiresAt ? this.formatDate(r.expiresAt) : tt("mcp.never"));
    },
    sheetItems() {
      const r = this.sheet.row;
      if (!r) return [];
      const items = [
        { key: "cfg", label: tt("mcp.viewConfig"), icon: "Document" },
        { key: "log", label: tt("mcp.usageLog"), icon: "DataLine" },
      ];
      if (this.externalUrlFor(r)) items.push({ key: "www", label: tt("mcp.copyExternal"), icon: "Link" });
      if (canMutate() && this.remotePublished) items.push({ key: "gen", label: tt("mcp.generateExternalKey"), icon: "Key" });
      if (canMutate()) items.push({ key: "del", label: tt("mcp.deleteToken"), icon: "Delete", danger: true });
      return items;
    },
    remote() {
      return store.mcpRemote;
    },
    remotePublished() {
      return !!(store.mcpRemote && store.mcpRemote.enabled);
    },
    toolGroups() {
      return tt("mcp.flowSelect").includes("选择") ? TOOL_GROUPS_ZH : TOOL_GROUPS_EN;
    },
    domainPublished() {
      return !!(store.mcpRemote && store.mcpRemote.enabled && store.mcpRemote.baseUrl);
    },
    activeBase() {
      return this.config.scope === "remote" && this.config.remoteBase ? this.config.remoteBase : window.location.origin;
    },
    labelSlug() {
      const label = this.config.label;
      return (label ? label.replace(/[^a-zA-Z0-9_-]/g, "-") : "") || "mudp-container";
    },
    configText() {
      const entry = {
        type: this.config.transport === "http" ? "http" : "sse",
        url: this.endpointFor(this.config.transport, this.activeBase),
      };
      if (this.config.scope === "remote" && this.config.externalKey) {
        entry.headers = { Authorization: `Bearer ${this.config.externalKey}` };
      }
      return JSON.stringify({ mcpServers: { [this.labelSlug]: entry } }, null, 2);
    },
    authHeaderJson() {
      return JSON.stringify({ Authorization: `Bearer ${this.config.externalKey}` }, null, 2);
    },
    curlExample() {
      const base = this.activeBase;
      const token = this.config.token;
      const auth = this.config.scope === "remote" && this.config.externalKey
        ? `  -H "Authorization: Bearer ${this.config.externalKey}" \\\n` : "";
      if (this.config.transport === "http") {
        return `# Streamable HTTP: one POST per JSON-RPC request.\n` +
          `curl -X POST "${base}/mcp/${token}" \\\n` +
          `  -H "Content-Type: application/json" \\\n` +
          auth +
          `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`;
      }
      const authLine = this.config.scope === "remote" && this.config.externalKey
        ? ` \\\n  -H "Authorization: Bearer ${this.config.externalKey}"` : "";
      return `# 1. Open the SSE stream to get the message endpoint:\n` +
        `curl -N ${base}/mcp/${token}/sse${authLine}\n\n` +
        `# 2. POST a JSON-RPC request to the endpoint URL printed above, e.g.:\n` +
        `curl -X POST "${base}/mcp/${token}/messages?session=SESSION_ID" \\\n` +
        `  -H "Content-Type: application/json" \\\n` +
        auth +
        `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`;
    },
    transportHint() {
      return this.config.transport === "http" ? tt("mcp.transportHintHttp") : tt("mcp.transportHintSse");
    },
    scopeHint() {
      return this.config.scope === "remote"
        ? tt("mcp.scopeRemoteHint", { domain: store.mcpRemote?.domain || "", safe: store.mcpRemote?.safeNetwork || "safe" })
        : tt("mcp.scopeLocalHint");
    },
  },
  async mounted() {
    registerRouteRefresh("mcp", () => this.refreshTokens());
    await this.refreshTokens();
  },
  beforeUnmount() {
    unregisterRouteRefresh("mcp");
  },
  methods: {
    tt,
    canMutate,
    isAdmin,
    displayNameForUsername,
    onRowClick(row) {
      if (!store.isMobile) return;
      this.sheet = { visible: true, row };
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      if (!row) return;
      if (item.key === "cfg") this.showConfig(row);
      else if (item.key === "log") this.showUsage(row);
      else if (item.key === "www") this.copy(this.externalUrlFor(row));
      else if (item.key === "gen") this.generateKey(row);
      else if (item.key === "del") this.deleteToken(row);
    },
    formatDate,
    expired(tk) {
      return tk.expiresAt && new Date(tk.expiresAt) < new Date();
    },
    async copy(text) {
      try {
        await copyText(text);
        ElMessage.success(tt("common.copied"));
      } catch {
        ElMessage.error(tt("common.copyFailed"));
      }
    },
    async refreshTokens() {
      try {
        store.mcpTokens = (await api("/api/mcp/tokens")) || [];
      } catch {
        store.mcpTokens = [];
      }
      // The public hostname an admin bound to the MCP-only listener, if any.
      try {
        store.mcpRemote = (await api("/api/mcp/remote")) || null;
      } catch {
        store.mcpRemote = null;
      }
    },
    // The external URL is gated on onSafeNetwork — only offered when the
    // safe-network rule would actually let the remote listener through.
    externalUrlFor(tk) {
      const remote = store.mcpRemote;
      if (!remote || !remote.enabled || !remote.baseUrl || !tk.token) return "";
      if (!tk.onSafeNetwork) return "";
      return `${remote.baseUrl}/mcp/${tk.token}/sse`;
    },
    async createToken() {
      if (!this.form.containerId) {
        ElMessage.warning(tt("mcp.selectContainerFirst"));
        return;
      }
      this.creating = true;
      try {
        const res = await api("/api/mcp/tokens", {
          method: "POST",
          body: JSON.stringify({
            containerId: this.form.containerId,
            label: this.form.label || "",
            expiresInHours: Number(this.form.expiresInHours) || 0,
          }),
        });
        ElMessage.success(tt("mcp.tokenCreated"));
        await this.refreshTokens();
        this.form = { containerId: "", label: "", expiresInHours: 0 };
        // Look the new token up so its onSafeNetwork flag carries into the
        // config dialog — a fresh token has no external link unless its
        // container is on the safe network.
        const created = (store.mcpTokens || []).find((tk) => tk.id === res.id);
        this.openConfigRaw(res.token, "", res.label || "", false, created ? created.onSafeNetwork : false, "local");
      } catch (err) {
        ElMessage.error(err.message || tt("mcp.createFail"));
      } finally {
        this.creating = false;
      }
    },
    // Older tokens (created when only the hash was stored) have no recoverable
    // cleartext — tell the user to recreate it.
    showConfig(tk) {
      if (!tk.token) {
        this.config = { ...this.config, visible: true, oldToken: true, name: tk.containerName };
        return;
      }
      this.openConfigRaw(tk.token, tk.externalKey || "", tk.label || "", false, !!tk.onSafeNetwork, "local");
    },
    openConfigRaw(token, externalKey, label, placeholder, onSafeNetwork, initialScope) {
      const remote = store.mcpRemote;
      const remoteBase = remote && remote.enabled && onSafeNetwork ? remote.baseUrl || "" : "";
      this.config = {
        visible: true,
        oldToken: false,
        placeholder,
        token,
        externalKey,
        name: "",
        label,
        onSafeNetwork,
        remoteBase,
        scope: remoteBase && initialScope === "remote" ? "remote" : "local",
        transport: "sse",
      };
    },
    async generateKey(tk) {
      try {
        await ElMessageBox.confirm(tt("mcp.generateExternalKeyConfirm", { name: tk.containerName }), tt("mcp.generateExternalKey"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        });
      } catch { return; }
      try {
        const res = await api(`/api/mcp/tokens/${tk.id}/rotate-external`, { method: "POST" });
        ElMessage.success(tt("mcp.externalKeyGenerated"));
        await this.refreshTokens();
        const updated = (store.mcpTokens || []).find((x) => x.id === res.id);
        // Opens straight to the External scope the new credential matters for.
        this.openConfigRaw(updated ? updated.token : "", res.externalKey, res.label || tk.label || "", false, updated ? updated.onSafeNetwork : !!tk.onSafeNetwork, "remote");
      } catch (err) {
        ElMessage.error(err.message || tt("mcp.generateExternalKeyFail"));
      }
    },
    async deleteToken(tk) {
      try {
        await ElMessageBox.confirm(tt("mcp.deleteConfirm", { name: tk.containerName }), tt("mcp.deleteToken"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api(`/api/mcp/tokens/${tk.id}`, { method: "DELETE" });
        ElMessage.success(tt("mcp.tokenDeleted"));
        await this.refreshTokens();
      } catch (err) {
        ElMessage.error(err.message || tt("mcp.tokenDeletedFail"));
      }
    },
    async showUsage(tk) {
      this.usage = { visible: true, loading: true, error: "", rows: [], name: tk.containerName, label: tk.label };
      try {
        const rows = (await api(`/api/mcp/tokens/${tk.id}/usage?limit=200`)) || [];
        this.usage.rows = rows;
      } catch (err) {
        this.usage.error = err.message || tt("mcp.usageFail");
      } finally {
        this.usage.loading = false;
      }
    },
    endpointFor(transport, base) {
      const token = this.config.token;
      return transport === "http" ? `${base}/mcp/${token}` : `${base}/mcp/${token}/sse`;
    },
  },
};
</script>

<style scoped>
.mcp-page > * + * { margin-top: 16px; }
.mcp-hero { display: flex; gap: 20px; align-items: center; justify-content: space-between; flex-wrap: wrap; background: linear-gradient(135deg, #0b1220, #1e3a8a); color: #e2e8f0; border: none; }
.mcp-hero h2 { margin: 6px 0; }
.mcp-eyebrow { font-size: 11px; letter-spacing: 2px; text-transform: uppercase; color: #93c5fd; }
.mcp-flow { display: flex; align-items: center; gap: 8px; font-size: 12.5px; flex-wrap: wrap; }
.mcp-flow strong { width: 22px; height: 22px; border-radius: 50%; background: rgba(147, 197, 253, 0.2); display: inline-flex; align-items: center; justify-content: center; font-size: 11px; }
.mcp-main-grid { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: 16px; align-items: start; }
.mcp-card-note { color: var(--muted); font-size: 12px; }
.mcp-tool-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 14px; }
.mcp-tool-group-title { margin: 0 0 8px; font-size: 12.5px; color: var(--muted); }
.mcp-tool { margin-bottom: 6px; }
.mcp-tool-name { background: #eff6ff; color: var(--brand); border-radius: 4px; padding: 1px 6px; font-size: 11.5px; }
.mcp-tool-desc { display: block; color: #475569; font-size: 12px; margin-top: 2px; }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.mcp-copy-row { display: flex; align-items: flex-start; gap: 10px; margin-bottom: 10px; }
.mcp-code { background: #0f172a; color: #cbd5e1; border-radius: 8px; padding: 10px; font-size: 12px; flex: 1; overflow: auto; white-space: pre-wrap; word-break: break-all; margin: 0; }
.mcp-code-inline { padding: 6px 10px; }
.mcp-config-section { margin-bottom: 14px; }
.mcp-config-section h4 { margin: 0 0 6px; font-size: 13px; }
.mcp-transport-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.mcp-config-actions { margin-top: 8px; display: flex; justify-content: flex-end; }
.mcp-live-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: #22c55e; margin-right: 6px; animation: mcp-pulse 1.6s infinite; }
@keyframes mcp-pulse { 0%, 100% { box-shadow: 0 0 0 0 rgba(34, 197, 94, 0.5); } 50% { box-shadow: 0 0 0 5px rgba(34, 197, 94, 0); } }
.mcp-log-list { list-style: none; margin: 0; padding: 0; max-height: 50vh; overflow-y: auto; }
.mcp-log-list li { border-top: 1px solid var(--line); padding: 8px 0; }
.mcp-log-head { display: flex; justify-content: space-between; gap: 10px; }
.mcp-log-tool { background: #eff6ff; color: var(--brand); border-radius: 4px; padding: 1px 6px; font-size: 11.5px; }
.mcp-log-args { display: block; color: var(--muted); font-size: 11.5px; margin-top: 4px; word-break: break-all; }
.primary-line { font-weight: 600; display: flex; align-items: center; }
.secondary-line { color: var(--muted); font-size: 12px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
@media (max-width: 1000px) { .mcp-main-grid { grid-template-columns: minmax(0, 1fr); } }
</style>
