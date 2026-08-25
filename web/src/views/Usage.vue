<template>
  <div class="stack">
    <div class="card">
      <div class="card-head"><h2>{{ tt("usage.resourceUsage") }}</h2></div>
      <el-table :data="rows" size="small" :empty-text="tt('usage.noUsage')">
        <el-table-column :label="tt('common.user')">
          <template #default="{ row }"><span class="primary-line">{{ displayName(row) }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('usage.colContainers')" width="100">
          <template #default="{ row }">{{ row.containers }}</template>
        </el-table-column>
        <el-table-column :label="tt('usage.colMemory')" width="100">
          <template #default="{ row }">{{ (row.memoryMb || 0).toFixed(0) }} MB</template>
        </el-table-column>
        <el-table-column :label="tt('usage.colDisk')" width="100">
          <template #default="{ row }">{{ (row.diskMb || 0).toFixed(0) }} MB</template>
        </el-table-column>
        <el-table-column :label="tt('usage.colGpu')" min-width="110">
          <template #default="{ row }"><span class="secondary-line">{{ row.gpu || "none" }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('usage.colGpuUsage')" width="100">
          <template #default="{ row }">
            <span v-if="!row.gpu || row.gpu === 'none'" class="hint">n/a</span>
            <span v-else>{{ (row.gpuPct || 0).toFixed(1) }}%</span>
          </template>
        </el-table-column>
        <el-table-column :label="tt('usage.colGpuMemory')" width="130">
          <template #default="{ row }">
            <template v-if="row.gpu && row.gpu !== 'none' && row.gpuMemTotalMb">
              <div>{{ (row.gpuMemMb || 0).toFixed(0) }} / {{ row.gpuMemTotalMb.toFixed(0) }} MB</div>
              <div class="secondary-line">{{ ((row.gpuMemPct || ((row.gpuMemMb / row.gpuMemTotalMb) * 100)) || 0).toFixed(1) }}%</div>
            </template>
            <span v-else class="hint">n/a</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div class="card">
      <div class="card-head"><h2>{{ tt("usage.last24h") }}</h2></div>
      <div v-if="!trends.length" class="empty-state">{{ tt("usage.noHistory") }}</div>
      <div v-else class="stats-grid">
        <div v-for="tr in trends" :key="tr.user" class="stat-card">
          <div class="stat-card-head">{{ displayNameForUsername(tr.user) }}</div>
          <div class="stat-card-value">{{ tr.maxCpu }}% {{ tt("hardware.cpu") }}</div>
          <div class="stat-card-sub">{{ tt("usage.peakGpu", { mem: tr.maxMem, gpu: tr.maxGpu }) }}</div>
          <spark :series="tr.cpu" color="#3370ff" height="34px" />
        </div>
      </div>
    </div>

    <div v-if="isAdmin()" class="card">
      <div class="card-head"><h2>{{ tt("usage.topProcesses") }}</h2></div>
      <el-table :data="processes" size="small" :empty-text="tt('usage.noProcess')">
        <el-table-column :label="tt('common.user')" width="130">
          <template #default="{ row }">{{ displayNameForUsername(row.user || "") }}</template>
        </el-table-column>
        <el-table-column :label="tt('usage.colContainer')" min-width="130">
          <template #default="{ row }">{{ row.container || "" }}</template>
        </el-table-column>
        <el-table-column :label="tt('usage.colPid')" width="90">
          <template #default="{ row }"><span class="mono">{{ row.pid || "" }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('usage.colCpu')" width="90">
          <template #default="{ row }">{{ (row.cpuPct || 0).toFixed(1) }}%</template>
        </el-table-column>
        <el-table-column :label="tt('usage.colCommand')" min-width="220">
          <template #default="{ row }"><span class="secondary-line mono">{{ row.command || "" }}</span></template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script>
import { api } from "@/api";
import { store, isAdmin, displayName, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import { registerRouteRefresh, unregisterRouteRefresh } from "@/refresh";
import SparkE from "@/components/SparkE.vue";

function avg(values) {
  if (!values.length) return 0;
  return values.reduce((sum, v) => sum + v, 0) / values.length;
}

export default {
  name: "Usage",
  components: { Spark: SparkE },
  data() {
    return { history: [], processes: [] };
  },
  computed: {
    rows() {
      if (isAdmin()) return store.usage || [];
      const cs = store.containers || [];
      return [{
        username: store.me?.username || "me",
        displayName: store.me?.displayName || "",
        containers: cs.length,
        memoryMb: cs.reduce((sum, c) => sum + (c.memoryMb || 0), 0),
        diskMb: cs.reduce((sum, c) => sum + (c.diskMb || 0), 0),
        gpu: [...new Set(cs.map((c) => c.gpu).filter((g) => g && g !== "none"))].join(", "),
        gpuPct: avg(cs.map((c) => c.gpuPct || 0).filter(Boolean)),
        gpuMemMb: cs.reduce((sum, c) => sum + (c.gpuMemMb || 0), 0),
        gpuMemTotalMb: cs.reduce((sum, c) => sum + (c.gpuMemTotalMb || 0), 0),
      }];
    },
    trends() {
      const byUser = new Map();
      for (const s of this.history || []) {
        const key = s.username || "unknown";
        const list = byUser.get(key) || [];
        list.push(s);
        byUser.set(key, list);
      }
      return [...byUser.entries()].slice(0, 12).map(([user, list]) => {
        // Samples are per-container, but each snapshot round shares one
        // timestamp: aggregate per timestamp so the trend reflects the user's
        // total footprint, not the largest single container reading.
        const byTs = new Map();
        for (const x of list) {
          const ts = x.createdAt || "";
          const agg = byTs.get(ts) || { cpu: 0, mem: 0, gpu: 0 };
          agg.cpu += x.cpuPct || 0;
          agg.mem += x.memMb || 0;
          // GPU % is already a per-user average in the snapshot; don't double-sum.
          agg.gpu = Math.max(agg.gpu, x.gpuPct || 0);
          byTs.set(ts, agg);
        }
        const series = [...byTs.values()];
        const cpu = series.map((x) => x.cpu);
        return {
          user,
          cpu,
          maxCpu: Math.max(...cpu, 0).toFixed(1),
          maxMem: Math.max(...series.map((x) => x.mem), 0).toFixed(0),
          maxGpu: Math.max(...series.map((x) => x.gpu), 0).toFixed(1),
        };
      });
    },
  },
  async mounted() {
    registerRouteRefresh("usage", () => this.load());
    await this.load();
  },
  beforeUnmount() {
    unregisterRouteRefresh("usage");
  },
  methods: {
    tt,
    isAdmin,
    displayName,
    displayNameForUsername,
    async load() {
      const [history, processes, usage] = await Promise.all([
        api("/api/resources/history").catch(() => []),
        isAdmin() ? api("/api/admin/processes").catch(() => []) : Promise.resolve([]),
        isAdmin() ? api("/api/admin/usage").catch(() => null) : Promise.resolve(null),
      ]);
      this.history = history || [];
      this.processes = processes || [];
      if (isAdmin() && usage) store.usage = usage;
    },
  },
};
</script>

<style scoped>
.stack > * + * { margin-top: 16px; }
.card-head { display: flex; align-items: center; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; }
.stats-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }
.stat-card { border: 1px solid var(--line); border-radius: 10px; padding: 12px; }
.stat-card-head { color: var(--muted); font-size: 12px; }
.stat-card-value { font-size: 18px; font-weight: 700; margin-top: 4px; }
.stat-card-sub { margin-top: 4px; font-size: 12px; color: var(--muted); }
.spark-box { margin-top: 6px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
</style>
