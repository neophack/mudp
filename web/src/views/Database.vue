<template>
  <div class="stack">
    <div v-if="error" class="card"><div class="error-box">✗ {{ error }}</div></div>
    <div v-if="!data && !error" class="card"><div class="empty-state">{{ tt("common.loadingDots") }}</div></div>
    <template v-if="data">
      <div class="db-stat-row">
        <div class="card stat">
          <span class="stat-label">{{ tt("database.dbFile") }}</span>
          <span class="stat-value mono">{{ fmtBytes(report.fileBytes) }}</span>
        </div>
        <div class="card stat">
          <span class="stat-label">{{ tt("database.walFile") }}</span>
          <span class="stat-value mono">{{ fmtBytes(report.walBytes) }}</span>
        </div>
        <div class="card stat">
          <span class="stat-label">{{ tt("database.freePages") }}</span>
          <span class="stat-value">{{ report.freePages ?? 0 }}</span>
        </div>
      </div>
      <div class="card">
        <div class="card-head">
          <h2>{{ tt("database.tables") }}</h2>
          <el-button size="small" @click="refresh">{{ tt("action.refresh") }}</el-button>
        </div>
        <p class="hint">{{ report.byteSizes ? tt("database.tableHint") : tt("database.rowOnlyHint") }}</p>
        <el-table :data="tables" size="small" :empty-text="tt('database.noTables')">
          <el-table-column :label="tt('database.colTable')" min-width="170">
            <template #default="{ row }"><span class="primary-line mono">{{ row.name }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('database.colPurpose')" min-width="200">
            <template #default="{ row }"><span class="secondary-line">{{ row.description || "—" }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('database.colSize')" min-width="150">
            <template #default="{ row }">
              <div class="secondary-line mono">{{ row.bytes > 0 ? fmtBytes(row.bytes) : "—" }}</div>
              <el-progress v-if="row.bytes > 0" :percentage="pctOf(row)" :stroke-width="6" :show-text="false" />
            </template>
          </el-table-column>
          <el-table-column :label="tt('database.colRows')" width="100">
            <template #default="{ row }"><span class="secondary-line">{{ row.rows ?? 0 }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('common.actions')" width="110" fixed="right">
            <template #default="{ row }">
              <!-- Only log tables in the allow-list can be cleared; user tables
                 appear for visibility but the backend rejects them regardless. -->
              <el-button v-if="prunable.has(row.name)" link size="small" @click="openPrune(row)">{{ tt("database.clean") }}</el-button>
              <span v-else class="hint">{{ tt("database.protected") }}</span>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <el-dialog v-model="prune.visible" :title="tt('database.cleanTitle', { table: prune.table })" width="420px" append-to-body>
        <p class="hint">{{ tt("database.cleanHint", { table: prune.table }) }}</p>
        <el-select v-model="prune.days" style="width: 100%">
          <el-option :value="7" :label="tt('database.olderThan', { n: 7 })" />
          <el-option :value="30" :label="tt('database.olderThan', { n: 30 })" />
          <el-option :value="90" :label="tt('database.olderThan', { n: 90 })" />
          <el-option :value="0" :label="tt('database.allRows')" />
        </el-select>
        <template #footer>
          <el-button @click="prune.visible = false">{{ tt("common.cancel") }}</el-button>
          <el-button type="danger" @click="confirmPrune">{{ tt("database.cleanNow") }}</el-button>
        </template>
      </el-dialog>
    </template>
  </div>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { tt } from "@/i18n";
import { fmtBytes } from "@/lib/common.js";

export default {
  name: "Database",
  data() {
    return {
      data: null,
      error: "",
      prune: { visible: false, table: "", days: 30 },
    };
  },
  computed: {
    report() { return this.data?.report || {}; },
    tables() { return this.report.tables || []; },
    prunable() { return new Set(this.data?.prunable || []); },
    maxBytes() { return Math.max(1, ...this.tables.map((tb) => tb.bytes || 0)); },
  },
  mounted() {
    this.refresh();
  },
  methods: {
    tt,
    fmtBytes,
    // Fetch fresh on every entry: this page is about the current footprint.
    async refresh() {
      try {
        this.data = await api("/api/admin/db/usage");
      } catch (err) {
        this.error = err.message;
      }
    },
    pctOf(tb) {
      return this.maxBytes > 0 ? Math.round(((tb.bytes || 0) / this.maxBytes) * 100) : 0;
    },
    openPrune(tb) {
      this.prune = { visible: true, table: tb.name, days: 30 };
    },
    async confirmPrune() {
      try {
        const res = await api("/api/admin/db/prune", {
          method: "POST",
          body: JSON.stringify({ tables: [this.prune.table], olderThanDays: Number(this.prune.days) }),
        });
        const removed = res.deleted?.[this.prune.table] ?? 0;
        ElMessage.success(tt("database.cleaned", { n: removed }));
        this.prune.visible = false;
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
.db-stat-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; }
.stat { display: flex; flex-direction: column; gap: 4px; margin-bottom: 0; }
.stat-label { color: var(--muted); font-size: 12.5px; }
.stat-value { font-size: 20px; font-weight: 750; }
.card-head { display: flex; align-items: center; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
</style>
