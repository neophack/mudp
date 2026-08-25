<template>
  <div v-if="snap" class="dash-stack">
    <div class="dash-tiles">
      <section class="card stat-tile">
        <div class="stat-tile-head"><span class="stat-emoji">🖥</span><span class="stat-label">{{ tt("hardware.cpu") }}</span></div>
        <div class="stat-value">{{ (host.cpuPct ?? 0).toFixed(1) }}%</div>
        <el-progress :percentage="clamp(host.cpuPct)" :stroke-width="6" :show-text="false" />
        <div class="stat-sub">{{ tt("hardware.cores", { n: sys.cpus ?? "?" }) }}</div>
      </section>
      <section class="card stat-tile">
        <div class="stat-tile-head"><span class="stat-emoji">🧠</span><span class="stat-label">{{ tt("hardware.memory") }}</span></div>
        <div class="stat-value">{{ (host.memPct ?? 0).toFixed(1) }}%</div>
        <el-progress :percentage="clamp(host.memPct)" :stroke-width="6" :show-text="false" />
        <div class="stat-sub">{{ fmtMB(host.memUsedMb) }} / {{ fmtMB(host.memTotalMb) }}</div>
      </section>
      <section class="card stat-tile">
        <div class="stat-tile-head"><span class="stat-emoji">📈</span><span class="stat-label">{{ tt("hardware.load1m") }}</span></div>
        <div class="stat-value">{{ (host.load1 ?? 0).toFixed(2) }}</div>
        <div class="stat-sub">{{ tt("hardware.cpu") }}: {{ sys.cpus ?? "?" }}</div>
      </section>
      <section class="card stat-tile">
        <div class="stat-tile-head"><span class="stat-emoji">🌡</span><span class="stat-label">{{ tt("hardware.gpuTemp") }}</span></div>
        <div class="stat-value">{{ avgTemp }}</div>
        <div class="stat-sub">{{ gpus.length }} GPU{{ gpus.length === 1 ? "" : "s" }}</div>
      </section>
    </div>

    <!-- Rolling history (reset on tab entry, like htop/nvtop graphs). -->
    <div class="hist-row">
      <div class="card hist-card">
        <div class="hist-head">
          <span class="hist-label">{{ tt("hardware.cpuUsage") }}</span>
          <span class="hist-current" style="color: #3370ff">{{ last(history.cpu).toFixed(1) }}%</span>
        </div>
        <e-chart v-if="history.cpu.length > 1" :option="cpuOption" height="140px" />
        <p v-else class="hint">{{ tt("common.collectingData") }}</p>
      </div>
      <div class="card hist-card">
        <div class="hist-head">
          <span class="hist-label">{{ tt("hardware.memUsage") }}</span>
          <span class="hist-current" style="color: #10b981">{{ last(history.mem).toFixed(1) }}%</span>
        </div>
        <e-chart v-if="history.mem.length > 1" :option="memOption" height="140px" />
        <p v-else class="hint">{{ tt("common.collectingData") }}</p>
      </div>
    </div>

    <div class="dash-section-head">
      <h2>{{ tt("hardware.gpus") }}</h2>
      <span class="hint">{{ tt("hardware.nDetected", { n: gpus.length }) }}</span>
    </div>
    <div v-if="gpus.length" class="gpu-grid">
      <section v-for="g in gpus" :key="g.index" class="card gpu-card">
        <div class="card-head">
          <h3>{{ g.name || "GPU " + g.index }}</h3>
          <el-tag size="small" :type="tempTag(g.tempC)">{{ (g.tempC ?? 0).toFixed(0) }}°C</el-tag>
        </div>
        <div class="metric-row">
          <div class="metric-label">{{ tt("hardware.gpuUtil") }}</div>
          <div class="metric-value">{{ (g.utilPct ?? 0).toFixed(0) }}%</div>
          <el-progress :percentage="clamp(g.utilPct)" :stroke-width="6" :show-text="false" />
        </div>
        <div class="metric-row">
          <div class="metric-label">{{ tt("hardware.memory") }}</div>
          <div class="metric-value">{{ fmtMB(g.memUsedMb) }} / {{ fmtMB(g.memTotalMb) }}</div>
          <el-progress :percentage="clamp(g.memPct)" :stroke-width="6" :show-text="false" />
        </div>
        <div v-if="g.powerW > 0" class="metric-row">
          <div class="metric-label">{{ tt("hardware.power") }}</div>
          <div class="metric-value">{{ g.powerW.toFixed(1) }} W</div>
        </div>
        <div v-if="g.memUtilPct > 0" class="metric-row">
          <div class="metric-label">{{ tt("hardware.memController") }}</div>
          <div class="metric-value">{{ g.memUtilPct.toFixed(0) }}%</div>
          <el-progress :percentage="clamp(g.memUtilPct)" :stroke-width="6" :show-text="false" />
        </div>
        <div class="gpu-trends">
          <div class="trend-row">
            <span class="trend-label">{{ tt("hardware.utilTrend") }}</span>
            <spark :series="gpuHistory(g.index).util" color="#3370ff" />
          </div>
          <div class="trend-row">
            <span class="trend-label">{{ tt("hardware.tempTrend") }}</span>
            <spark :series="gpuHistory(g.index).temp" :color="trendColor(g.tempC)" />
          </div>
        </div>
      </section>
    </div>
    <div v-else class="card"><p class="hint">{{ tt("hardware.noGpus") }}</p></div>

    <div class="dash-row-2">
      <div class="card">
        <div class="card-head"><h2>{{ tt("hardware.host") }}</h2></div>
        <div v-for="(row, i) in hostRows" :key="i" class="kv">
          <span>{{ row[0] }}</span><strong>{{ row[1] }}</strong>
        </div>
      </div>
      <div class="card">
        <div class="card-head"><h2>{{ tt("hardware.temperatures") }}</h2></div>
        <div v-if="!(host.temp || []).length" class="empty-state">{{ tt("hardware.noTempSensors") }}</div>
        <div v-for="tp in host.temp || []" :key="tp.name" class="sensor-row">
          <div class="kv">
            <span>{{ tp.name }}</span>
            <strong :style="{ color: trendColor(tp.tempC) }">{{ (tp.tempC ?? 0).toFixed(1) }}°C</strong>
          </div>
          <spark :series="sensorHistory(tp.name)" :color="trendColor(tp.tempC)" autoscale />
        </div>
      </div>
    </div>

    <p class="hint">{{ tt("hardware.updated", { time: updatedTime, sec: POLL_MS / 1000 }) }}</p>
  </div>
  <div v-else-if="error" class="card"><div class="error-box">✗ {{ error }}</div></div>
  <div v-else class="card"><p class="hint">{{ tt("hardware.loadingSnapshot") }}</p></div>
</template>

<script>
import { api } from "@/api";
import { tt } from "@/i18n";
import EChart from "@/components/EChart.vue";
import SparkE from "@/components/SparkE.vue";

const POLL_MS = 5000;
// 120 samples @ 5s = 10 minutes of history, similar to htop's default window.
const HISTORY_LEN = 120;

function lineOption(values, times, color) {
  return {
    grid: { left: 34, right: 8, top: 8, bottom: 20 },
    xAxis: {
      type: "category",
      data: times.map((t) => new Date(t).toLocaleTimeString()),
      axisLabel: { fontSize: 10, color: "#94a3b8" },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: "value",
      min: 0,
      max: 100,
      axisLabel: { fontSize: 10, color: "#94a3b8", formatter: "{value}%" },
      splitLine: { lineStyle: { color: "#eef2f7" } },
    },
    tooltip: {
      trigger: "axis",
      formatter: (params) => {
        const p = params[0];
        return `${p.axisValue}<br><b>${Number(p.value).toFixed(1)}%</b>`;
      },
    },
    series: [{
      type: "line",
      data: values.map((v) => Number(v.toFixed(1))),
      showSymbol: false,
      smooth: true,
      lineStyle: { color, width: 2 },
      areaStyle: { color, opacity: 0.1 },
    }],
  };
}

export default {
  name: "Hardware",
  components: { EChart, Spark: SparkE },
  data() {
    return {
      POLL_MS,
      snap: null,
      error: "",
      history: { t: [], cpu: [], mem: [], gpu: {}, sensor: {} },
    };
  },
  computed: {
    sys() { return this.snap?.system || {}; },
    host() { return this.snap?.host || {}; },
    gpus() { return this.snap?.gpus || []; },
    avgTemp() {
      const temps = this.gpus.map((g) => Number(g.tempC)).filter((t) => t > 0);
      if (!temps.length) return "—";
      return (temps.reduce((a, b) => a + b, 0) / temps.length).toFixed(0) + "°C";
    },
    updatedTime() {
      return new Date(this.snap?.updated ?? Date.now()).toLocaleTimeString();
    },
    hostRows() {
      const sys = this.sys;
      const host = this.host;
      return [
        [tt("hardware.host"), sys.name],
        ["OS", `${sys.osType || ""} ${sys.osVersion || ""}`.trim()],
        ["Kernel", sys.kernel],
        [tt("dash.arch"), sys.arch],
        [tt("hardware.colCpuCores"), sys.cpus],
        [tt("hardware.colTotalMemory"), this.fmtMB(host.memTotalMb)],
        ["Docker", sys.dockerVersion],
        [tt("hardware.colStorageDriver"), sys.storageDriver],
      ].filter(([, v]) => v !== undefined && v !== null && v !== "");
    },
    cpuOption() { return lineOption(this.history.cpu, this.history.t, "#3370ff"); },
    memOption() { return lineOption(this.history.mem, this.history.t, "#10b981"); },
  },
  mounted() {
    this.refresh();
    this.timer = setInterval(this.refresh, POLL_MS);
  },
  beforeUnmount() {
    clearInterval(this.timer);
  },
  methods: {
    tt,
    clamp(pct) {
      return Math.max(0, Math.min(100, Number(pct) || 0));
    },
    last(arr) {
      return arr.length ? arr[arr.length - 1] : 0;
    },
    fmtMB(mb) {
      const v = Number(mb) || 0;
      if (v >= 1024) return (v / 1024).toFixed(1) + " GB";
      return v.toFixed(0) + " MB";
    },
    tempTag(tempC) {
      const t = Number(tempC) || 0;
      if (t >= 85) return "warning";
      if (t >= 70) return "success";
      return "info";
    },
    trendColor(tempC) {
      const t = Number(tempC) || 0;
      if (t >= 85) return "#ef4444";
      if (t >= 70) return "#f59e0b";
      return "#10b981";
    },
    gpuHistory(index) {
      return this.history.gpu[index] || { util: [], temp: [] };
    },
    sensorHistory(name) {
      return this.history.sensor[name] || [];
    },
    pushSeries(arr, value) {
      arr.push(value);
      if (arr.length > HISTORY_LEN) arr.shift();
    },
    pushHistory(snap) {
      const host = snap.host || {};
      const now = snap.updated ?? Date.now();
      const h = this.history;
      this.pushSeries(h.t, now);
      this.pushSeries(h.cpu, Number(host.cpuPct) || 0);
      this.pushSeries(h.mem, Number(host.memPct) || 0);
      const seenGpu = new Set();
      for (const g of snap.gpus || []) {
        seenGpu.add(g.index);
        if (!h.gpu[g.index]) h.gpu[g.index] = { util: [], temp: [] };
        const gh = h.gpu[g.index];
        this.pushSeries(gh.util, Number(g.utilPct) || 0);
        this.pushSeries(gh.temp, Number(g.tempC) || 0);
      }
      for (const key of Object.keys(h.gpu)) if (!seenGpu.has(key)) delete h.gpu[key];
      const seenSensor = new Set();
      for (const tp of host.temp || []) {
        seenSensor.add(tp.name);
        if (!h.sensor[tp.name]) h.sensor[tp.name] = [];
        this.pushSeries(h.sensor[tp.name], Number(tp.tempC) || 0);
      }
      for (const key of Object.keys(h.sensor)) if (!seenSensor.has(key)) delete h.sensor[key];
    },
    // Skip while the tab is hidden — there's no point sampling sensors nobody
    // is looking at; the interval keeps running so sampling resumes on focus.
    async refresh() {
      if (document.hidden) return;
      try {
        const snap = await api("/api/hardware");
        this.snap = snap;
        this.error = "";
        this.pushHistory(snap);
      } catch (err) {
        // Keep polling so the page recovers when the host comes back.
        this.error = err.message;
      }
    },
  },
};
</script>

<style scoped>
.dash-stack > * + * { margin-top: 16px; }
.dash-tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 16px; }
.stat-tile-head { display: flex; align-items: center; gap: 8px; }
.stat-emoji { font-size: 18px; }
.stat-label { color: var(--muted); font-size: 12.5px; }
.stat-value { font-size: 23px; font-weight: 750; margin: 6px 0; }
.stat-sub { color: var(--muted); font-size: 12px; margin-top: 4px; }
.hist-row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; }
.hist-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 8px; }
.hist-label { font-weight: 600; font-size: 13.5px; }
.hist-current { font-size: 16px; font-weight: 700; }
.dash-section-head { display: flex; align-items: center; gap: 10px; }
.dash-section-head h2 { margin: 0; font-size: 14px; }
.gpu-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 16px; }
.gpu-card h3 { margin: 0; font-size: 13.5px; flex: 1; }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.metric-row { margin-bottom: 10px; }
.metric-label { color: var(--muted); font-size: 12px; }
.metric-value { font-size: 13.5px; font-weight: 600; margin: 2px 0 4px; }
.gpu-trends { border-top: 1px dashed var(--line); padding-top: 8px; }
.trend-row { display: flex; align-items: center; gap: 10px; margin-top: 4px; }
.trend-label { font-size: 11.5px; color: var(--muted); width: 70px; }
.trend-row .spark-box { flex: 1; min-width: 90px; }
.kv { display: flex; justify-content: space-between; gap: 12px; font-size: 13px; padding: 3px 0; }
.kv span { color: var(--muted); }
.sensor-row { padding: 6px 0; border-bottom: 1px dashed var(--line); }
.dash-row-2 { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: 16px; }
.error-box { background: var(--danger-bg); color: var(--danger-text); border: 1px solid var(--danger-line); border-radius: 8px; padding: 10px 12px; }
@media (max-width: 1000px) { .hist-row, .dash-row-2 { grid-template-columns: minmax(0, 1fr); } }
</style>
