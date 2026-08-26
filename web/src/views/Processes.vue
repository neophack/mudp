<template>
  <div class="stack">
    <div class="card">
      <div class="card-head">
        <h2>{{ tt("processes.watchedTitle") }}</h2>
        <span class="card-head-sub hint">{{ tt("processes.watchedSub") }}</span>
      </div>
      <el-table :data="watches" size="small" :empty-text="tt('processes.noWatches')">
        <el-table-column :label="tt('processes.colName')" min-width="140">
          <template #default="{ row }">
            <div class="primary-line">{{ procName(row.command) || tt("processes.unknownName") }}</div>
            <div class="secondary-line mono cmd-line">{{ row.command || "" }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('processes.colContainer')" min-width="110">
          <template #default="{ row }">
            <div>{{ row.containerName || row.containerId.slice(0, 12) }}</div>
            <div class="secondary-line mono">{{ tt("processes.colPid") }} {{ row.pid }}</div>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('processes.colSince')" width="170">
          <template #default="{ row }"><span class="mono">{{ row.createdAt || "" }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('common.actions')" width="100">
          <template #default="{ row }">
            <el-button link size="small" @click="unwatch(row)">{{ tt("processes.unwatch") }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Feishu bot delivery history; unavailable until an admin configures SSO. -->
    <div class="card" :class="{ 'card-disabled': !feishuEnabled }">
      <div class="card-head">
        <h2>{{ tt("processes.historyTitle") }}</h2>
        <el-tag v-if="!feishuEnabled" size="small" type="info">{{ tt("processes.historyUnavailableTag") }}</el-tag>
        <span v-else class="card-head-sub hint">{{ tt("processes.historySub") }}</span>
        <el-button
          v-if="feishuEnabled && history.length"
          link
          size="small"
          class="danger-text clear-btn"
          @click="clearHistory"
        >{{ tt("processes.clearHistory") }}</el-button>
      </div>
      <div v-if="!feishuEnabled" class="unavailable">
        <p>{{ tt("processes.historyUnavailable") }}</p>
      </div>
      <el-table v-else :data="history" size="small" :empty-text="tt('processes.noHistory')">
        <el-table-column :label="tt('processes.colTime')" :width="s.isMobile ? 120 : 170">
          <template #default="{ row }"><span class="mono">{{ fmtTime(row.createdAt) }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('processes.colType')" width="120">
          <template #default="{ row }">{{ kindLabel(row.kind) }}</template>
        </el-table-column>
        <el-table-column :label="tt('processes.colContent')" min-width="200">
          <template #default="{ row }">
            <div class="cmd-line">{{ row.message }}</div>
            <div v-if="s.isMobile" class="secondary-line">{{ kindLabel(row.kind) }}</div>
            <div v-if="row.status === 'failed' && row.error" class="error-line">{{ row.error }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('processes.colStatus')" width="100">
          <template #default="{ row }">
            <el-tag v-if="row.status === 'sent'" size="small" type="success">{{ tt("processes.statusSent") }}</el-tag>
            <el-tag v-else size="small" type="danger">{{ tt("processes.statusFailed") }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div class="card">
      <div class="card-head">
        <h2>{{ tt("processes.title") }}</h2>
        <span class="card-head-sub hint">{{ tt("processes.sub") }}</span>
      </div>
      <el-input
        v-model="filter"
        class="filter-input"
        clearable
        size="small"
        :placeholder="tt('processes.filterPlaceholder')"
        prefix-icon="Search"
      />
      <el-table
        :data="filteredProcesses"
        size="small"
        :empty-text="filter ? tt('processes.noMatch') : tt('processes.noProcesses')"
        :default-sort="{ prop: 'cpu', order: 'descending' }"
      >
        <el-table-column v-if="admin && !s.isMobile" :label="tt('common.user')" width="120">
          <template #default="{ row }">{{ displayNameForUsername(row.user || "") }}</template>
        </el-table-column>
        <el-table-column prop="name" :label="tt('processes.colName')" min-width="140" sortable :sort-method="byName">
          <template #default="{ row }">
            <div class="primary-line">{{ procName(row.command) || tt("processes.unknownName") }}</div>
            <div v-if="row.container" class="secondary-line">{{ row.container }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('processes.colPid')" width="80" sortable :sort-method="(a, b) => Number(a.pid) - Number(b.pid)">
          <template #default="{ row }"><span class="mono">{{ row.pid }}</span></template>
        </el-table-column>
        <el-table-column prop="cpu" :label="tt('processes.colCpu')" width="110" sortable :sort-method="(a, b) => a.cpuPct - b.cpuPct">
          <template #default="{ row }">{{ (row.cpuPct || 0).toFixed(1) }}%</template>
        </el-table-column>
        <el-table-column prop="mem" :label="tt('processes.colMem')" width="120" sortable :sort-method="(a, b) => a.memMb - b.memMb">
          <template #default="{ row }">{{ fmtMem(row.memMb) }}</template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('processes.colCommand')" min-width="240">
          <template #default="{ row }"><span class="secondary-line mono cmd-line">{{ row.command || "" }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('common.actions')" width="100" :fixed="s.isMobile ? false : 'right'">
          <template #default="{ row }">
            <el-tag v-if="isWatched(row)" size="small" type="success">
              <span class="tag-dot"></span>{{ tt("processes.watching") }}
            </el-tag>
            <el-button v-else link size="small" @click="watch(row)">{{ tt("processes.watch") }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, isAdmin, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";

const POLL_MS = 5000;

export default {
  name: "Processes",
  data() {
    return { s: store, processes: [], watches: [], history: [], filter: "", feishuEnabled: false, failed: false };
  },
  computed: {
    admin() { return isAdmin(); },
    filteredProcesses() {
      const q = this.filter.trim().toLowerCase();
      if (!q) return this.processes;
      return this.processes.filter((p) =>
        `${p.command || ""} ${p.container || ""} ${p.pid || ""} ${this.procName(p.command)}`
          .toLowerCase()
          .includes(q)
      );
    },
  },
  async mounted() {
    // The Feishu send history only exists once an admin configures SSO; the
    // public config endpoint says whether that is the case.
    api("/api/feishu/config")
      .then((cfg) => { this.feishuEnabled = !!cfg.enabled; })
      .catch(() => { this.feishuEnabled = false; });
    await this.refresh();
    this.timer = setInterval(this.refresh, POLL_MS);
  },
  beforeUnmount() {
    clearInterval(this.timer);
  },
  methods: {
    tt,
    displayNameForUsername,
    async refresh() {
      if (document.hidden) return;
      const data = await api("/api/processes").catch(() => null);
      if (!data) {
        this.failed = true;
        return;
      }
      this.failed = false;
      this.processes = data.processes || [];
      this.watches = data.watches || [];
      if (this.feishuEnabled) {
        this.history = await api("/api/me/feishu_messages").catch(() => this.history);
      }
    },
    // The process name is the basename of the command's first token, e.g.
    // "python /app/train.py --x" → "python".
    procName(cmd) {
      if (!cmd) return "";
      const first = cmd.trim().split(/\s+/)[0];
      const base = first.split("/").pop();
      return base || first;
    },
    byName(a, b) {
      return this.procName(a.command).localeCompare(this.procName(b.command));
    },
    kindLabel(kind) {
      return kind === "admin_test" ? tt("processes.kindAdminTest") : tt("processes.kindProcessWatch");
    },
    async clearHistory() {
      try {
        await ElMessageBox.confirm(tt("processes.clearHistoryConfirm"), tt("processes.historyTitle"), {
          type: "warning",
          confirmButtonText: tt("processes.clearHistory"),
          cancelButtonText: tt("common.cancel"),
        });
      } catch { /* cancelled */ return; }
      try {
        await api("/api/me/feishu_messages/clear", { method: "POST" });
        this.history = [];
        ElMessage.success(tt("processes.historyCleared"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    fmtTime(iso) {
      const d = new Date(iso);
      return isNaN(d) ? iso || "" : d.toLocaleString();
    },
    isWatched(p) {
      return this.watches.some((w) => `${w.containerId}:${w.pid}` === `${p.containerId}:${p.pid}`);
    },
    async watch(p) {
      try {
        await api("/api/containers/processes/watch", {
          method: "POST",
          body: JSON.stringify({ containerId: p.containerId, pid: String(p.pid) }),
        });
        ElMessage.success(tt("processes.watched"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async unwatch(w) {
      try {
        await api("/api/containers/processes/unwatch", {
          method: "POST",
          body: JSON.stringify({ id: Number(w.id) }),
        });
        ElMessage.success(tt("processes.unwatched"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    fmtMem(mb) {
      if (!mb || mb <= 0) return "0 MB";
      if (mb < 1024) return `${Math.round(mb)} MB`;
      return `${(mb / 1024).toFixed(1)} GB`;
    },
  },
};
</script>

<style scoped>
.stack > * + * { margin-top: 16px; }
.card-head { display: flex; align-items: baseline; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.card-head h2 { margin: 0; font-size: 14px; }
/* Keep secondary text readable: noticeably dimmer than primary ink, but well
   clear of the muted placeholder tone used elsewhere. */
.secondary-line { color: color-mix(in srgb, var(--ink) 65%, var(--muted)); font-size: 12px; }
.primary-line { font-weight: 600; }
.error-line { color: var(--danger-text); font-size: 12px; margin-top: 2px; word-break: break-all; }
.cmd-line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.tag-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
.filter-input { margin-bottom: 10px; max-width: 320px; }
.clear-btn { margin-left: auto; }
.card-disabled { opacity: 0.75; }
.unavailable { padding: 6px 0 10px; }
.unavailable p { margin: 0; color: var(--muted); font-size: 12.5px; }
</style>
