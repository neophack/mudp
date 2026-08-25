<template>
  <el-dialog :model-value="dialog.visible" :title="tt('upgrade.title')" width="520px" append-to-body @update:model-value="!$event && close()">
    <template v-if="dialog.res">
      <div v-if="!upToDate" class="upgrade-hero">
        <div class="upgrade-hero-icon">⬇</div>
        <div class="upgrade-hero-title">
          {{ tt("upgrade.available") }} <span class="mono">{{ dialog.res.latest }}</span>
        </div>
        <div class="upgrade-hero-sub hint">
          {{ dialog.res.current }} → {{ dialog.res.latest }}<template v-if="released"> · {{ released }}</template>
        </div>
      </div>
      <div v-else class="upgrade-hero">
        <div class="upgrade-hero-icon">✓</div>
        <div class="upgrade-hero-title">{{ upToDateMsg }}</div>
      </div>
      <div v-if="!upToDate" class="upgrade-notes">
        <div class="upgrade-notes-title">{{ tt("upgrade.releaseNotes") }}</div>
        <ul v-if="notes.length">
          <li v-for="(line, i) in notes" :key="i">{{ line }}</li>
        </ul>
        <p v-else class="hint">{{ tt("upgrade.noNotes") }}</p>
      </div>
      <div v-if="!upToDate && downloads" class="upgrade-manual hint">
        {{ tt("upgrade.manualDownload") }}
        <a v-for="(url, key) in downloads" :key="key" :href="url" download>{{ key }}</a>
      </div>
    </template>
    <template #footer>
      <el-button @click="close()">{{ tt("upgrade.later") }}</el-button>
      <el-button v-if="!upToDate" type="primary" @click="go">{{ tt("upgrade.now") }}</el-button>
    </template>
  </el-dialog>
</template>

<script>
import { upgradeState, closeUpgrade, startUpgrade } from "@/upgrade";
import { tt } from "@/i18n";

const DOWNLOAD_LABELS = [
  ["windows", "win-x64"],
  ["windows-arm64", "win-arm64"],
  ["linux", "linux-x64"],
  ["linux-arm64", "linux-arm64"],
];

export default {
  name: "UpgradeDialog",
  computed: {
    dialog() {
      return upgradeState.dialog;
    },
    res() {
      return this.dialog.res;
    },
    upToDate() {
      const r = this.res;
      return !r || r.error || !r.available;
    },
    upToDateMsg() {
      const r = this.res;
      return r && r.error ? tt("dash.checkFailed") : `${tt("upgrade.upToDateTitle")} · ${r?.current || "dev"}`;
    },
    released() {
      const r = this.res;
      if (!r || !r.releasedAt || isNaN(new Date(r.releasedAt))) return "";
      return tt("upgrade.releasedOn", { time: new Date(r.releasedAt).toLocaleDateString() });
    },
    // GitHub release body → plain bullet list: markdown list markers and
    // headings stripped, one <li> per non-empty line.
    notes() {
      return String(this.res?.notes || "")
        .split("\n")
        .map((l) => l.trim().replace(/^[-*+]\s+/, "").replace(/^#+\s*/, ""))
        .filter(Boolean);
    },
    downloads() {
      const dl = this.res?.downloads;
      if (!dl) return null;
      const out = {};
      for (const [k, label] of DOWNLOAD_LABELS) {
        if (dl[k]) out[label] = dl[k];
      }
      return out;
    },
  },
  methods: {
    tt,
    close() {
      closeUpgrade();
    },
    go() {
      startUpgrade(this.res.latest);
    },
  },
};
</script>

<style scoped>
.upgrade-hero { text-align: center; padding: 6px 0 2px; }
.upgrade-hero-icon {
  width: 52px;
  height: 52px;
  margin: 0 auto 10px;
  border-radius: 50%;
  background: var(--brand-tint);
  box-shadow: inset 0 0 0 2px #bfdbfe;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 22px;
}
.upgrade-hero-title { font-size: 15px; font-weight: 650; }
.upgrade-hero-sub { margin-top: 4px; }
.upgrade-notes { margin-top: 14px; border-top: 1px solid var(--line); padding-top: 12px; }
.upgrade-notes-title { font-weight: 600; font-size: 13px; margin-bottom: 8px; }
.upgrade-notes ul { margin: 0; padding-left: 18px; color: var(--muted); font-size: 12.5px; max-height: 220px; overflow-y: auto; }
.upgrade-notes li { margin-bottom: 4px; }
.upgrade-manual { margin-top: 12px; border-top: 1px solid var(--line); padding-top: 10px; }
.upgrade-manual a { margin-left: 6px; }
</style>
