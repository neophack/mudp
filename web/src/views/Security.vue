<template>
  <div>
    <el-radio-group v-model="tab" size="small" style="margin-bottom: 16px" @change="onTab">
      <el-radio-button value="overview">{{ tt("security.tabOverview") }}</el-radio-button>
      <el-radio-button value="logs">{{ tt("security.tabLogs") }}</el-radio-button>
      <el-radio-button value="settings">{{ tt("security.tabSettings") }}</el-radio-button>
      <el-radio-button value="mcp">{{ tt("mcp.tabMcp") }}</el-radio-button>
    </el-radio-group>

    <!-- Overview: stat tiles + ECharts access map + top lists -->
    <template v-if="tab === 'overview'">
      <div class="sec-stat-row">
        <section class="card stat-tile">
          <div class="stat-icon">🌐</div>
          <div class="stat-body"><div class="stat-value">{{ stats.totalVisits ?? 0 }}</div><div class="stat-label">{{ tt("security.totalVisits") }}</div></div>
        </section>
        <section class="card stat-tile">
          <div class="stat-icon">✓</div>
          <div class="stat-body"><div class="stat-value">{{ stats.loginSuccess ?? 0 }}</div><div class="stat-label">{{ tt("security.successLogins") }}</div></div>
        </section>
        <section class="card stat-tile">
          <div class="stat-icon">✗</div>
          <div class="stat-body"><div class="stat-value">{{ stats.loginFailed ?? 0 }}</div><div class="stat-label">{{ tt("security.failedAttempts") }}</div></div>
        </section>
        <section class="card stat-tile">
          <div class="stat-icon">🛡</div>
          <div class="stat-body"><div class="stat-value">{{ stats.vpnProxy ?? 0 }}</div><div class="stat-label">{{ tt("security.vpnProxy") }}</div></div>
        </section>
        <section class="card stat-tile">
          <div class="stat-icon">⚠</div>
          <div class="stat-body"><div class="stat-value">{{ stats.suspicious ?? 0 }}</div><div class="stat-label">{{ tt("security.suspicious") }}</div></div>
        </section>
        <section class="card stat-tile">
          <div class="stat-icon">🔢</div>
          <div class="stat-body"><div class="stat-value">{{ stats.uniqueIPs ?? 0 }}</div><div class="stat-label">{{ tt("security.uniqueIPs") }}</div></div>
        </section>
      </div>

      <div class="card">
        <div class="card-head">
          <h2>{{ tt("security.accessMap") }}</h2>
          <span class="hint">{{ tt("security.accessMapHint") }}</span>
        </div>
        <world-map
          :points="points"
          mode="single"
          :country-data="countryData"
          :tooltip-html="accessTooltip"
          height="460px"
        />
      </div>

      <div class="sec-cols">
        <div class="card">
          <div class="card-head"><h2>{{ tt("security.topCountries") }}</h2></div>
          <ul class="top-list">
            <li v-for="(c, i) in stats.topCountries || []" :key="i">
              <span class="flag">{{ flag(c.label) }}</span><span class="lbl">{{ c.label }}</span><span class="cnt">{{ c.count }}</span>
            </li>
            <li v-if="!(stats.topCountries || []).length" class="hint">{{ tt("security.none") }}</li>
          </ul>
        </div>
        <div class="card">
          <div class="card-head"><h2>{{ tt("security.topIPs") }}</h2></div>
          <ul class="top-list">
            <li v-for="(c, i) in stats.topIPs || []" :key="i">
              <span class="lbl mono">{{ c.label }}</span><span class="cnt">{{ c.count }}</span>
            </li>
            <li v-if="!(stats.topIPs || []).length" class="hint">{{ tt("security.none") }}</li>
          </ul>
        </div>
      </div>
    </template>

    <!-- Logs -->
    <div v-else-if="tab === 'logs'" class="card">
      <div class="card-head">
        <h2>{{ tt("security.logsTitle") }}</h2>
        <div class="filters">
          <el-select v-model="filter.event" clearable size="small" style="width: 150px" :placeholder="tt('security.allEvents')" @change="applyFilters">
            <el-option value="page_view" :label="tt('security.eventPageView')" />
            <el-option value="login_success" :label="tt('security.eventLoginSuccess')" />
            <el-option value="login_failed" :label="tt('security.eventLoginFailed')" />
          </el-select>
          <el-input v-model="filter.ip" clearable size="small" style="width: 140px" :placeholder="tt('security.filterIP')" @input="debouncedFilters" />
          <el-input v-model="filter.username" clearable size="small" style="width: 120px" :placeholder="tt('security.filterUser')" @input="debouncedFilters" />
          <el-input v-model="filter.q" clearable size="small" style="width: 140px" :placeholder="tt('security.search')" @input="debouncedFilters" />
          <el-checkbox v-model="filter.suspicious" @change="applyFilters">{{ tt("security.suspiciousOnly") }}</el-checkbox>
          <el-button size="small" @click="exportCsv">{{ tt("security.exportCsv") }}</el-button>
        </div>
      </div>
      <el-table :data="logs" size="small" :empty-text="tt('security.noLogs')">
        <el-table-column :label="tt('security.colTime')" width="160">
          <template #default="{ row }"><span class="secondary-line">{{ fmtTime(row.createdAt) }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('security.colEvent')" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="row.event === 'login_success' ? 'success' : row.event === 'login_failed' ? 'warning' : 'info'">
              {{ eventLabel(row.event) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="tt('security.colLocation')" min-width="150">
          <template #default="{ row }">
            <span class="flag">{{ flag(row.countryCode) }}</span> {{ locOf(row) }}
            <div v-if="row.isp" class="secondary-line mono">{{ row.isp }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('security.colIP')" min-width="130">
          <template #default="{ row }">
            <span class="mono">{{ row.ip || "—" }}</span>
            <div v-if="row.publicIP" class="secondary-line mono">{{ tt("security.publicIpShort") }} {{ row.publicIP }}</div>
            <div v-if="row.clientTimezone" class="secondary-line">{{ row.clientTimezone }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('security.colDevice')" min-width="150">
          <template #default="{ row }">
            <div>{{ [row.browser, row.os, row.deviceType].filter(Boolean).join(" · ") || "—" }}</div>
            <div v-if="[row.clientScreen, row.clientPlatform].filter(Boolean).length" class="secondary-line">{{ [row.clientScreen, row.clientPlatform].filter(Boolean).join(" · ") }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('security.colUser')" width="120">
          <template #default="{ row }">
            <div class="primary-line">{{ displayNameForUsername(row.username) || "—" }}</div>
            <div v-if="row.failureReason" class="secondary-line">{{ row.failureReason }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('security.colFlags')" width="120">
          <template #default="{ row }">
            <el-tag v-if="row.isProxy || row.isHosting" size="small" type="warning">{{ row.proxyType || "vpn" }}</el-tag>
            <el-tag v-if="row.suspicious" size="small">{{ row.suspicious }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
      <div class="sec-pager">
        <span class="hint">{{ pagerText }}</span>
        <el-button size="small" :disabled="page === 0" @click="page--; loadLogs()">‹</el-button>
        <el-button size="small" :disabled="!hasMore" @click="page++; loadLogs()">›</el-button>
      </div>
    </div>

    <!-- Settings -->
    <div v-else-if="tab === 'settings'" class="card">
      <div class="card-head"><h2>{{ tt("security.settingsTitle") }}</h2></div>
      <el-switch v-model="settings.enabled" active-color="#3370ff" /> {{ tt("security.settingEnabled") }}
      <p class="hint">{{ tt("security.settingEnabledHint") }}</p>
      <el-switch v-model="settings.geoipLookup" active-color="#3370ff" /> {{ tt("security.settingGeo") }}
      <p class="hint">{{ tt("security.settingGeoHint") }}</p>
      <el-switch v-model="settings.vpnDetect" active-color="#3370ff" /> {{ tt("security.settingVpn") }}
      <p class="hint">{{ tt("security.settingVpnHint") }}</p>
      <el-switch v-model="settings.collectClient" active-color="#3370ff" /> {{ tt("security.settingClient") }}
      <p class="hint">{{ tt("security.settingClientHint") }}</p>
      <div class="sec-field">
        <label>{{ tt("security.settingRetention") }}</label>
        <div class="sec-retention">
          <el-input v-model="settings.retentionDays" type="number" size="small" style="width: 120px" />
          <span class="hint">{{ tt("security.days") }}</span>
        </div>
      </div>
      <p class="hint">{{ tt("security.settingNote") }}</p>

      <!-- The Worker only exposes an unauthenticated /whoami (visitor IP+geo
           from request.cf), so configuring it is just the URL. -->
      <h3 class="sec-subhead">{{ tt("security.ipWorkerTitle") }}</h3>
      <div class="sec-field">
        <label>{{ tt("security.ipWorkerUrl") }}</label>
        <p class="hint">{{ tt("security.ipWorkerUrlHint") }}</p>
        <el-input v-model="settings.ipWorkerUrl" placeholder="https://your-worker.workers.dev" size="small" style="max-width: 420px" />
      </div>

      <!-- The Worker source is secret-free, safe to view/copy into Cloudflare. -->
      <div class="sec-deploy">
        <div class="sec-deploy-head">
          <span class="hint">{{ tt("security.ipWorkerDeployHint") }}</span>
          <el-button size="small" @click="toggleWorkerSource">{{ tt("security.ipWorkerViewCode") }}</el-button>
        </div>
        <pre v-if="workerSource !== null" class="sec-deploy-code">{{ workerSource }}</pre>
        <div v-if="workerSource !== null" class="sec-deploy-actions">
          <el-button size="small" @click="copyWorkerSource">{{ tt("security.ipWorkerCopyCode") }}</el-button>
        </div>
      </div>

      <div class="sec-verify">
        <div class="sec-verify-head">
          <h3 class="sec-subhead">{{ tt("security.ipWorkerVerifyTitle") }}</h3>
          <el-button type="primary" size="small" @click="verifyAll">{{ tt("security.ipWorkerVerifyAll") }}</el-button>
        </div>
        <div class="sec-verify-row">
          <div class="sec-verify-label">
            <strong>{{ tt("security.ipWorkerVerifyCfg") }}</strong>
            <div class="hint">{{ tt("security.ipWorkerVerifyCfgHint") }}</div>
          </div>
          <div class="sec-verify-side">
            <span class="sec-verify-result" :class="verify.cfg.state">{{ verifyMark(verify.cfg) }}</span>
            <el-button size="small" @click="runVerifyConfig">{{ tt("security.ipWorkerVerifyRun") }}</el-button>
          </div>
        </div>
        <div class="sec-verify-row">
          <div class="sec-verify-label">
            <strong>{{ tt("security.ipWorkerVerifyWho") }}</strong>
            <div class="hint">{{ tt("security.ipWorkerVerifyWhoHint") }}</div>
          </div>
          <div class="sec-verify-side">
            <span class="sec-verify-result" :class="verify.who.state">{{ verifyMark(verify.who) }}</span>
            <el-button size="small" @click="runVerifyWhoami">{{ tt("security.ipWorkerVerifyRun") }}</el-button>
          </div>
        </div>
      </div>

      <el-button type="primary" style="margin-top: 14px" @click="saveSettings">{{ tt("common.save") }}</el-button>
    </div>

    <!-- MCP external-port attacks -->
    <template v-else-if="tab === 'mcp'">
      <div class="stack">
        <section class="card">
          <div class="card-head">
            <div>
              <h2>{{ tt("mcp.securityTitle") }}</h2>
              <p class="hint">{{ tt("mcp.securityDesc") }}</p>
            </div>
          </div>
          <div class="stat-cards">
            <div class="stat-card"><div class="stat-value">{{ attackStats.totalAttacks }}</div><div class="stat-label">{{ tt("mcp.totalAttacks") }}</div></div>
            <div class="stat-card"><div class="stat-value">{{ attackStats.uniqueIPs }}</div><div class="stat-label">{{ tt("mcp.uniqueIPs") }}</div></div>
            <div class="stat-card"><div class="stat-value">{{ attackStats.last24h }}</div><div class="stat-label">{{ tt("mcp.last24h") }}</div></div>
          </div>
          <div v-if="(attackStats.topCountries || []).length || (attackStats.topIPs || []).length" class="mcp-top-buckets">
            <div v-if="(attackStats.topCountries || []).length">
              <div class="secondary-line">{{ tt("mcp.topCountries") }}</div>
              <div class="chip-row">
                <span v-for="(c, i) in attackStats.topCountries" :key="'c' + i" class="chip">{{ c.label }} <b>{{ c.count }}</b></span>
              </div>
            </div>
            <div v-if="(attackStats.topIPs || []).length">
              <div class="secondary-line">{{ tt("mcp.topIPs") }}</div>
              <div class="chip-row">
                <span v-for="(c, i) in attackStats.topIPs" :key="'i' + i" class="chip mono">{{ c.label }} <b>{{ c.count }}</b></span>
              </div>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-head">
            <div><h2>{{ tt("mcp.mapTitle") }}</h2><p class="hint">{{ tt("mcp.mapDesc") }}</p></div>
            <div class="mcp-map-legend">
              <span class="mcp-legend-item"><i class="mcp-dot mcp-dot-green"></i>{{ tt("mcp.legendAccess") }}</span>
              <span class="mcp-legend-item"><i class="mcp-dot mcp-dot-yellow"></i>{{ tt("mcp.legendAttackLow") }}</span>
              <span class="mcp-legend-item"><i class="mcp-dot mcp-dot-red"></i>{{ tt("mcp.legendAttackHigh") }}</span>
            </div>
          </div>
          <world-map :points="mapPoints" mode="mcp" :tooltip-html="attackTooltip" height="460px" />
        </section>

        <section class="card">
          <div class="card-head">
            <div><h2>{{ tt("mcp.attackList") }}</h2><p class="hint">{{ tt("mcp.geoFromCloudflare") }}</p></div>
            <div class="filters">
              <el-input v-model="attackSearch.ip" clearable size="small" style="width: 140px" :placeholder="tt('mcp.filterIP')" @input="debouncedAttacks" />
              <el-input v-model="attackSearch.q" clearable size="small" style="width: 160px" :placeholder="tt('mcp.filterKeyword')" @input="debouncedAttacks" />
              <el-button size="small" @click="exportAttacks">{{ tt("mcp.exportAttacks") }}</el-button>
            </div>
          </div>
          <el-table :data="attacks" size="small" :empty-text="tt('mcp.noAttacks')">
            <el-table-column :label="tt('mcp.colAttackTime')" width="150">
              <template #default="{ row }"><span class="secondary-line">{{ formatDate(row.createdAt) }}</span></template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colSource')" width="90">
              <template #default="{ row }">
                <el-tag v-if="row.sourceKind === 'extranet'" size="small">{{ tt("mcp.sourceExtranet") }}</el-tag>
                <el-tag v-else-if="row.sourceKind === 'intranet'" size="small" type="warning">{{ tt("mcp.sourceIntranet") }}</el-tag>
                <span v-else class="secondary-line">—</span>
              </template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colIP')" width="130">
              <template #default="{ row }"><span class="mono">{{ row.ip || "—" }}</span></template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colLocation')" min-width="140">
              <template #default="{ row }">
                <span v-if="row.countryCode || row.city || row.region || row.country">
                  <span class="flag">{{ flag(row.countryCode) }}</span>
                  <span class="primary-line">{{ [row.city, row.region].filter(Boolean).join(", ") || row.country || "" }}</span>
                  <div v-if="row.countryCode" class="secondary-line">{{ row.countryCode }}</div>
                </span>
                <span v-else class="secondary-line">—</span>
              </template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colIsp')" min-width="110">
              <template #default="{ row }"><span class="secondary-line">{{ row.isp || "—" }}</span></template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colTimezone')" min-width="120">
              <template #default="{ row }"><span class="secondary-line">{{ row.timezone || "—" }}</span></template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colDevice')" width="100">
              <template #default="{ row }">
                <el-tag v-if="row.device === 'desktop'" size="small">🖥 {{ tt("mcp.deviceDesktop") }}</el-tag>
                <el-tag v-else-if="row.device === 'mobile'" size="small">📱 {{ tt("mcp.deviceMobile") }}</el-tag>
                <el-tag v-else-if="row.device === 'tablet'" size="small">🔡 {{ tt("mcp.deviceTablet") }}</el-tag>
                <el-tag v-else-if="row.device === 'bot'" size="small" type="warning">🤖 {{ tt("mcp.deviceBot") }}</el-tag>
                <span v-else class="secondary-line">—</span>
              </template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colReason')" min-width="120">
              <template #default="{ row }"><span class="secondary-line">{{ row.reason || "—" }}</span></template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colPath')" min-width="110">
              <template #default="{ row }"><span class="secondary-line mono">{{ row.path || "—" }}</span></template>
            </el-table-column>
            <el-table-column :label="tt('mcp.colUA')" min-width="150">
              <template #default="{ row }"><span class="secondary-line">{{ uaSummary(row) }}</span></template>
            </el-table-column>
          </el-table>
        </section>
      </div>
    </template>
  </div>
</template>

<script>
import { ElMessage } from "element-plus";
import { api, copyText } from "@/api";
import { store, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import { formatDate } from "@/lib/common.js";
import WorldMap from "@/components/WorldMap.vue";

function countryCodeFlag(code) {
  if (!code || code.length !== 2) return "🌍";
  const A = 0x1f1e6;
  const base = "A".charCodeAt(0);
  const cc = code.toUpperCase();
  return String.fromCodePoint(A + cc.charCodeAt(0) - base, A + cc.charCodeAt(1) - base);
}

function emptyStats() {
  return { totalAttacks: 0, uniqueIPs: 0, last24h: 0, topCountries: [], topIPs: [], hourlyTrend: [] };
}

export default {
  name: "Security",
  components: { WorldMap },
  data() {
    return {
      s: store,
      tab: "overview",
      stats: {},
      points: [],
      logs: [],
      filter: { event: "", ip: "", username: "", q: "", suspicious: false },
      page: 0,
      pageSize: 25,
      hasMore: false,
      settings: { enabled: true, geoipLookup: true, vpnDetect: true, collectClient: true, retentionDays: 90, ipWorkerUrl: "" },
      workerSource: null,
      verify: { cfg: { state: "", detail: "" }, who: { state: "", detail: "" } },
      attacks: [],
      attackStats: emptyStats(),
      mapPoints: [],
      attackSearch: { ip: "", q: "" },
    };
  },
  computed: {
    // Choropleth input for the access map: top countries by visit count.
    countryData() {
      return (this.stats.topCountries || [])
        .filter((c) => c.label && c.label.length > 2)
        .map((c) => ({ name: c.label, value: c.count }));
    },
    pagerText() {
      const count = this.logs.length;
      if (!count) return tt("security.noLogs");
      const from = this.page * this.pageSize + 1;
      const to = this.page * this.pageSize + count;
      return `${from}-${to}`;
    },
  },
  async mounted() {
    this.onTab("overview");
  },
  methods: {
    tt,
    displayNameForUsername,
    formatDate,
    flag: countryCodeFlag,
    onTab(tab) {
      if (tab === "overview") this.loadOverview();
      else if (tab === "logs") this.loadLogs();
      else if (tab === "settings") this.loadSettings();
      else if (tab === "mcp") this.loadMcp();
    },
    async loadOverview() {
      try {
        const [stats, points] = await Promise.all([
          api("/api/admin/access/stats"),
          api("/api/admin/access/geo?limit=500"),
        ]);
        this.stats = stats || {};
        this.points = points || [];
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    accessTooltip(p) {
      return (
        `<strong>${p.label || p.city || tt("security.unknown")}</strong>` +
        `<br>${p.count} ${tt("security.visits")}` +
        (p.country ? `<br>${p.country}` : "")
      );
    },
    attackTooltip(p) {
      const kind = p.kind === "access" ? tt("mcp.legendAccess") : tt("mcp.legendAttack");
      return (
        `<strong>${p.label || p.city || tt("mcp.unknown")}</strong>` +
        `<br>${kind} · ${p.count} ${tt("mcp.events")}` +
        (p.country ? `<br>${p.country}` : "") +
        (p.ip ? `<br><span style="font-family:monospace">${p.ip}</span>` : "")
      );
    },
    eventLabel(event) {
      if (event === "login_success") return tt("security.eventLoginSuccess");
      if (event === "login_failed") return tt("security.eventLoginFailed");
      return tt("security.eventPageView");
    },
    locOf(e) {
      return [e.city, e.country].filter(Boolean).join(", ") || tt("security.unknown");
    },
    applyFilters() {
      this.page = 0;
      this.loadLogs();
    },
    debouncedFilters() {
      clearTimeout(this._filterTimer);
      this._filterTimer = setTimeout(() => this.applyFilters(), 300);
    },
    debouncedAttacks() {
      clearTimeout(this._attackTimer);
      this._attackTimer = setTimeout(() => this.reloadAttacks(), 300);
    },
    async loadLogs() {
      const f = this.filter;
      const params = new URLSearchParams();
      if (f.event) params.set("event", f.event);
      if (f.ip) params.set("ip", f.ip);
      if (f.username) params.set("username", f.username);
      if (f.q) params.set("q", f.q);
      if (f.suspicious) params.set("suspicious", "1");
      params.set("limit", this.pageSize);
      params.set("offset", this.page * this.pageSize);
      try {
        const data = await api("/api/admin/access/logs?" + params.toString());
        // The handler returns a flat array, so "more pages exist" is inferred
        // from whether this page came back full.
        this.logs = Array.isArray(data) ? data : [];
        this.hasMore = this.logs.length === this.pageSize;
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    exportCsv() {
      const f = this.filter;
      const params = new URLSearchParams();
      if (f.event) params.set("event", f.event);
      if (f.ip) params.set("ip", f.ip);
      if (f.username) params.set("username", f.username);
      if (f.q) params.set("q", f.q);
      if (f.suspicious) params.set("suspicious", "1");
      // Server-side CSV export handles quoting; just trigger a download.
      const a = document.createElement("a");
      a.href = "/api/admin/access/export?" + params.toString();
      a.download = `mudp-access-${new Date().toISOString().slice(0, 10)}.csv`;
      document.body.appendChild(a);
      a.click();
      a.remove();
    },
    async loadSettings() {
      try {
        const s = await api("/api/admin/security/settings");
        this.settings = {
          enabled: !!s.enabled,
          geoipLookup: !!s.geoipLookup,
          vpnDetect: !!s.vpnDetect,
          collectClient: !!s.collectClient,
          retentionDays: s.retentionDays ?? 90,
          ipWorkerUrl: s.ipWorkerUrl || "",
        };
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveSettings() {
      const s = {
        enabled: !!this.settings.enabled,
        geoipLookup: !!this.settings.geoipLookup,
        vpnDetect: !!this.settings.vpnDetect,
        collectClient: !!this.settings.collectClient,
        retentionDays: parseInt(this.settings.retentionDays, 10) || 90,
        ipWorkerUrl: (this.settings.ipWorkerUrl || "").trim(),
      };
      try {
        await api("/api/admin/security/settings", { method: "POST", body: JSON.stringify(s) });
        ElMessage.success(tt("security.saved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async toggleWorkerSource() {
      if (this.workerSource !== null) {
        this.workerSource = null;
        return;
      }
      try {
        const res = await fetch("/api/admin/security/worker-source", { credentials: "same-origin" });
        this.workerSource = res.ok ? await res.text() : "";
      } catch (err) {
        ElMessage.error(err.message || tt("common.copyFailed"));
      }
    },
    async copyWorkerSource() {
      const src = this.workerSource || "";
      if (!src) return;
      try {
        await copyText(src);
        ElMessage.success(tt("security.ipWorkerCopied"));
      } catch {
        ElMessage.error(tt("common.copyFailed"));
      }
    },
    verifyMark(v) {
      const mark = v.state === "ok" ? "✓ " : v.state === "fail" ? "✗ " : v.state === "pending" ? "… " : "";
      return mark + (v.detail || "");
    },
    // Checks the public config endpoint: does mudp know about a configured,
    // https Worker URL? Catches "forgot to save" and "http:// URL" before any
    // network call.
    async runVerifyConfig() {
      this.verify.cfg = { state: "pending", detail: tt("security.ipWorkerVerifyRunning") };
      try {
        const data = await api("/api/security/ipworker");
        if (data && data.enabled && data.url) {
          this.verify.cfg = { state: "ok", detail: data.url };
        } else {
          this.verify.cfg = { state: "fail", detail: tt("security.ipWorkerVerifyCfgFail") };
        }
      } catch (err) {
        this.verify.cfg = { state: "fail", detail: err.message };
      }
    },
    // Exercises the browser→Worker path directly, proving the Worker is
    // deployed, reachable, CORS-permits this origin, and returns valid geo.
    async runVerifyWhoami() {
      this.verify.who = { state: "pending", detail: tt("security.ipWorkerVerifyRunning") };
      try {
        const cfg = await api("/api/security/ipworker");
        if (!cfg || !cfg.enabled || !cfg.url) {
          this.verify.who = { state: "fail", detail: tt("security.ipWorkerVerifyCfgFail") };
          return;
        }
        const res = await fetch(cfg.url + "/whoami", { credentials: "omit", signal: AbortSignal.timeout(8000) });
        if (!res.ok) {
          this.verify.who = { state: "fail", detail: "HTTP " + res.status };
          return;
        }
        const data = await res.json();
        if (!data || data.status !== "success") {
          this.verify.who = { state: "fail", detail: (data && data.message) || "no success" };
          return;
        }
        this.verify.who = { state: "ok", detail: `${data.ip || "?"} · ${data.city || "?"}, ${data.country || "?"}` };
      } catch (err) {
        this.verify.who = { state: "fail", detail: err.message || String(err) };
      }
    },
    verifyAll() {
      return Promise.all([this.runVerifyConfig(), this.runVerifyWhoami()]);
    },
    async loadMcp() {
      try {
        const [attacks, stats, points] = await Promise.all([
          api("/api/admin/mcp/attacks?limit=200"),
          api("/api/admin/mcp/attacks/stats"),
          api("/api/admin/mcp/map?limit=500"),
        ]);
        this.attacks = attacks || [];
        this.attackStats = stats || emptyStats();
        this.mapPoints = points || [];
      } catch {
        this.attacks = [];
        this.attackStats = emptyStats();
        this.mapPoints = [];
      }
    },
    async reloadAttacks() {
      const params = new URLSearchParams({ limit: "200" });
      if (this.attackSearch.ip) params.set("ip", this.attackSearch.ip);
      if (this.attackSearch.q) params.set("q", this.attackSearch.q);
      try {
        this.attacks = (await api("/api/admin/mcp/attacks?" + params.toString())) || [];
      } catch (err) {
        ElMessage.error(err.message || tt("mcp.loadFail"));
      }
    },
    uaSummary(a) {
      const parts = [a.browser, a.os].filter(Boolean);
      if (parts.length) return parts.join(" · ");
      return a.userAgent ? a.userAgent.slice(0, 60) : "—";
    },
    exportAttacks() {
      const lines = ["time,source,ip,country,country_code,region,city,isp,timezone,device,reason,path,browser,os,user_agent"];
      for (const a of this.attacks) {
        lines.push(
          [a.createdAt, a.sourceKind, a.ip, a.country, a.countryCode, a.region, a.city, a.isp, a.timezone, a.device, a.reason, a.path, a.browser, a.os, a.userAgent]
            .map(this.csvCell)
            .join(","),
        );
      }
      const blob = new Blob([lines.join("\n")], { type: "text/csv" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `mcp-attacks-${new Date().toISOString().slice(0, 10)}.csv`;
      link.click();
      URL.revokeObjectURL(url);
    },
    csvCell(v) {
      const s = String(v ?? "");
      return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
    },
    fmtTime(iso) {
      if (!iso) return "—";
      const d = new Date(iso);
      return isNaN(d) ? iso : d.toLocaleString();
    },
  },
};
</script>

<style scoped>
.sec-stat-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; margin-bottom: 16px; }
.stat-tile { display: flex; gap: 12px; align-items: center; margin-bottom: 0; }
.stat-icon { font-size: 22px; }
.stat-value { font-size: 21px; font-weight: 750; line-height: 1.1; }
.stat-label { color: var(--muted); font-size: 12px; }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.card-head h2 { margin: 0; font-size: 14px; }
.sec-cols { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; margin-top: 16px; }
@media (max-width: 900px) { .sec-cols { grid-template-columns: minmax(0, 1fr); } }
.top-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; font-size: 13px; }
.top-list li { display: flex; align-items: center; gap: 10px; }
.top-list .lbl { flex: 1; }
.top-list .cnt { font-weight: 700; }
.filters { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.sec-pager { display: flex; align-items: center; gap: 10px; justify-content: flex-end; margin-top: 10px; }
.sec-field { margin: 14px 0; }
.sec-retention { display: flex; align-items: center; gap: 10px; margin-top: 6px; }
.sec-subhead { font-size: 13.5px; margin: 18px 0 8px; }
.sec-deploy { margin-top: 10px; }
.sec-deploy-head { display: flex; align-items: center; gap: 10px; }
.sec-deploy-code { background: #0f172a; color: #cbd5e1; border-radius: 8px; padding: 10px; font-size: 11.5px; max-height: 220px; overflow: auto; margin: 8px 0; }
.sec-deploy-actions { display: flex; justify-content: flex-end; }
.sec-verify { border-top: 1px dashed var(--line); padding-top: 8px; }
.sec-verify-head { display: flex; align-items: center; justify-content: space-between; }
.sec-verify-row { display: flex; align-items: center; gap: 14px; padding: 8px 0; border-bottom: 1px solid var(--line); }
.sec-verify-label { flex: 1; }
.sec-verify-side { display: flex; align-items: center; gap: 10px; }
.sec-verify-result { font-size: 12px; min-width: 160px; text-align: right; }
.sec-verify-result.ok { color: var(--ok); }
.sec-verify-result.fail { color: var(--danger); }
.sec-verify-result.pending { color: var(--muted); }
.stack > * + * { margin-top: 16px; }
.stat-cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; }
.stat-cards .stat-card { border: 1px solid var(--line); border-radius: 10px; padding: 12px; }
.stat-cards .stat-value { font-size: 22px; font-weight: 750; }
.stat-cards .stat-label { color: var(--muted); font-size: 12px; margin-top: 2px; }
.mcp-top-buckets { display: flex; gap: 24px; margin-top: 12px; flex-wrap: wrap; }
.chip-row { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 6px; }
.chip { background: var(--fill); border: 1px solid var(--line); border-radius: 12px; padding: 2px 10px; font-size: 12px; }
.mcp-map-legend { display: flex; gap: 12px; margin-left: auto; font-size: 12px; color: var(--muted); }
.mcp-legend-item { display: inline-flex; align-items: center; gap: 6px; }
.mcp-dot { width: 9px; height: 9px; border-radius: 50%; display: inline-block; }
.mcp-dot-green { background: #22c55e; }
.mcp-dot-yellow { background: #f59e0b; }
.mcp-dot-red { background: #ef4444; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.flag { margin-right: 4px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
</style>
