<template>
  <div v-if="s" v-show="s.visible" class="upload-overlay">
    <div class="upload-head">
      <div class="upload-title">{{ s.label }}</div>
      <button type="button" class="upload-close" aria-label="Close" :title="tt('common.close')" @click="s.visible = false">&times;</button>
    </div>
    <div class="upload-bar"><div class="bar-fill" :style="{ transform: 'scaleX(' + s.overall.percent / 100 + ')' }"></div></div>
    <div class="upload-meta">
      {{ fmtBytes(s.overall.loaded) }} / {{ fmtBytes(s.overall.bytesTotal) }} · {{ fmtSpeed(s.overall.speedBps) }} · {{ Math.round(s.overall.percent) }}%
      <template v-if="s.overall.etaSec > 0 && s.overall.percent < 100"> · {{ fmtEta(s.overall.etaSec) }} left</template>
    </div>
    <div class="upload-counts">
      {{ s.overall.done }} / {{ s.overall.total }} · {{ s.overall.done }} done · {{ s.overall.failed }} failed
    </div>
    <div class="upload-file-list">
      <div v-for="row in s.active" :key="row.id" class="upload-file-row is-uploading">
        <div class="upload-file-head">
          <span class="upload-file-name" :title="row.name">{{ row.name }}</span>
          <span class="upload-file-size">{{ fmtBytes(row.size) }}</span>
        </div>
        <div class="upload-file-bar"><div class="bar-fill" :style="{ transform: 'scaleX(' + row.percent / 100 + ')' }"></div></div>
        <div class="upload-file-meta">
          <span class="upload-file-status">{{ fmtBytes(row.loaded) }} · {{ row.percent }}% · {{ fmtSpeed(row.speedBps) }}</span>
        </div>
      </div>
      <div v-if="s.overflowDone > 0" class="upload-overflow">… +{{ s.overflowDone }} more</div>
      <div v-for="row in s.settled" :key="'s' + row.id" class="upload-file-row" :class="row.status === 'error' ? 'is-error' : 'is-done'">
        <div class="upload-file-head">
          <span class="upload-file-name" :title="row.name">{{ row.name }}</span>
          <span class="upload-file-size">{{ fmtBytes(row.size) }}</span>
        </div>
        <div class="upload-file-bar"><div class="bar-fill" :style="{ transform: 'scaleX(' + (row.status === 'error' ? row.percent : 100) / 100 + ')' }"></div></div>
        <div class="upload-file-meta">
          <span class="upload-file-status">{{ row.status === "error" ? row.msg : tt("common.done") }}</span>
          <button v-if="row.status === 'error' && row.retry" type="button" class="upload-file-retry" @click.stop="row.retry()">{{ tt("common.retry") }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { getUploadOverlayState, fmtSpeed } from "@/uploadOverlay";
import { fmtBytes } from "@/lib/common.js";
import { tt } from "@/i18n";

function fmtEta(sec) {
  if (!Number.isFinite(sec) || sec <= 0) return "";
  if (sec < 60) return `${Math.round(sec)}s`;
  const m = Math.floor(sec / 60);
  const s = Math.round(sec % 60);
  return s ? `${m}m ${s}s` : `${m}m`;
}

export default {
  name: "UploadOverlay",
  computed: {
    // getUploadOverlayState swaps a whole object per upload batch; reading it
    // through the stable reactive host registers the dependency, so this
    // recomputes exactly when a batch starts or the overlay closes — no
    // polling timer.
    s() {
      return getUploadOverlayState();
    },
  },
  methods: {
    tt,
    fmtBytes,
    fmtSpeed,
    fmtEta,
  },
};
</script>

<style scoped>
.upload-overlay {
  position: fixed;
  right: 20px;
  bottom: 20px;
  width: 340px;
  max-height: 60vh;
  background: var(--card);
  color: var(--ink);
  border-radius: 10px;
  padding: 12px;
  z-index: 2100;
  box-shadow: var(--card-shadow, 0 10px 30px rgba(0, 0, 0, 0.2));
  display: flex;
  flex-direction: column;
}
.upload-head { display: flex; align-items: center; margin-bottom: 8px; }
.upload-title { font-size: 13px; font-weight: 600; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upload-close { background: none; border: none; color: var(--muted); font-size: 16px; cursor: pointer; line-height: 1; }
.upload-close:hover { color: var(--ink); }
.upload-bar { height: 4px; border-radius: 2px; background: var(--line); overflow: hidden; }
.upload-bar .bar-fill { height: 100%; background: var(--brand); transform-origin: 0 50%; transition: transform 0.16s var(--ease-out, ease-out); }
.upload-meta, .upload-counts { font-size: 11.5px; color: var(--muted); margin-top: 4px; }
.upload-file-list { margin-top: 8px; overflow-y: auto; max-height: 40vh; }
.upload-file-row { padding: 6px 0; border-top: 1px solid rgba(255, 255, 255, 0.08); }
.upload-file-head { display: flex; gap: 8px; font-size: 12px; }
.upload-file-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upload-file-size { color: var(--muted); }
.upload-file-bar { height: 3px; background: var(--line); border-radius: 2px; overflow: hidden; margin: 4px 0; }
.upload-file-bar .bar-fill { height: 100%; background: var(--brand); transform-origin: 0 50%; transition: transform 0.16s var(--ease-out, ease-out); }
.upload-file-row.is-done .upload-file-bar .bar-fill { background: var(--ok); }
.upload-file-row.is-error .upload-file-bar .bar-fill { background: var(--danger); }
.upload-file-meta { display: flex; align-items: center; gap: 8px; font-size: 11px; color: var(--muted); }
.upload-file-retry { margin-left: auto; background: none; border: 1px solid var(--line); color: var(--ink); border-radius: 4px; font-size: 10.5px; padding: 1px 8px; cursor: pointer; }
.upload-file-retry:hover { border-color: var(--brand); color: #fff; }
.upload-overflow { font-size: 11px; color: var(--muted); padding: 4px 0; }
</style>
