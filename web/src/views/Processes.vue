<template>
  <div class="stack">
    <div class="card">
      <div class="card-head">
        <h2>{{ tt("processes.watchedTitle") }}</h2>
        <span class="card-head-sub hint">{{ tt("processes.watchedSub") }}</span>
      </div>
      <el-table :data="watches" size="small" :empty-text="tt('processes.noWatches')">
        <el-table-column :label="tt('processes.colContainer')" min-width="110">
          <template #default="{ row }">
            <div>{{ row.containerName || row.containerId.slice(0, 12) }}</div>
            <div v-if="s.isMobile" class="secondary-line mono cmd-line">{{ row.command || "" }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('processes.colPid')" width="70">
          <template #default="{ row }"><span class="mono">{{ row.pid }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('processes.colCommand')" min-width="200">
          <template #default="{ row }"><span class="secondary-line mono">{{ row.command || "" }}</span></template>
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
    <div class="card">
      <div class="card-head">
        <h2>{{ tt("processes.title") }}</h2>
        <span class="card-head-sub hint">{{ tt("processes.sub") }}</span>
      </div>
      <el-table :data="processes" size="small" :empty-text="tt('processes.noProcesses')">
        <el-table-column v-if="admin && !s.isMobile" :label="tt('common.user')" width="120">
          <template #default="{ row }">{{ displayNameForUsername(row.user || "") }}</template>
        </el-table-column>
        <el-table-column :label="tt('processes.colContainer')" min-width="110">
          <template #default="{ row }">
            <div>{{ row.container || "" }}</div>
            <div v-if="s.isMobile" class="secondary-line mono cmd-line">{{ row.command || "" }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('processes.colPid')" width="70">
          <template #default="{ row }"><span class="mono">{{ row.pid }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('processes.colCpu')" width="90">
          <template #default="{ row }">{{ (row.cpuPct || 0).toFixed(1) }}%</template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('processes.colMem')" width="100">
          <template #default="{ row }">{{ fmtMem(row.memMb) }}</template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('processes.colCommand')" min-width="240">
          <template #default="{ row }"><span class="secondary-line mono">{{ row.command || "" }}</span></template>
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
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { store, isAdmin, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";

const POLL_MS = 5000;

export default {
  name: "Processes",
  data() {
    return { s: store, processes: [], watches: [], failed: false };
  },
  computed: {
    admin() { return isAdmin(); },
  },
  async mounted() {
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
        await api("/api/containers/processes/unwatch", { method: "POST", body: JSON.stringify({ id: Number(w.id) }) });
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
.secondary-line { color: var(--muted); font-size: 12px; }
.cmd-line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.tag-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
</style>
