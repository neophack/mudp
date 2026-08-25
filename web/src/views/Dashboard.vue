<template>
  <div v-if="d" class="dash-stack">
    <p v-if="!admin" class="hint dash-scope">{{ tt("dash.scopeMine") }}</p>

    <!-- Row 1: four stat tiles with tinted icon squares -->
    <div class="dash-tiles">
      <section class="card stat-tile">
        <span class="stat-icon tint-blue"><v-icon name="containers" :size="17" /></span>
        <div class="stat-body">
          <div class="stat-value">{{ sys.containers?.total ?? 0 }}</div>
          <div class="stat-label">{{ tt("nav.containers") }} · {{ tt("dash.running", { n: sys.containers?.running ?? 0 }) }}</div>
        </div>
      </section>
      <section class="card stat-tile">
        <span class="stat-icon tint-purple"><v-icon name="images" :size="17" /></span>
        <div class="stat-body">
          <div class="stat-value">{{ sys.images?.count ?? 0 }}</div>
          <div class="stat-label">{{ tt("nav.images") }} · {{ admin ? fmtMB(sys.images?.sizeMb) : tt("dash.inUse", { n: sys.images?.count ?? 0 }) }}</div>
        </div>
      </section>
      <section class="card stat-tile">
        <span class="stat-icon tint-orange"><v-icon name="volumes" :size="17" /></span>
        <div class="stat-body">
          <div class="stat-value">{{ sys.volumes?.count ?? 0 }}</div>
          <div class="stat-label">{{ tt("nav.volumes") }} · {{ fmtMB(sys.volumes?.sizeMb) }}</div>
        </div>
      </section>
      <section class="card stat-tile">
        <span class="stat-icon tint-green"><v-icon name="networks" :size="17" /></span>
        <div class="stat-body">
          <div class="stat-value">{{ sys.networks ?? 0 }}</div>
          <div class="stat-label">{{ tt("nav.networks") }} · {{ admin ? tt("dash.managedSystem") : tt("dash.mineSystem") }}</div>
        </div>
      </section>
    </div>

    <!-- Version banner: platform information, admins only. -->
    <section v-if="admin" class="card ver-banner">
      <div class="ver-main">
        <div class="ver-row">
          <span class="mono ver-current">{{ s.me?.version || "dev" }}</span>
          <el-tag v-if="update && !update.error" size="small" :type="update.available ? 'warning' : 'success'">
            {{ update.available ? tt("dash.updateAvailable") : tt("dash.upToDate") }}
          </el-tag>
          <span class="hint">{{ tt("dash.latestVersion") }}: <span class="mono" :class="{ hint: !update || update.error }">{{ latestText }}</span></span>
        </div>
        <div v-if="update && update.downloads" class="dl-links">
          <a :href="update.downloads.windows || '#'" download>mudp-windows-amd64</a> ·
          <a :href="update.downloads.linux || '#'" download>mudp-linux-amd64</a> ·
          <a :href="update.downloads['windows-arm64'] || '#'" download>mudp-windows-arm64</a> ·
          <a :href="update.downloads['linux-arm64'] || '#'" download>mudp-linux-arm64</a>
        </div>
      </div>
      <div class="ver-actions">
        <el-button size="small" :disabled="rechecking" @click="fillVersion(true)">{{ rechecking ? tt("dash.checking") : tt("dash.recheck") }}</el-button>
        <el-button v-if="update && update.available" type="primary" size="small" :disabled="isUpgrading()" @click="openUpgrade(update)">
          {{ isUpgrading() ? tt("upgrade.busy") : tt("dash.upgrade") }}
        </el-button>
      </div>
    </section>

    <!-- Row 2: environment (wide) + donut chart -->
    <div class="dash-row-2">
      <section class="card">
        <div class="card-head">
          <h2>{{ tt("dash.environment") }}</h2>
          <el-tag size="small" :type="healthy ? 'success' : 'danger'">
            <span class="tag-dot"></span>{{ healthy ? tt("dash.healthy") : tt("dash.unreachable") }}
          </el-tag>
        </div>
        <div class="env-grid">
          <div class="env-item"><span>{{ tt("hardware.host") }}</span><b>{{ sys.name || "—" }}</b></div>
          <div class="env-item"><span>{{ tt("dash.lanIp") }}</span><b>{{ (sys.lanIps || []).join(", ") || "—" }}</b></div>
          <!-- Public IP is the server's own egress address, distinct from the
               visitor's browser IP shown in "My Workspace". -->
          <div class="env-item"><span>{{ tt("dash.publicIp") }}</span><b>{{ sys.publicIp || "—" }}</b></div>
          <div class="env-item"><span>OS</span><b>{{ [sys.osType, sys.osVersion].filter(Boolean).join(" ") || "—" }}</b></div>
          <div class="env-item"><span>Kernel</span><b>{{ sys.kernel || "—" }}</b></div>
          <div class="env-item"><span>{{ tt("dash.arch") }}</span><b>{{ sys.arch || "—" }}</b></div>
          <div class="env-item"><span>CPUs</span><b>{{ sys.cpus ?? "—" }}</b></div>
          <div class="env-item"><span>{{ tt("hardware.memory") }}</span><b>{{ sys.memoryGb ? `${sys.memoryGb} GB` : "—" }}</b></div>
          <div class="env-item"><span>Docker</span><b>{{ sys.dockerVersion || "—" }}</b></div>
          <div class="env-item"><span>{{ tt("hardware.colStorageDriver") }}</span><b>{{ sys.storageDriver || "—" }}</b></div>
          <div class="env-item env-item-wide"><span>{{ tt("dash.agent") }}</span><b>{{ sys.agentGoRuntime || "—" }} · {{ sys.agentCpu ?? "—" }} CPU · {{ fmtMB(sys.agentMemMb) }} mem</b></div>
        </div>
      </section>

      <section class="card">
        <div class="card-head"><h2>{{ tt("dash.containers") }}</h2></div>
        <div class="card-body chart-row">
          <e-chart :option="donutOption" height="190px" width="190px" />
          <ul class="legend">
            <li><span class="swatch" style="background: var(--ok)"></span>{{ tt("containers.filterRunning") }} <strong>{{ sys.containers?.running || 0 }}</strong></li>
            <li><span class="swatch" style="background: var(--warn)"></span>{{ tt("containers.filterPaused") }} <strong>{{ sys.containers?.paused || 0 }}</strong></li>
            <li><span class="swatch" style="background: var(--muted)"></span>{{ tt("containers.filterStopped") }} <strong>{{ sys.containers?.stopped || 0 }}</strong></li>
            <li v-if="(sys.containers?.unhealthy || 0) > 0"><span class="swatch" style="background: var(--danger)"></span>Unhealthy <strong>{{ sys.containers.unhealthy }}</strong></li>
          </ul>
        </div>
      </section>
    </div>

    <!-- Row 2.5: Feishu identity card (only for Feishu-SSO users) -->
    <div v-if="feishuUser" class="dash-row-feishu">
      <section class="card feishu-card">
        <div class="card-head">
          <h2>{{ tt("dash.feishuProfile") }}</h2>
          <el-tag size="small" type="success">{{ tt("dash.feishuLoginMethod") }}</el-tag>
        </div>
        <div class="card-body feishu-card-body">
          <div class="feishu-header">
            <div class="feishu-avatar">{{ userInitial }}</div>
            <div class="feishu-title">
              <div class="feishu-name">{{ user.displayName || user.username || "—" }}</div>
              <div class="feishu-handle">{{ user.username || "—" }}</div>
            </div>
          </div>
          <dl class="detail feishu-detail">
            <dt>{{ tt("dash.feishuOpenId") }}</dt><dd>{{ user.feishuOpenId || "—" }}</dd>
            <dt>{{ tt("dash.feishuTenant") }}</dt><dd>{{ user.feishuTenantName || user.feishuTenantKey || "—" }}</dd>
            <dt>{{ tt("dash.feishuLastLogin") }}</dt><dd>{{ user.lastLoginAt || "—" }}</dd>
          </dl>
        </div>
      </section>
    </div>

    <!-- Row 3: my workspace + top users (admin) or my containers -->
    <div class="dash-row-3">
      <section class="card">
        <div class="card-head"><h2>{{ tt("dash.myWorkspace") }}</h2></div>
        <div class="card-body">
          <div class="kv"><span>{{ tt("nav.containers") }}</span><strong>{{ used }}{{ mine.cap ? ` / ${mine.cap}` : "" }}</strong></div>
          <div class="bar"><div class="bar-fill" :style="{ width: quotaPct + '%' }"></div></div>
          <div class="kv-row">
            <div class="kv"><span>{{ tt("containers.filterRunning") }}</span><strong>{{ mine.running ?? 0 }}</strong></div>
            <div class="kv"><span>{{ tt("hardware.memory") }}</span><strong>{{ fmtMB(mine.memoryMb) }}</strong></div>
            <div class="kv"><span>{{ tt("common.disk") }}</span><strong>{{ fmtMB(mine.diskMb) }}</strong></div>
          </div>
          <!-- Your IP: the visitor's own browser IP + location, detected
               client-side via WebRTC/STUN after render, then geo-located
               through /api/geo. Cached values render immediately so the
               dashboard never flashes "detecting" on every poll. -->
          <div class="client-ip-block">
            <div class="kv">
              <span>{{ tt("dash.yourIp") }}</span>
              <span class="client-ip-main">
                <strong class="mono" :class="{ hint: !clientIp }">{{ clientIp || tt("dash.detecting") }}</strong>
                <span v-if="clientLoc" class="client-loc" v-html="clientLoc"></span>
              </span>
            </div>
            <div class="kv"><span>{{ tt("dash.yourLan") }}</span><strong class="mono" :class="{ hint: !clientLan }">{{ clientLan || tt("dash.detecting") }}</strong></div>
          </div>
        </div>
      </section>

      <section v-if="admin" class="card">
        <div class="card-head"><h2>{{ tt("dash.topUsers") }}</h2></div>
        <el-table :data="topUsers" size="small" empty-text="—">
          <el-table-column :label="tt('common.user')">
            <template #default="{ row }">{{ row.displayName || row.username }}</template>
          </el-table-column>
          <el-table-column prop="containers" :label="tt('common.containers')" width="90" />
          <el-table-column :label="tt('hardware.memory')" width="90">
            <template #default="{ row }">{{ fmtMB(row.memoryMb) }}</template>
          </el-table-column>
          <el-table-column :label="tt('common.disk')" width="90">
            <template #default="{ row }">{{ fmtMB(row.diskMb) }}</template>
          </el-table-column>
          <el-table-column :label="tt('common.gpu')">
            <template #default="{ row }">{{ row.gpu || "none" }}</template>
          </el-table-column>
        </el-table>
      </section>

      <section v-else class="card">
        <div class="card-head"><h2>{{ tt("dash.myContainers") }}</h2></div>
        <div class="card-body">
          <ul class="mini-list">
            <li v-for="c in myContainers" :key="c.id">
              <el-tag size="small" :type="stateTag(c.state)">{{ stateLabel(c.state) }}</el-tag>
              <span class="primary-line">{{ c.name || c.fullName }}</span>
              <span class="secondary-line">{{ c.image || "" }}</span>
            </li>
            <li v-if="!myContainers.length" class="hint">{{ tt("dash.noContainers") }}</li>
          </ul>
        </div>
      </section>
    </div>

    <!-- Row 4: recent activity (admin only) -->
    <div v-if="admin" class="dash-row-full">
      <section class="card">
        <div class="card-head"><h2>{{ tt("dash.recentActivity") }}</h2></div>
        <div class="card-body">
          <ul class="mini-list activity">
            <li v-for="(e, i) in recentActivity" :key="i">
              <span class="audit-actor">{{ e.actor }}</span>
              <span class="audit-act">{{ e.action }}</span>
              <span class="audit-target mono">{{ e.target }}</span>
              <span class="audit-time">{{ relTime(e.createdAt) }}</span>
            </li>
            <li v-if="!recentActivity.length" class="hint">{{ tt("dash.noActivity") }}</li>
          </ul>
        </div>
      </section>
    </div>
  </div>
  <div v-else class="card"><p class="hint">{{ tt("subtitle.dashboard") }}</p></div>
</template>

<script>
import { api } from "@/api";
import { store, isAdmin } from "@/store";
import { tt } from "@/i18n";
import { openUpgrade, isUpgrading } from "@/upgrade";
import { detectClientIP, readIPCache, isCacheFresh } from "@/lib/publicip.js";
import EChart from "@/components/EChart.vue";
import VIcon from "@/components/VIcon.vue";

// Containers-by-state donut.
function donutOptionFor(c) {
  const data = [
    { name: "running", value: c.running || 0, itemStyle: { color: "#10b981" } },
    { name: "paused", value: c.paused || 0, itemStyle: { color: "#f59e0b" } },
    { name: "stopped", value: c.stopped || 0, itemStyle: { color: "#94a3b8" } },
  ];
  if ((c.unhealthy || 0) > 0) data.push({ name: "unhealthy", value: c.unhealthy, itemStyle: { color: "#ef4444" } });
  return {
    tooltip: { trigger: "item" },
    series: [{
      type: "pie",
      radius: ["62%", "88%"],
      avoidLabelOverlap: true,
      label: { show: false },
      data,
    }],
    graphic: [],
  };
}

function countryCodeFlag(code) {
  if (!code || code.length !== 2) return "🌍";
  const A = 0x1f1e6;
  const base = "A".charCodeAt(0);
  const cc = code.toUpperCase();
  return String.fromCodePoint(A + cc.charCodeAt(0) - base, A + cc.charCodeAt(1) - base);
}

export default {
  name: "Dashboard",
  components: { EChart, VIcon },
  data() {
    return {
      s: store,
      clientIp: "",
      clientLan: "",
      clientLoc: "",
      update: null,
      rechecking: false,
    };
  },
  computed: {
    d() { return store.dashboard; },
    sys() { return this.d?.system || {}; },
    mine() { return this.d?.mine || {}; },
    user() { return this.d?.user || {}; },
    admin() { return isAdmin(); },
    feishuUser() { return !!(this.user.feishuOpenId && this.user.feishuOpenId !== ""); },
    userInitial() {
      const name = (this.user.displayName || this.user.username || "").trim();
      return name ? name[0].toUpperCase() : "?";
    },
    healthy() { return !!this.sys.healthy; },
    used() { return this.mine.containers || 0; },
    quotaPct() {
      const cap = this.mine.cap || 0;
      return cap > 0 ? Math.min(100, Math.round((this.used / cap) * 100)) : 0;
    },
    donutOption() { return donutOptionFor(this.sys.containers || {}); },
    topUsers() {
      return [...(this.d?.usage || [])]
        .sort((a, b) => (b.containers || 0) - (a.containers || 0))
        .slice(0, 6);
    },
    myContainers() {
      return (store.containers || []).slice(0, 6);
    },
    recentActivity() {
      return (store.audit || []).slice(0, 8);
    },
    latestText() {
      if (!this.update) return tt("dash.checking");
      if (this.update.error) return tt("dash.checkFailed");
      return this.update.latest || "—";
    },
  },
  async mounted() {
    // Show the cached browser-IP result immediately, then refresh silently.
    const cached = readIPCache();
    if (cached && isCacheFresh(cached)) {
      if (cached.public?.length) this.clientIp = cached.public.join(", ");
      if (cached.lan?.length) this.clientLan = cached.lan.join(", ");
    }
    this.probeClientIP();
    this.fillVersion(false);
  },
  methods: {
    tt,
    isUpgrading,
    openUpgrade,
    fmtMB(mb) {
      if (!mb || mb <= 0) return "0 MB";
      if (mb < 1024) return `${Math.round(mb)} MB`;
      return `${(mb / 1024).toFixed(1)} GB`;
    },
    stateTag(state) {
      return state === "running" ? "success" : state === "paused" ? "warning" : "info";
    },
    stateLabel(state) {
      return state === "running" ? tt("containers.up") : state === "paused" ? tt("containers.paused") : tt("containers.stopped");
    },
    // Fills the visitor's own IP, location, and LAN address. The result cache
    // is refreshed asynchronously and the UI updates silently when it lands.
    async probeClientIP() {
      let result;
      try {
        result = await detectClientIP(4000);
      } catch {
        result = { public: [], lan: [], geo: null, background: Promise.resolve(null) };
      }
      this.applyClientIP(result);
      if (result.background) {
        result.background.then((fresh) => {
          if (fresh) this.applyClientIP(fresh);
        }).catch(() => {});
      }
    },
    applyClientIP(result) {
      const pub = result.public || [];
      const lan = result.lan || [];
      if (pub.length) this.clientIp = pub.join(", ");
      else if (!this.clientIp) this.clientIp = tt("dash.detectFailed");
      if (lan.length) this.clientLan = lan.join(", ");
      else if (!this.clientLan) this.clientLan = "—";
      // When the detection Worker answered it already carried geo; otherwise
      // fall back to the server's cached GeoIP proxy (ip-api is HTTP-only, so
      // the browser can't reach it on HTTPS pages).
      if (result.geo) {
        this.renderClientLocation(result.geo);
      } else if (pub.length) {
        api(`/api/geo?ip=${encodeURIComponent(pub[0])}`)
          .then((g) => this.renderClientLocation(g))
          .catch(() => { /* location is a nice-to-have */ });
      }
    },
    renderClientLocation(geo) {
      if (!geo || (!geo.city && !geo.country)) {
        this.clientLoc = "";
        return;
      }
      const flag = countryCodeFlag(geo.countryCode);
      const place = [geo.city, geo.country].filter(Boolean).join(", ") || "—";
      let html = `${flag} ${place}`;
      if (geo.isp) html += ` · ${geo.isp}`;
      if (geo.proxy || geo.hosting) html += ` <span class="badge-warn-text">[${tt("dash.vpnProxy")}]</span>`;
      this.clientLoc = html;
    },
    // Resolves the update check; `refresh` asks the server to bypass its cache
    // (the manual check-now button); the automatic pass uses the cache.
    async fillVersion(refresh) {
      if (!isAdmin()) return;
      if (refresh) this.rechecking = true;
      const url = refresh ? "/api/update/check?refresh=1" : "/api/update/check";
      const res = await api(url).catch(() => null);
      this.rechecking = false;
      if (!res) return;
      this.update = res;
      if (refresh) {
        import("element-plus").then(({ ElMessage }) => ElMessage.success(tt("dash.rechecked")));
      }
    },
    relTime(iso) {
      if (!iso) return "";
      const t = new Date(iso).getTime();
      if (isNaN(t)) return iso;
      const s = Math.round((Date.now() - t) / 1000);
      if (s < 60) return tt("dash.sAgo", { n: s });
      if (s < 3600) return tt("dash.minAgo", { n: Math.round(s / 60) });
      if (s < 86400) return tt("dash.hAgo", { n: Math.round(s / 3600) });
      return tt("dash.dAgo", { n: Math.round(s / 86400) });
    },
  },
};
</script>

<style scoped>
.dash-tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 16px; }
.dash-row-2, .dash-row-3 { display: grid; grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr); gap: 16px; }
.dash-stack > * + * { margin-top: 16px; }

/* Stat tiles: tinted icon square + big number. */
.stat-tile { display: flex; gap: 13px; align-items: center; margin-bottom: 0; padding: 16px; }
.stat-icon {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
}
.tint-blue { background: #3370ff; }
.tint-green { background: var(--ok); }
.tint-purple { background: #8b5cf6; }
.tint-orange { background: var(--warn); }
.stat-value { font-size: 25px; font-weight: 750; line-height: 1.15; }
.stat-label { color: var(--muted); font-size: 12px; margin-top: 2px; }

/* Version banner: version + status left, actions right. */
.ver-banner { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.ver-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.ver-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.ver-current { font-size: 15px; font-weight: 700; }
.ver-actions { display: flex; gap: 8px; flex-shrink: 0; }
.dl-links { font-size: 12px; color: var(--muted); }
.dl-links a { color: var(--brand); }

/* Environment card: responsive label-over-value grid. */
.env-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px 18px; }
.env-item { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.env-item span { font-size: 11.5px; color: var(--muted); }
.env-item b { font-size: 13px; font-weight: 600; word-break: break-word; }
.env-item-wide { grid-column: 1 / -1; }

.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.card-head h2 { margin: 0; font-size: 14px; }
.tag-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
dl.detail { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; font-size: 13px; }
dl.detail dt { color: var(--muted); }
dl.detail dd { margin: 0; word-break: break-word; }
.chart-row { display: flex; align-items: center; gap: 18px; }
.legend { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; font-size: 13px; }
.legend .swatch { display: inline-block; width: 10px; height: 10px; border-radius: 3px; margin-right: 8px; }
.kv { display: flex; justify-content: space-between; gap: 12px; font-size: 13px; }
.kv span:first-child { color: var(--muted); }
.bar { height: 6px; background: var(--line); border-radius: 3px; overflow: hidden; margin: 8px 0; }
.bar-fill { height: 100%; background: var(--brand); transition: width 0.3s; }
.kv-row { display: flex; gap: 18px; margin-top: 8px; }
.kv-row .kv { flex: 1; flex-direction: column; gap: 2px; }
.client-ip-block { border-top: 1px solid var(--line); margin-top: 14px; padding-top: 10px; display: flex; flex-direction: column; gap: 8px; }
.client-ip-main { display: flex; flex-direction: column; align-items: flex-end; gap: 2px; text-align: right; }
.client-loc { font-size: 12px; }
.badge-warn-text { color: var(--warn); font-size: 11px; }

/* Compact list rows (my containers, recent activity) with hairlines. */
.mini-list { list-style: none; margin: 0; padding: 0; font-size: 12.5px; }
.mini-list li { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 8px 0; }
.mini-list li + li { border-top: 1px solid var(--line); }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); }
.audit-act { color: var(--brand); }
.audit-target { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 320px; }
.audit-time { margin-left: auto; color: var(--muted); white-space: nowrap; }
.feishu-header { display: flex; gap: 14px; align-items: center; margin-bottom: 12px; }
.feishu-avatar {
  width: 46px;
  height: 46px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--el-color-primary-light-9);
  color: var(--brand);
  font-size: 18px;
  font-weight: 650;
  display: flex;
  align-items: center;
  justify-content: center;
}
.feishu-name { font-weight: 650; }
.feishu-handle { color: var(--muted); font-size: 12.5px; }
@media (max-width: 1100px) {
  .dash-row-2, .dash-row-3 { grid-template-columns: minmax(0, 1fr); }
}
</style>
