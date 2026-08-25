<template>
  <el-dialog :model-value="visible" :title="tt('stats.title', { name: title })" width="760px" top="5vh" append-to-body @update:model-value="onVisible">
    <div class="stats-toolbar">
      <el-select v-model="interval" size="small" style="width: 90px" @change="reconnect">
        <el-option v-for="s in [1, 2, 5]" :key="s" :value="s" :label="`${s}s`" />
      </el-select>
    </div>
    <div v-if="error" class="error-box">✗ {{ error }}</div>
    <div v-else-if="!sample" class="empty-state">{{ tt("stats.connecting") }}</div>
    <div v-else class="stats-grid">
      <div class="stat-card">
        <div class="stat-card-head"><span>{{ tt("hardware.cpu") }}</span></div>
        <div class="stat-card-value">{{ (sample.cpuPct || 0).toFixed(1) }}%</div>
        <div class="stat-card-sub"><spark :series="history.cpu" color="#3370ff" /></div>
      </div>
      <div class="stat-card">
        <div class="stat-card-head"><span>{{ tt("common.memory") }}</span></div>
        <div class="stat-card-value">{{ (sample.memMb || 0).toFixed(0) }} MB</div>
        <div class="stat-card-sub">{{ (sample.memPct || 0).toFixed(1) }}% of {{ (sample.memLimitMb || 0).toFixed(0) }} MB</div>
        <div class="bar"><div class="bar-fill" :style="{ width: clamp(sample.memPct || 0) + '%' }"></div></div>
      </div>
      <div class="stat-card">
        <div class="stat-card-head"><span>{{ tt("stats.networkRx") }}</span></div>
        <div class="stat-card-value">{{ (sample.netRxKb || 0).toFixed(1) }} KB</div>
        <div class="stat-card-sub"><spark :series="history.netRx" color="#10b981" /></div>
      </div>
      <div class="stat-card">
        <div class="stat-card-head"><span>{{ tt("stats.networkTx") }}</span></div>
        <div class="stat-card-value">{{ (sample.netTxKb || 0).toFixed(1) }} KB</div>
        <div class="stat-card-sub"><spark :series="history.netTx" color="#f59e0b" /></div>
      </div>
      <div class="stat-card">
        <div class="stat-card-head"><span>{{ tt("stats.blockRead") }}</span></div>
        <div class="stat-card-value">{{ (sample.blockReadKb || 0).toFixed(1) }} KB</div>
        <div class="stat-card-sub hint">{{ tt("stats.diskRead") }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-card-head"><span>{{ tt("stats.blockWrite") }}</span></div>
        <div class="stat-card-value">{{ (sample.blockWriteKb || 0).toFixed(1) }} KB</div>
        <div class="stat-card-sub hint">{{ tt("stats.diskWrite") }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-card-head"><span>{{ tt("stats.processes") }}</span></div>
        <div class="stat-card-value">{{ sample.pids ?? 0 }}</div>
        <div class="stat-card-sub hint">{{ tt("stats.pids") }}</div>
      </div>
      <template v-if="sawGPU">
        <div class="stat-card">
          <div class="stat-card-head"><span>{{ tt("common.gpu") }}</span></div>
          <div class="stat-card-value">{{ (sample.gpuPct || 0).toFixed(1) }}%</div>
          <div class="stat-card-sub"><spark :series="history.gpu" color="#3370ff" /></div>
        </div>
        <div class="stat-card">
          <div class="stat-card-head"><span>{{ tt("stats.gpuMemory") }}</span></div>
          <div class="stat-card-value">{{ (sample.gpuMemMb || 0).toFixed(0) }} MB</div>
          <div class="stat-card-sub">{{ (sample.gpuMemPct || 0).toFixed(1) }}% of {{ (sample.gpuMemLimitMb || 0).toFixed(0) }} MB</div>
          <div class="bar"><div class="bar-fill" :style="{ width: clamp(sample.gpuMemPct || 0) + '%' }"></div></div>
        </div>
      </template>
    </div>
  </el-dialog>
</template>

<script>
import SparkE from "@/components/SparkE.vue";
import { tt } from "@/i18n";

export default {
  name: "StatsDialog",
  components: { Spark: SparkE },
  props: {
    visible: { type: Boolean, default: false },
    id: { type: String, default: "" },
    title: { type: String, default: "" },
  },
  data() {
    return {
      interval: 2,
      sample: null,
      error: "",
      sawGPU: false,
      history: { cpu: [], mem: [], netRx: [], netTx: [], gpu: [], gpuMem: [] },
      es: null,
    };
  },
  watch: {
    visible(v) {
      if (v) this.reconnect();
      else this.closeStream();
    },
  },
  beforeUnmount() {
    this.closeStream();
  },
  methods: {
    tt,
    onVisible(v) {
      this.$emit("update:visible", v);
    },
    clamp(pct) {
      return Math.min(100, Math.max(0, pct));
    },
    push(arr, v) {
      arr.push(Number(v) || 0);
      if (arr.length > 30) arr.shift();
    },
    onSample(s) {
      // The stream stays open when the backend can't sample (container died,
      // docker hiccup) — it sends {"error": …} frames instead of numbers.
      this.error = s.error ? String(s.error) : "";
      if (s.error) return;
      this.sample = s;
      this.push(this.history.cpu, s.cpuPct);
      this.push(this.history.mem, s.memMb);
      this.push(this.history.netRx, s.netRxKb);
      this.push(this.history.netTx, s.netTxKb);
      const hasGPU = (s.gpuPct || s.gpuMemMb || s.gpuMemLimitMb) !== undefined && ((s.gpuMemLimitMb || 0) > 0 || (s.gpuPct || 0) > 0);
      if (hasGPU) {
        this.sawGPU = true;
        this.push(this.history.gpu, s.gpuPct);
        this.push(this.history.gpuMem, s.gpuMemMb);
      }
    },
    reconnect() {
      this.closeStream();
      this.sample = null;
      this.sawGPU = false;
      this.history = { cpu: [], mem: [], netRx: [], netTx: [], gpu: [], gpuMem: [] };
      const url = `/api/containers/stats/stream?id=${encodeURIComponent(this.id)}&interval=${this.interval}`;
      this.es = new EventSource(url);
      this.es.addEventListener("sample", (ev) => {
        try { this.onSample(JSON.parse(ev.data)); } catch { /* malformed frame */ }
      });
      this.es.onerror = () => {
        // Show the frozen-last-sample dialog as ended rather than silently
        // live-looking, mirroring the old "Stream ended." hint.
        this.error = this.error || "stream ended";
        if (this.es) this.es.close();
        this.es = null;
      };
    },
    closeStream() {
      if (this.es) {
        this.es.close();
        this.es = null;
      }
    },
  },
};
</script>

<style scoped>
.stats-toolbar { margin-bottom: 10px; }
.stats-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(215px, 1fr)); gap: 12px; }
.stat-card { border: 1px solid var(--line); border-radius: 10px; padding: 12px; }
.stat-card-head { color: var(--muted); font-size: 12px; }
.stat-card-value { font-size: 21px; font-weight: 700; margin-top: 4px; }
.stat-card-sub { margin-top: 6px; font-size: 12px; color: var(--muted); }
.stat-card-sub .spark-box { height: 28px; }
.bar { height: 5px; background: var(--line); border-radius: 3px; overflow: hidden; margin-top: 6px; }
.bar-fill { height: 100%; background: var(--brand); }
.error-box { background: #fef2f2; color: #b91c1c; border: 1px solid #fecaca; border-radius: 8px; padding: 10px 12px; }
</style>
