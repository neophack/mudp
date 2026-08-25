<template>
  <div class="sse-progress">
    <div v-if="state.error" class="error-box">✗ {{ state.error }}</div>
    <div v-else class="step active">
      <el-icon class="is-loading"><Loading /></el-icon>
      <span class="step-label">{{ state.label }}</span>
    </div>
    <pre ref="log" class="log-output">{{ state.logs }}</pre>
    <div class="foot-actions">
      <el-button v-if="state.error" type="primary" size="small" @click="$emit('retry')">{{ tt("common.retry") }}</el-button>
      <el-button size="small" @click="$emit('hide')">{{ state.error ? tt("common.close") : tt("common.hide") }}</el-button>
    </div>
  </div>
</template>

<script>
import { tt } from "@/i18n";

// Shared SSE-job progress panel: running label, streaming log, error box with
// retry. The parent owns the { active, label, logs, error } state object and
// the streaming logic.
export default {
  name: "SseProgress",
  props: {
    state: { type: Object, required: true },
  },
  watch: {
    "state.logs"() {
      this.$nextTick(() => {
        if (this.$refs.log) this.$refs.log.scrollTop = this.$refs.log.scrollHeight;
      });
    },
  },
  methods: {
    tt,
  },
};
</script>

<style scoped>
.step { display: flex; gap: 10px; align-items: center; font-size: 13px; margin-bottom: 10px; }
.error-box { background: var(--danger-bg); color: var(--danger-text); border: 1px solid var(--danger-line); border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; }
.log-output {
  background: #0f172a;
  color: #cbd5e1;
  border-radius: 8px;
  padding: 10px;
  font-size: 12px;
  height: 260px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0 0 10px;
}
.foot-actions { display: flex; gap: 8px; justify-content: flex-end; }
</style>
