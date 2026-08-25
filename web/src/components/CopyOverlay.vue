<template>
  <div v-if="overlay.visible" class="copy-overlay">
    <div class="copy-head">
      <div class="copy-title">{{ overlay.title }}</div>
      <button type="button" class="copy-close" aria-label="Close" :title="tt('common.close')" @click="dismiss">&times;</button>
    </div>
    <div class="copy-bar"><div class="bar-fill" :style="{ width: pct + '%' }"></div></div>
    <div class="copy-meta">{{ meta }}</div>
  </div>
</template>

<script>
import { copyOverlay, dismissCopyOverlay } from "@/overlays";
import { fmtBytes } from "@/lib/common.js";
import { tt } from "@/i18n";

export default {
  name: "CopyOverlay",
  data() {
    return { overlay: copyOverlay };
  },
  computed: {
    pct() {
      return this.overlay.total > 0 ? Math.min(100, Math.round((this.overlay.done / this.overlay.total) * 100)) : 0;
    },
    meta() {
      const fmt = this.overlay.unit === "items" ? String : fmtBytes;
      const totStr = this.overlay.total > 0 ? fmt(this.overlay.total) : "…";
      const msg = this.overlay.message ? ` · ${this.overlay.message}` : "";
      return `${fmt(this.overlay.done)} / ${totStr} · ${this.pct}%${msg}`;
    },
  },
  methods: {
    tt,
    dismiss() {
      dismissCopyOverlay();
    },
  },
};
</script>
