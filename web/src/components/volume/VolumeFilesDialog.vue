<template>
  <!-- Volume file browser: browse/upload/download/delete/rename files inside a
       mudp-managed volume via its host mountpoint (server-side). Single-pane. -->
  <el-dialog :model-value="visible" :title="tt('volfiles.title', { name: display })" width="860px" top="4vh" append-to-body @update:model-value="onVisible">
    <div class="files-toolbar">
      <el-button v-if="canMutate()" type="primary" size="small" :title="tt('volfiles.uploadTitle')" :disabled="busy" @click="pick">{{ tt("volfiles.upload") }}</el-button>
      <input ref="picker" type="file" multiple hidden @change="onPicked" />
      <el-button v-if="canMutate()" size="small" :title="tt('volfiles.newFolder')" :disabled="busy" @click="newFolder">📁 {{ tt("volfiles.newFolder") }}</el-button>
      <el-button v-if="canMutate()" size="small" type="danger" plain :title="tt('volfiles.deleteTitle')" :disabled="busy" @click="deleteSelected">✕ {{ tt("common.delete") }}</el-button>
      <el-button size="small" :disabled="busy" @click="goUp">{{ tt("volfiles.up") }}</el-button>
      <code class="mono pane-path">/{{ path }}</code>
      <span class="hint">{{ status }}</span>
    </div>
    <el-table :data="sortedItems" size="small" height="420" :empty-text="tt('volfiles.emptyFolder')" @selection-change="onSel">
      <el-table-column type="selection" width="36" />
      <el-table-column :label="tt('common.name')">
        <template #default="{ row }">
          <el-button v-if="row.dir" link class="file-name" @click="open(row.path)">{{ row.dir ? "📁" : "📄" }} {{ row.name }}</el-button>
          <span v-else>{{ row.dir ? "📁" : "📄" }} {{ row.name }}</span>
        </template>
      </el-table-column>
      <el-table-column :label="tt('common.size')" width="90">
        <template #default="{ row }">{{ row.dir ? "—" : fmtBytes(row.size) }}</template>
      </el-table-column>
      <el-table-column :label="tt('volfiles.colMode')" width="110">
        <template #default="{ row }"><span class="mono">{{ row.mode || "—" }}</span></template>
      </el-table-column>
      <el-table-column :label="tt('volfiles.colModified')" width="150">
        <template #default="{ row }">{{ fmtTime(row.modTime) }}</template>
      </el-table-column>
      <el-table-column width="90">
        <template #default="{ row }">
          <el-button link icon="Download" :title="tt('netdisk.download')" @click="download(row.path)" />
          <el-button v-if="canMutate()" link icon="Edit" :title="tt('netdisk.rename')" @click="rename(row)" />
        </template>
      </el-table-column>
    </el-table>
  </el-dialog>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api, readCSRFCookie } from "@/api";
import { store, canMutate } from "@/store";
import { tt } from "@/i18n";
import { uploadWithProgress } from "@/lib/upload.js";
import { hashFileCRC32 } from "@/lib/hashfile.js";
import { uploadLargeFile } from "@/lib/chunkupload.js";
import { showUploadOverlay } from "@/uploadOverlay";

// Files at or above this size use the chunked/resumable protocol (per-chunk
// CRC32, resume after a drop) instead of one multipart request.
const CHUNK_THRESHOLD = 1 << 30; // 1 GiB

export default {
  name: "VolumeFilesDialog",
  props: {
    visible: { type: Boolean, default: false },
    name: { type: String, default: "" },
    display: { type: String, default: "" },
  },
  data() {
    return {
      path: "",
      items: [],
      selected: [],
      status: "",
      busy: false,
    };
  },
  computed: {
    sortedItems() {
      return (this.items || []).slice().sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
    },
  },
  watch: {
    visible(v) {
      if (v) {
        this.path = "";
        this.selected = [];
        this.status = "";
        this.load();
      }
    },
  },
  methods: {
    tt,
    canMutate,
    onVisible(v) {
      this.$emit("update:visible", v);
    },
    onSel(rows) {
      this.selected = rows.map((r) => r.path);
    },
    open(path) {
      this.path = path;
      this.load();
    },
    goUp() {
      const parts = (this.path || "").split("/").filter(Boolean);
      parts.pop();
      this.path = parts.join("/");
      this.load();
    },
    download(path) {
      window.open(`/api/volumes/files/download?name=${encodeURIComponent(this.name)}&path=${encodeURIComponent(path)}`, "_blank");
    },
    async load() {
      try {
        const res = await api(`/api/volumes/files?name=${encodeURIComponent(this.name)}&path=${encodeURIComponent(this.path)}`);
        this.path = res.path || this.path;
        this.items = res.items || [];
        this.status = "";
      } catch (err) {
        this.items = [];
        this.status = err.message;
      }
    },
    async newFolder() {
      let input;
      try {
        ({ value: input } = await ElMessageBox.prompt(tt("netdisk.folderNamePrompt"), tt("volfiles.newFolder"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      if (!input || !input.trim()) return;
      const dir = (this.path ? this.path + "/" : "") + input.trim();
      try {
        await api("/api/volumes/files/mkdir", { method: "POST", body: JSON.stringify({ name: this.name, path: dir }) });
        ElMessage.success(tt("volfiles.folderCreated"));
        await this.load();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async deleteSelected() {
      const paths = [...this.selected];
      if (paths.length === 0) {
        ElMessage.info(tt("volfiles.selectFirst"));
        return;
      }
      try {
        await ElMessageBox.confirm(tt("volfiles.deleteConfirm", { n: paths.length }), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/volumes/files/delete", { method: "POST", body: JSON.stringify({ name: this.name, paths }) });
        ElMessage.success(tt("volfiles.deletedN", { n: paths.length }));
        await this.load();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async rename(item) {
      let input;
      try {
        ({ value: input } = await ElMessageBox.prompt(tt("volfiles.newName"), tt("netdisk.rename"), {
          inputValue: item.name,
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      if (!input || !input.trim() || input.trim() === item.name) return;
      try {
        await api("/api/volumes/files/rename", { method: "POST", body: JSON.stringify({ name: this.name, path: item.path, newName: input.trim() }) });
        ElMessage.success(tt("volfiles.renamed"));
        await this.load();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    pick() {
      this.$refs.picker.click();
    },
    onPicked(e) {
      this.doUpload(e.target.files);
    },
    // Files upload sequentially, one request per file. Large files go through
    // the chunked/resumable protocol; the overlay card is bounded so even a
    // huge pick never allocates a row per file.
    async doUpload(fileList) {
      const files = [...(fileList || [])];
      if (files.length === 0) return;
      this.status = tt("volfiles.uploading", { n: files.length });
      this.busy = true;
      let ok = 0;
      let failed = 0;
      const totalBytes = files.reduce((sum, f) => sum + (f.size || 0), 0);
      let baseBytes = 0;
      const overlay = showUploadOverlay();
      const batchStart = performance.now();
      try {
        for (let i = 0; i < files.length; i++) {
          const file = files[i];
          overlay.setLabel(`${i + 1}/${files.length}: ${file.name}`);
          if ((file.size || 0) >= CHUNK_THRESHOLD) {
            const slot = overlay.addActive({ name: file.name, size: file.size });
            const sendLarge = () => uploadLargeFile(file, file.name, {
              base: "/api/volumes/files", dir: this.path || "", volume: this.name,
              csrfToken: readCSRFCookie() || store.csrfToken || "",
              onProgress: ({ loaded, total }) => {
                overlay.updateActive(slot, {
                  loaded, total,
                  percent: total > 0 ? Math.min(100, Math.round((loaded / total) * 100)) : 0,
                  speedBps: 0,
                });
                const elapsed = (performance.now() - batchStart) / 1000;
                overlay.updateOverall({
                  done: ok, failed, total: files.length,
                  loaded: baseBytes + loaded, bytesTotal: totalBytes,
                  speedBps: elapsed > 0 ? loaded / elapsed : 0,
                  percent: totalBytes > 0 ? Math.min(100, Math.round(((baseBytes + loaded) / totalBytes) * 100)) : 0,
                });
              },
            });
            try {
              await sendLarge();
              overlay.settleActive(slot, "done");
              ok++;
            } catch (err) {
              failed++;
              overlay.markFailedWithRetry(slot, err.message, async () => {
                overlay.reactivate(slot, { name: file.name, size: file.size });
                failed = Math.max(0, failed - 1);
                await sendLarge().then(() => { overlay.settleActive(slot, "done"); ok++; });
                await this.load();
              });
            }
            baseBytes += file.size || 0;
            continue;
          }
          // Compute the file's CRC32 so the server can verify integrity.
          // Unhashable files yield "" and are still uploaded.
          const clientCrc32 = (await hashFileCRC32(file)) || "";
          const slot = overlay.addActive({ name: file.name, size: file.size });
          const sendOne = async () => {
            const fd = new FormData();
            fd.append("name", this.name);
            fd.append("path", this.path || "");
            fd.append("hashes", clientCrc32);
            fd.append("files", file, file.name);
            let resp;
            try {
              resp = await uploadWithProgress("/api/volumes/files/upload", fd, {
                csrfToken: readCSRFCookie() || store.csrfToken || "",
                onProgress: (p) => {
                  const fileTotal = file.size || 0;
                  const fileLoaded = p.total > 0 ? Math.min(fileTotal, Math.round(fileTotal * (p.loaded / p.total))) : 0;
                  overlay.updateActive(slot, {
                    loaded: fileLoaded,
                    total: fileTotal,
                    percent: fileTotal > 0 ? Math.min(100, Math.round((fileLoaded / fileTotal) * 100)) : 100,
                    speedBps: p.speedBps,
                  });
                  const loaded = baseBytes + fileLoaded;
                  const elapsed = (performance.now() - batchStart) / 1000;
                  overlay.updateOverall({
                    done: ok, failed, total: files.length, loaded, bytesTotal: totalBytes,
                    speedBps: elapsed > 0 ? loaded / elapsed : 0,
                    percent: totalBytes > 0 ? Math.min(100, Math.round((loaded / totalBytes) * 100)) : 0,
                  });
                },
              });
            } catch (err) {
              failed++;
              overlay.markFailedWithRetry(slot, err.message, async () => {
                overlay.reactivate(slot, { name: file.name, size: file.size });
                failed = Math.max(0, failed - 1);
                await sendOne();
                await this.load();
              });
              return;
            }
            // Delivered only if no error and (no client hash or it matches).
            const r = (resp?.results?.[0]) || {};
            const serverCrc32 = (r.crc32 || "").toLowerCase();
            const okFile = !r.error && (!clientCrc32 || clientCrc32.toLowerCase() === serverCrc32);
            if (okFile) {
              overlay.settleActive(slot, "done");
              ok++;
            } else {
              failed++;
              const why = r.error || (clientCrc32 ? tt("netdisk.crc32Mismatch") : "Failed");
              overlay.markFailedWithRetry(slot, why, async () => {
                overlay.reactivate(slot, { name: file.name, size: file.size });
                failed = Math.max(0, failed - 1);
                await sendOne();
                await this.load();
              });
            }
          };
          await sendOne();
          baseBytes += file.size || 0;
        }
        ElMessage.success(tt("volfiles.uploadedN", { n: ok }));
        await this.load();
        this.status = "";
      } catch (err) {
        ElMessage.error(err.message);
        this.status = err.message;
      } finally {
        this.busy = false;
        if (this.$refs.picker) this.$refs.picker.value = "";
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
.files-toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; flex-wrap: wrap; }
.pane-path { font-size: 12px; color: var(--muted); }
.file-name { padding: 0; }
</style>
