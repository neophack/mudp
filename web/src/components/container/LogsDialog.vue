<template>
  <el-dialog :model-value="visible" :title="tt('logs.title', { name: title })" width="860px" top="4vh" append-to-body @update:model-value="onVisible">
    <div class="logs-toolbar">
      <el-input v-model="grep" :placeholder="tt('logs.filterPlaceholder')" clearable style="width: 160px" size="small" />
      <el-select v-model="tail" size="small" style="width: 110px" @change="fetchLogs">
        <el-option v-for="n in [100, 300, 1000, 5000]" :key="n" :value="n" :label="tt('logs.lastN', { n })" />
      </el-select>
      <el-checkbox v-model="follow" :title="tt('logs.followTitle')">{{ tt("logs.follow") }}</el-checkbox>
      <el-checkbox v-model="wrap" :title="tt('logs.wrapTitle')">{{ tt("logs.wrap") }}</el-checkbox>
      <el-button size="small" icon="Download" :title="tt('netdisk.download')" @click="download" />
      <el-button size="small" @click="fetchLogs">{{ tt("action.refresh") }}</el-button>
    </div>
    <pre ref="out" class="log-output" :class="{ nowrap: !wrap }">{{ filtered }}</pre>
  </el-dialog>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { tt } from "@/i18n";

export default {
  name: "LogsDialog",
  props: {
    visible: { type: Boolean, default: false },
    id: { type: String, default: "" },
    title: { type: String, default: "" },
  },
  data() {
    return {
      content: tt("common.loadingDots"),
      tail: 300,
      follow: false,
      grep: "",
      wrap: true,
      es: null,
    };
  },
  computed: {
    filtered() {
      if (!this.grep) return this.content;
      const needle = this.grep.toLowerCase();
      return this.content
        .split("\n")
        .filter((l) => l.toLowerCase().includes(needle))
        .join("\n");
    },
  },
  watch: {
    visible(v) {
      if (v) {
        this.content = "Loading…";
        this.fetchLogs();
      } else {
        this.stopFollow();
      }
    },
    follow(v) {
      if (v) this.startFollow();
      else this.stopFollow();
    },
  },
  beforeUnmount() {
    this.stopFollow();
  },
  methods: {
    tt,
    onVisible(v) {
      this.$emit("update:visible", v);
      if (!v) this.stopFollow();
    },
    async fetchLogs() {
      if (!this.visible) return;
      try {
        const data = await api("/api/containers/action", {
          method: "POST",
          body: JSON.stringify({ id: this.id, action: "logs", tail: this.tail }),
        });
        this.content = data.logs || tt("logs.noLogs");
        this.scrollDown();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    startFollow() {
      this.stopFollow();
      const url = `/api/containers/logs/stream?id=${encodeURIComponent(this.id)}&tail=${this.tail}`;
      this.es = new EventSource(url);
      this.es.addEventListener("log", (ev) => {
        try {
          const data = JSON.parse(ev.data);
          if (data.line) {
            this.content = (this.content === "Loading…" ? "" : this.content) + data.line;
            this.scrollDown();
          }
        } catch { /* malformed frame */ }
      });
      // Stream ended (container stopped or disconnected): leave existing logs.
      this.es.onerror = () => {};
    },
    stopFollow() {
      if (this.es) {
        this.es.close();
        this.es = null;
      }
    },
    scrollDown() {
      this.$nextTick(() => {
        if (this.$refs.out) this.$refs.out.scrollTop = this.$refs.out.scrollHeight;
      });
    },
    download() {
      const blob = new Blob([this.content || ""], { type: "text/plain" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${this.title}-logs.txt`;
      a.click();
      URL.revokeObjectURL(url);
    },
  },
};
</script>

<style scoped>
.logs-toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.log-output {
  background: #0f172a;
  color: #cbd5e1;
  border-radius: 8px;
  padding: 12px;
  font-size: 12px;
  line-height: 1.55;
  height: 55vh;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0;
}
.log-output.nowrap { white-space: pre; word-break: normal; }
</style>
