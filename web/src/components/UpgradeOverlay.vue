<template>
  <div v-if="o" class="upgrade-overlay">
    <div class="upgrade-panel">
      <div class="upgrade-ring" :class="{ 'is-spin': indeterminate, 'is-ok': o.done === 'ok', 'is-err': o.done === 'err' }" :style="ringStyle">
        <div class="upgrade-ring-hole"><span>{{ ringText }}</span></div>
      </div>
      <div class="upgrade-phase">{{ o.phaseKey }}</div>
      <div class="upgrade-detail hint">{{ o.detail }}</div>
      <div class="upgrade-actions">
        <el-button v-if="o.done === 'err'" @click="close">{{ tt("common.close") }}</el-button>
      </div>
    </div>
  </div>
</template>

<script>
import { upgradeState, closeUpgradeOverlay } from "@/upgrade";
import { tt } from "@/i18n";

export default {
  name: "UpgradeOverlay",
  computed: {
    o() {
      return upgradeState.overlay;
    },
    indeterminate() {
      return !this.o.done && (this.o.pct === null || this.o.pct === undefined || isNaN(this.o.pct));
    },
    ringStyle() {
      if (this.indeterminate || this.o.done) return {};
      const clamped = Math.max(0, Math.min(100, this.o.pct));
      return { background: `conic-gradient(var(--brand) ${clamped}%, #e2e8f0 0)` };
    },
    ringText() {
      if (this.o.done === "ok") return "✓";
      if (this.o.done === "err") return "✕";
      if (this.indeterminate) return "";
      return `${Math.max(0, Math.min(100, this.o.pct)).toFixed(0)}%`;
    },
  },
  methods: {
    tt,
    close() {
      closeUpgradeOverlay();
    },
  },
};
</script>

<style scoped>
.upgrade-overlay {
  position: fixed;
  inset: 0;
  background: rgba(2, 8, 23, 0.88);
  z-index: 3000;
  display: flex;
  align-items: center;
  justify-content: center;
}
.upgrade-panel {
  background: #0f172a;
  color: #e2e8f0;
  border-radius: 14px;
  padding: 36px 48px;
  text-align: center;
  box-shadow: 0 30px 80px rgba(0, 0, 0, 0.5);
}
.upgrade-ring {
  width: 110px;
  height: 110px;
  border-radius: 50%;
  margin: 0 auto 18px;
  position: relative;
  background: conic-gradient(#334155 0%, #334155 0%);
}
.upgrade-ring-hole {
  position: absolute;
  inset: 10px;
  border-radius: 50%;
  background: #0f172a;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  font-size: 18px;
}
.upgrade-ring.is-spin { animation: upgrade-spin 1s linear infinite; }
.upgrade-ring.is-ok { background: var(--ok); }
.upgrade-ring.is-err { background: var(--danger); }
@keyframes upgrade-spin { to { transform: rotate(360deg); } }
.upgrade-phase { font-size: 15px; font-weight: 600; }
.upgrade-detail { margin-top: 6px; color: #94a3b8; }
.upgrade-actions { margin-top: 18px; }
</style>
