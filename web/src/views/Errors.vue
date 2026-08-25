<template>
  <div class="stack">
    <div v-if="loadFailed" class="card"><div class="error-box">✗ {{ tt("errors.loadFailed") }} {{ loadFailed }}</div></div>
    <div class="dash-tiles">
      <section class="card stat-tile">
        <div class="stat-icon">🧯</div>
        <div class="stat-body"><div class="stat-value">{{ stats.events ?? 0 }}</div><div class="stat-label">{{ tt("errors.statIssues") }}</div></div>
      </section>
      <section class="card stat-tile">
        <div class="stat-icon">💥</div>
        <div class="stat-body"><div class="stat-value">{{ stats.panics ?? 0 }}</div><div class="stat-label">{{ tt("errors.statPanics") }}</div></div>
      </section>
      <section class="card stat-tile">
        <div class="stat-icon">🔁</div>
        <div class="stat-body"><div class="stat-value">{{ stats.occurrences ?? 0 }}</div><div class="stat-label">{{ tt("errors.statOccurrences") }}</div></div>
      </section>
    </div>
    <div class="card">
      <div class="card-head">
        <h2>{{ tt("errors.title") }}</h2>
        <div class="head-actions">
          <el-button size="small">
            <a href="/api/admin/errors/export" download style="color: inherit; text-decoration: none">{{ tt("errors.export") }}</a>
          </el-button>
          <el-button size="small" type="danger" plain @click="clearAll">{{ tt("errors.clearAll") }}</el-button>
        </div>
      </div>
      <el-table :data="events" size="small" :empty-text="tt('errors.noErrors')">
        <el-table-column :label="tt('errors.colKind')" :width="s.isMobile ? 80 : 100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.kind === 'panic' ? 'danger' : 'warning'">{{ row.kind }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('errors.colWhere')" min-width="170">
          <template #default="{ row }"><span class="mono">{{ `${row.method || ""} ${row.path || ""}`.trim() || "—" }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('errors.colMessage')" min-width="150">
          <template #default="{ row }">
            <div class="primary-line mono">{{ truncate(row.message, 160) }}</div>
            <div v-if="s.isMobile" class="secondary-line mono">{{ `${row.method || ""} ${row.path || ""}`.trim() }}</div>
            <el-button v-if="row.stack" link size="small" @click="viewStack(row)">{{ tt("errors.viewStack") }}</el-button>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('errors.colCount')" width="90">
          <template #default="{ row }">{{ row.count || 1 }}</template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('errors.colSeen')" min-width="220">
          <template #default="{ row }">
            <div class="secondary-line">{{ tt("errors.first") }}: {{ row.firstSeen || "—" }} · {{ tt("errors.last") }}: {{ row.lastSeen || "—" }}</div>
          </template>
        </el-table-column>
        <el-table-column label="" :width="s.isMobile ? 84 : 100" :fixed="s.isMobile ? false : 'right'">
          <template #default="{ row }">
            <el-button link size="small" @click="resolve(row)">{{ tt("errors.resolve") }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="stack.visible" :title="tt('errors.viewStack')" width="720px" append-to-body>
      <pre class="mono stack-pre">{{ stack.text }}</pre>
    </el-dialog>
  </div>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { store } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "Errors",
  data() {
    return { s: store, stats: {}, events: [], loadFailed: "", stack: { visible: false, text: "" } };
  },
  async mounted() {
    await this.refresh();
  },
  methods: {
    tt,
    async refresh() {
      const data = await api("/api/admin/errors").catch((err) => {
        this.loadFailed = err.message;
        return null;
      });
      if (!data) return;
      this.loadFailed = "";
      this.stats = data.stats || {};
      this.events = data.events || [];
    },
    truncate(s, n) {
      s = s || "";
      return s.length > n ? s.slice(0, n) + "…" : s;
    },
    viewStack(e) {
      if (!e.stack) {
        ElMessage.info(tt("errors.noStack"));
        return;
      }
      this.stack = { visible: true, text: e.stack };
    },
    async resolve(e) {
      try {
        await api(`/api/admin/errors/${e.id}`, { method: "DELETE" });
        ElMessage.success(tt("errors.resolved"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async clearAll() {
      try {
        await api("/api/admin/errors/clear", { method: "POST" });
        ElMessage.success(tt("errors.cleared"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
.stack > * + * { margin-top: 16px; }
.dash-tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; }
.stat-tile { display: flex; gap: 14px; align-items: center; margin-bottom: 0; }
.stat-icon { font-size: 24px; }
.stat-value { font-size: 22px; font-weight: 750; }
.stat-label { color: var(--muted); font-size: 12.5px; }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.stack-pre { background: #0f172a; color: #cbd5e1; border-radius: 8px; padding: 12px; font-size: 11.5px; max-height: 50vh; overflow: auto; white-space: pre-wrap; }
</style>
