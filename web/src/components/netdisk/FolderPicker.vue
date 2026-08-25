<template>
  <!-- Copy/move destination picker across the three disks. The shared disk has
       no same-disk copy/move of its own, so it is never offered as a target
       when it is also the source. -->
  <el-dialog :model-value="visible" :title="tt('netdisk.pickTitle', { action, n: paths.length })" width="720px" top="5vh" append-to-body @update:model-value="$emit('update:visible', $event)">
    <p class="hint">{{ tt("netdisk.pickHint", { disk: diskLabel.toLowerCase() }) }}</p>
    <div class="picker-layout">
      <aside class="picker-tree">
        <button class="picker-tree-item" :class="{ active: targetDisk === 'netdisk' }" type="button" @click="switchDisk('netdisk')">{{ tt("netdisk.myNetdisk") }}</button>
        <button v-if="backupConfigured" class="picker-tree-item" :class="{ active: targetDisk === 'backup' }" type="button" @click="switchDisk('backup')">{{ tt("netdisk.backupDisk") }}</button>
        <button v-if="sourceDisk !== 'shareddisk' && sharedDiskConfigured" class="picker-tree-item" :class="{ active: targetDisk === 'shareddisk' }" type="button" @click="switchDisk('shareddisk')">{{ tt("netdisk.modeSharedDisk") }}</button>
      </aside>
      <section class="picker-main">
        <div class="picker-toolbar">
          <el-button size="small" :disabled="upDisabled" @click="goUp">{{ tt("netdisk.pickUp") }}</el-button>
          <div class="picker-path">/{{ path }}</div>
          <el-button size="small" @click="mkdir">{{ tt("netdisk.pickNewFolder") }}</el-button>
        </div>
        <div class="picker-list">
          <div v-if="loading" class="picker-empty">{{ tt("netdisk.pickLoading") }}</div>
          <div v-else-if="error" class="picker-empty">{{ error }}</div>
          <template v-else>
            <div
              v-for="f in folders"
              :key="f.path"
              class="picker-row"
              :class="{ disabled: move && paths.includes(f.path) }"
              @click="!(move && paths.includes(f.path)) && open(f.path)"
            >
              <span class="picker-folder-icon">📁</span>
              <span class="picker-folder-name" :title="f.name">{{ f.name }}</span>
            </div>
            <div v-if="!folders.length" class="picker-empty">{{ tt("netdisk.pickNoSubfolders") }}</div>
          </template>
        </div>
      </section>
    </div>
    <p class="hint" style="margin: 10px 0 0">{{ hint }}</p>
    <template #footer>
      <el-button @click="$emit('update:visible', false)">{{ tt("netdisk.pickCancel") }}</el-button>
      <el-button type="primary" :disabled="alreadyHere" @click="confirm">{{ tt("netdisk.pickConfirm", { action }) }}</el-button>
    </template>
  </el-dialog>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "FolderPicker",
  props: {
    visible: { type: Boolean, default: false },
    move: { type: Boolean, default: false },
    paths: { type: Array, default: () => [] },
    sourceDisk: { type: String, default: "netdisk" },
  },
  data() {
    return {
      path: "",
      folders: [],
      targetDisk: "netdisk",
      loading: false,
      error: "",
      sharedDiskResolved: false,
      sharedDiskOwnFolder: "",
    };
  },
  computed: {
    action() {
      return this.move ? tt("netdisk.move") : tt("netdisk.copy");
    },
    diskLabel() {
      return this.targetDisk === "backup" ? tt("netdisk.backupDisk")
        : this.targetDisk === "shareddisk" ? tt("netdisk.modeSharedDisk")
        : tt("netdisk.myNetdisk");
    },
    backupConfigured() {
      return !!store.me?.backupConfigured;
    },
    sharedDiskConfigured() {
      return !!store.me?.sharedDiskConfigured;
    },
    // On the shared disk a regular user is fenced inside their own subfolder:
    // "Up" is disabled once at its root. An admin may roam the whole pool.
    upDisabled() {
      if (this.targetDisk === "shareddisk" && !this.isAdminUser()) {
        return this.path === this.sharedDiskOwnFolder;
      }
      return !this.path;
    },
    // Moving items onto their current location is a no-op; block it.
    alreadyHere() {
      if (!this.move) return false;
      return this.paths.every((p) => (p || "").split("/").filter(Boolean).slice(0, -1).join("/") === this.path);
    },
    hint() {
      return this.alreadyHere ? tt("netdisk.pickAlreadyHere") : tt("netdisk.pickDest", { path: this.path });
    },
  },
  watch: {
    visible(v) {
      if (v) {
        // The shared disk has no same-disk copy/move of its own — when copying
        // or moving out of it, default the destination to the netdisk.
        this.targetDisk = this.sourceDisk === "shareddisk" ? "netdisk" : this.sourceDisk;
        this.path = this.targetDisk === this.sourceDisk ? (store.netdisk?.path || "") : "";
        this.sharedDiskResolved = false;
        this.sharedDiskOwnFolder = "";
        this.load("");
      }
    },
  },
  methods: {
    tt,
    isAdminUser() {
      return store.me?.role === "admin";
    },
    switchDisk(disk) {
      if (disk === this.targetDisk) return;
      this.targetDisk = disk;
      this.path = "";
      // Re-entering the shared disk re-anchors at the caller's own folder root
      // rather than resuming at whatever pool path was left behind.
      this.sharedDiskResolved = false;
      this.sharedDiskOwnFolder = "";
      this.load("");
    },
    open(path) {
      this.load(path);
    },
    goUp() {
      const parts = this.path.split("/").filter(Boolean);
      parts.pop();
      this.load(parts.join("/"));
    },
    // First entry into the shared disk as a destination: resolve the caller's
    // own folder once (the starting point they may write into). A regular user
    // is fenced inside it — they only ever see that folder and descendants; an
    // admin starts at the pool root.
    async loadPickerSharedDiskTarget() {
      this.loading = true;
      this.error = "";
      try {
        const rootData = await api("/api/shareddisk?path=");
        this.sharedDiskOwnFolder = rootData.ownFolder || "";
        this.sharedDiskResolved = true;
        if (this.isAdminUser()) {
          this.path = "";
          this.folders = (rootData.items || []).filter((f) => f.dir);
        } else {
          const own = this.sharedDiskOwnFolder;
          this.path = own;
          const ownData = own ? await api(`/api/shareddisk?path=${encodeURIComponent(own)}`) : rootData;
          this.folders = (ownData.items || []).filter((f) => f.dir);
        }
      } catch (err) {
        this.error = err.message;
      } finally {
        this.loading = false;
      }
    },
    async load(path) {
      if (this.targetDisk === "shareddisk" && !this.sharedDiskResolved) {
        await this.loadPickerSharedDiskTarget();
        return;
      }
      this.path = path;
      this.loading = true;
      this.error = "";
      try {
        const url = this.targetDisk === "backup"
          ? `/api/netdisk/backup/browse?path=${encodeURIComponent(path)}`
          : this.targetDisk === "shareddisk"
            ? `/api/shareddisk?path=${encodeURIComponent(path)}`
            : `/api/netdisk?path=${encodeURIComponent(path)}`;
        const data = await api(url);
        this.folders = (data.items || []).filter((f) => f.dir);
      } catch (err) {
        this.error = err.message;
      } finally {
        this.loading = false;
      }
    },
    async mkdir() {
      let name;
      try {
        ({ value: name } = await ElMessageBox.prompt(tt("netdisk.newFolderNamePrompt"), tt("netdisk.pickNewFolder"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      if (!name) return;
      try {
        const endpoint = this.targetDisk === "backup" ? "/api/netdisk/backup/mkdir"
          : this.targetDisk === "shareddisk" ? "/api/shareddisk/mkdir"
          : "/api/netdisk/mkdir";
        await api(endpoint, { method: "POST", body: JSON.stringify({ path: [this.path, name].filter(Boolean).join("/") }) });
        ElMessage.success(tt("netdisk.folderCreated"));
        await this.load(this.path);
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    confirm() {
      const items = this.paths.map((p) => ({ from: p, to: this.path }));
      this.$emit("update:visible", false);
      this.$emit("confirm", { items, targetDisk: this.targetDisk, move: this.move });
    },
  },
};
</script>

<style scoped>
.picker-layout { display: flex; gap: 14px; min-height: 320px; }
.picker-tree { display: flex; flex-direction: column; gap: 4px; width: 150px; flex-shrink: 0; }
.picker-tree-item {
  border: 1px solid var(--line);
  background: #fff;
  border-radius: 8px;
  padding: 8px 10px;
  cursor: pointer;
  font-size: 13px;
  text-align: left;
}
.picker-tree-item.active { border-color: var(--brand); color: var(--brand); background: var(--brand-tint); }
.picker-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.picker-toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.picker-path { flex: 1; font-size: 12px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: ui-monospace, Consolas, monospace; }
.picker-list { border: 1px solid var(--line); border-radius: 8px; flex: 1; overflow-y: auto; max-height: 320px; padding: 6px; }
.picker-row { display: flex; align-items: center; gap: 8px; padding: 7px 8px; border-radius: 6px; cursor: pointer; }
.picker-row:hover { background: var(--fill); }
.picker-row.disabled { opacity: 0.45; cursor: not-allowed; }
.picker-folder-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.picker-empty { color: var(--muted); text-align: center; padding: 24px 0; font-size: 12.5px; }
</style>
