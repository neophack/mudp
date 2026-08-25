<template>
  <div class="disks-layout">
    <div v-if="loadFailed" class="card disks-error"><div class="error-box">✗ {{ loadFailed }}</div></div>
    <section class="stack tools-col">
      <div class="card">
        <div class="card-head"><h2>{{ tt("disks.mountDisk") }}</h2></div>
        <el-input v-model="mountForm.source" :placeholder="tt('disks.sourcePlaceholder')" class="mb" size="small" />
        <el-input v-model="mountForm.target" :placeholder="tt('disks.targetPlaceholder')" class="mb" size="small" />
        <el-input v-model="mountForm.fsType" :placeholder="tt('disks.fsTypePlaceholder')" class="mb" size="small" />
        <div class="page-actions">
          <el-button size="small" @click="saveMountConfig">{{ tt("disks.saveConfig") }}</el-button>
          <el-button size="small" type="primary" @click="mountNow">{{ tt("disks.mountNow") }}</el-button>
        </div>
        <p class="hint">{{ tt("disks.mountHint") }}</p>
      </div>
      <div class="card">
        <div class="card-head"><h2>{{ tt("disks.backup") }}</h2></div>
        <el-input v-model="backupForm.targetDir" :placeholder="tt('disks.backupDirPlaceholder')" class="mb" size="small" />
        <el-button size="small" type="primary" @click="backupDb">{{ tt("disks.backupDb") }}</el-button>
        <p class="hint">{{ tt("disks.backupHint") }}</p>
      </div>
    </section>

    <section class="stack schedule-col">
      <div class="card">
        <div class="card-head"><h2>{{ tt("disks.netdiskBackupSchedule") }}</h2></div>
        <p class="hint">{{ tt("disks.scheduleHint") }}</p>
        <label class="check"><input v-model="schedule.enabled" type="checkbox"> {{ tt("disks.enabled") }}</label>
        <div class="bk-sched-time">
          <label>{{ tt("disks.dailyAt") }}</label>
          <el-input v-model="schedule.hour" type="number" min="0" max="23" size="small" style="width: 80px" :title="tt('disks.hourTitle')" />
          <span>:</span>
          <el-input v-model="schedule.minute" type="number" min="0" max="59" size="small" style="width: 80px" :title="tt('disks.minuteTitle')" />
        </div>
        <el-button size="small" type="primary" @click="saveSchedule">{{ tt("disks.saveSchedule") }}</el-button>
        <p class="hint">{{ scheduleStatus }}</p>
        <el-button size="small" style="margin-top: 10px" @click="runNow">{{ tt("disks.backupAllNow") }}</el-button>
      </div>
    </section>

    <div class="card disks-table-card">
      <div class="card-head"><h2>{{ tt("disks.disks") }}</h2></div>
      <el-table :data="s.disks" size="small" :empty-text="tt('disks.noDiskData')">
        <el-table-column :label="tt('common.name')" width="130">
          <template #default="{ row }"><span class="primary-line">{{ row.name || "-" }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('users.colPath')" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.path }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('disks.colTotal')" width="100">
          <template #default="{ row }">{{ fmtBytes(row.totalBytes) }}</template>
        </el-table-column>
        <el-table-column :label="tt('disks.colFree')" width="100">
          <template #default="{ row }">{{ fmtBytes(row.freeBytes) }}</template>
        </el-table-column>
        <el-table-column :label="tt('disks.colUsed')" min-width="140">
          <template #default="{ row }">
            <el-progress :percentage="Math.max(0, Math.min(100, row.usedPct || 0))" :stroke-width="8" :show-text="false" />
          </template>
        </el-table-column>
        <el-table-column :label="tt('common.actions')" width="110" fixed="right">
          <template #default="{ row }">
            <el-button link size="small" @click="unmount(row)">{{ tt("disks.unmount") }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "Disks",
  data() {
    return {
      s: store,
      loadFailed: "",
      mountForm: { source: "", target: "", fsType: "" },
      backupForm: { targetDir: "" },
      schedule: { enabled: false, hour: 2, minute: 0, lastRunAt: "" },
    };
  },
  computed: {
    scheduleStatus() {
      const s = this.schedule;
      const pad = (n) => String(n).padStart(2, "0");
      const time = `${pad(s.hour)}:${pad(s.minute)}`;
      const parts = [];
      parts.push(s.enabled ? tt("disks.schedEnabled", { time }) : tt("disks.schedDisabled"));
      if (s.lastRunAt) {
        const d = new Date(s.lastRunAt);
        if (!Number.isNaN(d.getTime())) parts.push(tt("disks.lastRan", { when: d.toLocaleString() }));
      }
      return parts.join(" · ");
    },
  },
  async mounted() {
    if (!store.disks) {
      store.disks = await api("/api/admin/disks").catch((err) => {
        this.loadFailed = err.message;
        return [];
      });
    }
    try {
      this.schedule = (await api("/api/backup/schedule")) || { enabled: false, hour: 2, minute: 0 };
    } catch {
      this.schedule = { enabled: false, hour: 2, minute: 0, lastRunAt: "" };
    }
    try {
      const cfg = await api("/api/admin/disks/config");
      this.mountForm = { source: cfg.source || "", target: cfg.target || "", fsType: cfg.fsType || "" };
      this.backupForm = { targetDir: cfg.backupTargetDir || "" };
    } catch {
      /* defaults */
    }
  },
  methods: {
    tt,
    fmtBytes(n) {
      if (!n) return "-";
      if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(0)} MB`;
      return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
    },
    async refreshDisks() {
      store.disks = await api("/api/admin/disks").catch(() => []);
    },
    async saveMountConfig() {
      const payload = {
        source: this.mountForm.source || "",
        target: this.mountForm.target || "",
        fsType: this.mountForm.fsType || "",
        backupTargetDir: this.backupForm.targetDir || "",
      };
      try {
        await api("/api/admin/disks/config", { method: "POST", body: JSON.stringify(payload) });
        ElMessage.success(tt("disks.mountConfigSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async mountNow() {
      try {
        await api("/api/admin/disks/mount", { method: "POST", body: JSON.stringify({ ...this.mountForm }) });
        ElMessage.success(tt("disks.mountDone"));
        await this.saveMountConfig().catch(() => {});
        // Force a fresh fetch so the just-mounted disk shows up immediately.
        await this.refreshDisks();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async backupDb() {
      try {
        const out = await api("/api/admin/backup", { method: "POST", body: JSON.stringify({ targetDir: this.backupForm.targetDir || "" }) });
        ElMessage.success(tt("disks.backupCreated", { path: out.path }));
        await api("/api/admin/disks/config", {
          method: "POST",
          body: JSON.stringify({
            source: this.mountForm.source || "",
            target: this.mountForm.target || "",
            fsType: this.mountForm.fsType || "",
            backupTargetDir: this.backupForm.targetDir || "",
          }),
        }).catch(() => {});
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveSchedule() {
      const enabled = !!this.schedule.enabled;
      const hour = Math.max(0, Math.min(23, parseInt(this.schedule.hour, 10) || 0));
      const minute = Math.max(0, Math.min(59, parseInt(this.schedule.minute, 10) || 0));
      try {
        await api("/api/backup/schedule", { method: "POST", body: JSON.stringify({ hour, minute, enabled }) });
        this.schedule = { ...this.schedule, hour, minute, enabled };
        ElMessage.success(tt("disks.scheduleSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async runNow() {
      try {
        const out = await api("/api/netdisk/backup/all", { method: "POST" });
        ElMessage.success(tt("disks.backupStarted", { n: out.started || 0 }));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async unmount(d) {
      try {
        await ElMessageBox.confirm(tt("disks.unmountConfirm", { target: d.path }), tt("disks.unmount"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/admin/disks/unmount", { method: "POST", body: JSON.stringify({ target: d.path }) });
        ElMessage.success(tt("disks.unmountDone"));
        await this.refreshDisks();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
.disks-layout { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; align-items: start; }
/* The disk table is far wider than a tool column, so it gets its own full-width
   row underneath instead of being squeezed into (and clipped by) a third one. */
.disks-table-card { grid-column: 1 / -1; }
.disks-error { grid-column: 1 / -1; }
.tools-col, .schedule-col { display: flex; flex-direction: column; }
.card-head { display: flex; align-items: center; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; }
.mb { margin-bottom: 8px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; margin-bottom: 10px; }
.bk-sched-time { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; font-size: 13px; }
.primary-line { font-weight: 600; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
@media (max-width: 1000px) { .disks-layout { grid-template-columns: minmax(0, 1fr); } }
</style>
