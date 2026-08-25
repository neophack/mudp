<template>
  <!-- Dual-pane file explorer: container filesystem (left) ↔ user netdisk
       (right). Copies selected files/folders in either direction. Works on
       stopped containers because listing/download/upload use the Docker
       archive API (container writable layer), not exec. -->
  <el-dialog :model-value="visible" :title="tt('files.title', { name: title })" width="960px" top="4vh" append-to-body @update:model-value="onVisible">
    <div class="files-toolbar">
      <el-button type="primary" size="small" :title="tt('files.copyToNetdiskTitle')" :disabled="busy" @click="copy('to-netdisk')">{{ tt("files.copyToNetdisk") }}</el-button>
      <el-button type="primary" size="small" :title="tt('files.copyToContainerTitle')" :disabled="busy" @click="copy('to-container')">{{ tt("files.copyToContainer") }}</el-button>
      <span class="hint">{{ status }}</span>
    </div>
    <div class="files-panes">
      <section class="files-pane">
        <div class="pane-head">
          <h3>{{ tt("files.container") }}</h3>
          <code class="mono pane-path">{{ container.path }}</code>
          <el-button size="small" @click="goUp('container')">{{ tt("files.up") }}</el-button>
        </div>
        <el-table :data="sorted(container.items)" size="small" height="380" :empty-text="tt('files.emptyFolder')" @selection-change="(rows) => onSel('container', rows)">
          <el-table-column type="selection" width="36" />
          <el-table-column :label="tt('common.name')">
            <template #default="{ row }">
              <el-button v-if="row.dir" link class="file-name" @click="openDir('container', row.path)">{{ row.dir ? "📁" : "📄" }} {{ row.name }}</el-button>
              <span v-else>{{ row.dir ? "📁" : "📄" }} {{ row.name }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="tt('common.size')" width="90">
            <template #default="{ row }">{{ row.dir ? "—" : fmtBytes(row.size) }}</template>
          </el-table-column>
          <el-table-column :label="tt('files.colMode')" width="100">
            <template #default="{ row }"><span class="mono">{{ row.mode || "—" }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('files.colModified')" width="150">
            <template #default="{ row }">{{ fmtTime(row.modTime) }}</template>
          </el-table-column>
          <el-table-column width="50">
            <template #default="{ row }">
              <el-button link icon="Download" :title="tt('netdisk.download')" @click="downloadFile(row.path)" />
            </template>
          </el-table-column>
        </el-table>
      </section>
      <section class="files-pane">
        <div class="pane-head">
          <h3>{{ tt("files.netdisk") }}</h3>
          <code class="mono pane-path">/{{ netdisk.path }}</code>
          <el-button size="small" @click="goUp('netdisk')">{{ tt("files.up") }}</el-button>
        </div>
        <el-table :data="sorted(netdisk.items)" size="small" height="380" :empty-text="tt('files.emptyFolder')" @selection-change="(rows) => onSel('netdisk', rows)">
          <el-table-column type="selection" width="36" />
          <el-table-column :label="tt('common.name')">
            <template #default="{ row }">
              <el-button v-if="row.dir" link class="file-name" @click="openDir('netdisk', row.path)">{{ row.dir ? "📁" : "📄" }} {{ row.name }}</el-button>
              <span v-else>{{ row.dir ? "📁" : "📄" }} {{ row.name }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="tt('common.size')" width="90">
            <template #default="{ row }">{{ row.dir ? "—" : fmtBytes(row.size) }}</template>
          </el-table-column>
          <el-table-column :label="tt('files.colMode')" width="100">
            <template #default="{ row }"><span class="mono">{{ row.mode || "—" }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('files.colModified')" width="150">
            <template #default="{ row }">{{ fmtTime(row.modTime) }}</template>
          </el-table-column>
        </el-table>
      </section>
    </div>
  </el-dialog>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { tt } from "@/i18n";

export default {
  name: "FilesDialog",
  props: {
    visible: { type: Boolean, default: false },
    id: { type: String, default: "" },
    title: { type: String, default: "" },
  },
  data() {
    return {
      container: { path: "/", items: [], selected: [] },
      netdisk: { path: "", items: [], selected: [] },
      status: "",
      busy: false,
    };
  },
  watch: {
    visible(v) {
      if (v) {
        this.container = { path: "/", items: [], selected: [] };
        this.netdisk = { path: "", items: [], selected: [] };
        this.status = "";
        this.loadContainer();
        this.loadNetdisk();
      }
    },
  },
  methods: {
    tt,
    onVisible(v) {
      this.$emit("update:visible", v);
    },
    sorted(items) {
      return (items || []).slice().sort((a, b) =>
        a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1
      );
    },
    onSel(side, rows) {
      this[side].selected = rows.map((r) => r.path);
    },
    openDir(side, path) {
      if (side === "container") {
        this.container.path = path;
        this.loadContainer();
      } else {
        this.netdisk.path = path;
        this.loadNetdisk();
      }
    },
    goUp(side) {
      const p = this[side];
      const parts = (p.path || "").split("/").filter(Boolean);
      parts.pop();
      if (side === "container") {
        p.path = "/" + parts.join("/");
        this.loadContainer();
      } else {
        p.path = parts.join("/");
        this.loadNetdisk();
      }
    },
    async loadContainer() {
      try {
        const res = await api(`/api/containers/files/list?id=${encodeURIComponent(this.id)}&path=${encodeURIComponent(this.container.path)}`);
        this.container.path = res.path || this.container.path;
        this.container.items = res.items || [];
        this.status = "";
      } catch (err) {
        this.container.items = [];
        this.status = err.message;
      }
    },
    async loadNetdisk() {
      try {
        const res = await api(`/api/netdisk?path=${encodeURIComponent(this.netdisk.path)}`);
        this.netdisk.path = res.path || "";
        this.netdisk.items = res.items || [];
        this.status = "";
      } catch (err) {
        this.netdisk.items = [];
        this.status = err.message;
      }
    },
    downloadFile(path) {
      window.open(`/api/containers/files/download?id=${encodeURIComponent(this.id)}&path=${encodeURIComponent(path)}`, "_blank");
    },
    async copy(direction) {
      const fromContainer = direction === "to-netdisk";
      const from = fromContainer ? this.container : this.netdisk;
      const destDir = fromContainer
        ? String(this.netdisk.path || "").replace(/^\/+/, "")
        : (p => (p ? (p.startsWith("/") ? p : "/" + p) : "/"))(String(this.container.path || "").trim());
      const paths = [...from.selected];
      if (paths.length === 0) {
        ElMessage.info(tt("files.selectFirst"));
        return;
      }
      this.busy = true;
      this.status = fromContainer ? tt("files.copyingToNetdisk") : tt("files.copyingToContainer");
      try {
        const res = await api("/api/containers/files/copy", {
          method: "POST",
          body: JSON.stringify({ id: this.id, direction, paths, destDir }),
        });
        ElMessage.success(tt("files.copiedN", { n: res.copied ?? paths.length }));
        await Promise.all([this.loadContainer(), this.loadNetdisk()]);
        this.status = "";
      } catch (err) {
        ElMessage.error(err.message);
        this.status = err.message;
      } finally {
        this.busy = false;
      }
    },
    fmtBytes(n) {
      n = Number(n) || 0;
      if (n < 1024) return n + " B";
      if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
      if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + " MB";
      return (n / 1024 / 1024 / 1024).toFixed(2) + " GB";
    },
    fmtTime(ts) {
      if (!ts) return "—";
      const d = new Date(ts);
      if (Number.isNaN(d.getTime())) return "—";
      return d.toLocaleString();
    },
  },
};
</script>

<style scoped>
.files-toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.files-panes { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 14px; }
.pane-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.pane-head h3 { margin: 0; font-size: 13.5px; }
.pane-path { font-size: 12px; color: var(--muted); flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-name { padding: 0; }
@media (max-width: 900px) {
  .files-panes { grid-template-columns: minmax(0, 1fr); }
}
</style>
