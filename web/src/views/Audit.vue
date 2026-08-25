<template>
  <div class="card">
    <div class="card-head">
      <h2>{{ tt("audit.title") }}</h2>
      <div class="filters">
        <el-input v-model="actor" :placeholder="tt('audit.filterActor')" clearable size="small" style="width: 160px" @input="reload" />
        <el-input v-model="action" :placeholder="tt('audit.filterAction')" clearable size="small" style="width: 160px" @input="reload" />
        <el-button size="small" @click="exportCsv">{{ tt("audit.exportCsv") }}</el-button>
      </div>
    </div>
    <el-table :data="s.audit" size="small" :empty-text="tt('audit.noActivity')">
      <el-table-column :label="tt('audit.colTime')" :width="s.isMobile ? 110 : 170">
        <template #default="{ row }"><span class="secondary-line">{{ fmtTime(row.createdAt) }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('audit.colActor')" width="150">
        <template #default="{ row }"><span class="primary-line">{{ displayNameForUsername(row.actor) }}</span></template>
      </el-table-column>
      <el-table-column :label="tt('audit.colAction')" :width="s.isMobile ? 130 : 180">
        <template #default="{ row }">
          <el-tag size="small">{{ row.action }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="tt('audit.colTarget')">
        <template #default="{ row }"><span class="secondary-line mono">{{ row.target }}</span></template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { store, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "Audit",
  data() {
    return { s: store, actor: "", action: "" };
  },
  methods: {
    tt,
    displayNameForUsername,
    async reload() {
      store.auditSearch = { actor: this.actor, action: this.action };
      const params = new URLSearchParams();
      if (this.actor) params.set("actor", this.actor);
      if (this.action) params.set("action", this.action);
      try {
        store.audit = await api("/api/admin/audit?" + params.toString());
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    exportCsv() {
      const rows = store.audit || [];
      const lines = ["time,actor,action,target"];
      for (const e of rows) {
        lines.push([e.createdAt, e.actor, e.action, e.target].map(this.csvCell).join(","));
      }
      const blob = new Blob([lines.join("\n")], { type: "text/csv" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `mudp-audit-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();
      URL.revokeObjectURL(url);
    },
    csvCell(v) {
      const s = String(v ?? "");
      return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
    },
    fmtTime(iso) {
      if (!iso) return "—";
      const t = new Date(iso);
      return isNaN(t) ? iso : t.toLocaleString();
    },
  },
};
</script>

<style scoped>
.card-head { display: flex; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 10px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.filters { display: flex; gap: 8px; flex-wrap: wrap; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
</style>
