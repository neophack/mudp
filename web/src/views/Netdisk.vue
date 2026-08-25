<template>
  <div class="stack">
    <div
      class="card netdisk-card"
      :class="{ 'drag-over': dragging }"
      @dragover.prevent="dragging = true"
      @dragleave="dragging = false"
      @drop.prevent="onDrop"
    >
      <div class="netdisk-toolbar">
        <div class="netdisk-title">
          <!-- netdisk (primary SSD) / backup (slow mirror) / shareddisk (one
               pool per group) each keep their own browsing context. -->
          <div class="netdisk-mode-toggle" role="tablist">
            <button class="netdisk-mode-btn" :class="{ active: mode === 'netdisk' }" role="tab" @click="switchMode('netdisk')">{{ tt("netdisk.modeNetdisk") }}</button>
            <button v-if="backupConfigured" class="netdisk-mode-btn" :class="{ active: mode === 'backup' }" role="tab" @click="switchMode('backup')">{{ tt("netdisk.modeBackup") }}</button>
            <button v-if="sharedDiskConfigured" class="netdisk-mode-btn" :class="{ active: mode === 'shareddisk' }" role="tab" @click="switchMode('shareddisk')">{{ tt("netdisk.modeSharedDisk") }}</button>
          </div>
          <span class="netdisk-count">{{ tt("netdisk.counts", { folders: folderCount, files: fileCount }) }}</span>
        </div>
        <div class="netdisk-actions">
          <el-button size="small" @click="goUp">{{ tt("netdisk.up") }}</el-button>
          <el-button v-if="canMkdir" size="small" @click="mkdir">{{ tt("netdisk.newFolder") }}</el-button>
          <!-- Upload and Folder-upload are netdisk-only: the backup disk is a
               mirror target and the shared disk has no upload surface of its
               own — files reach it via copy/move. -->
          <template v-if="mode === 'netdisk'">
            <el-button size="small" type="primary" plain @click="pickFiles">{{ tt("netdisk.upload") }}</el-button>
            <el-button size="small" type="primary" plain @click="pickFolder">{{ tt("netdisk.folder") }}</el-button>
            <input ref="filePicker" type="file" multiple hidden @change="onFilesPicked" />
            <input ref="folderPicker" type="file" webkitdirectory directory multiple hidden @change="onFilesPicked" />
          </template>
          <template v-if="canMutate()">
            <el-button size="small" type="danger" plain :disabled="!selection.length || !allOwn" :title="!allOwn ? tt('netdisk.batchMixedForeign') : ''" @click="batchDelete">{{ tt("netdisk.deleteN", { n: selection.length }) }}</el-button>
            <el-button size="small" :disabled="!selection.length" @click="batchCopyMove(false)">{{ tt("netdisk.copyN", { n: selection.length }) }}</el-button>
            <el-button size="small" :disabled="!selection.length || !allOwn" :title="!allOwn ? tt('netdisk.batchMixedForeign') : ''" @click="batchCopyMove(true)">{{ tt("netdisk.moveN", { n: selection.length }) }}</el-button>
            <el-button v-if="mode === 'netdisk'" size="small" :disabled="!selection.length" @click="batchShare">{{ tt("netdisk.shareN", { n: selection.length }) }}</el-button>
            <el-button v-if="mode !== 'shareddisk'" size="small" type="primary" plain :disabled="!selection.length" @click="batchDownload">{{ tt("netdisk.downloadN", { n: selection.length }) }}</el-button>
          </template>
        </div>
      </div>

      <div v-if="mode === 'backup' && backupWarning" class="netdisk-hint warn">{{ backupWarning }}</div>
      <div v-if="mode === 'shareddisk'" class="netdisk-hint">{{ tt("netdisk.sharedDiskHint") }}</div>

      <div class="netdisk-pathbar">
        <div class="netdisk-crumbs">
          <el-button link size="small" @click="navigate('')">{{ tt("netdisk.allFiles") }}</el-button>
          <template v-for="(crumb, i) in crumbs" :key="i">
            <span class="crumb-sep">/</span>
            <el-button link size="small" @click="navigate(crumb.path)">{{ crumb.name }}</el-button>
          </template>
        </div>
        <div class="netdisk-used" v-html="quotaHtml"></div>
      </div>

      <el-table
        ref="table"
        :data="sortedItems"
        size="small"
        :empty-text="tt('netdisk.noFiles')"
        row-key="path"
        :row-class-name="s.isMobile ? 'row-tappable' : ''"
        @selection-change="onSelectionChange"
        @row-click="onRowClick"
      >
        <el-table-column v-if="canMutate() && !s.isMobile" type="selection" width="40" reserve-selection />
        <el-table-column :label="tt('common.name')" :min-width="s.isMobile ? 150 : 280">
          <template #default="{ row }">
            <div class="netdisk-file">
              <span class="netdisk-icon" :class="row.dir ? 'folder' : 'file'">
                <svg v-if="row.dir" viewBox="0 0 24 24" focusable="false"><path d="M3 6.8C3 5.8 3.8 5 4.8 5h5.1l2 2.2h7.3c1 0 1.8.8 1.8 1.8v1H3V6.8Z"/><path d="M3 9h18l-1.2 8.2c-.1 1-1 1.8-2 1.8H6.2c-1 0-1.8-.7-2-1.8L3 9Z"/></svg>
                <svg v-else viewBox="0 0 24 24" focusable="false"><path d="M6 3.5h8.4L19 8.1v12.1c0 1-.8 1.8-1.8 1.8H6.8c-1 0-1.8-.8-1.8-1.8V5.3c0-1 .8-1.8 1.8-1.8Z"/><path d="M14 3.8V8h4.2"/><path d="M8.5 12h7M8.5 15h7M8.5 18h4.5"/></svg>
              </span>
              <div class="netdisk-file-text">
                <!-- Phone: the name is plain text — the whole row is the tap
                     target, split at the row's midpoint (onRowClick): taps
                     left of it open the item, taps right of it pop the
                     action sheet. -->
                <div v-if="s.isMobile" class="primary-line name-link name-mobile" :title="row.name">{{ row.name }}</div>
                <el-button v-else-if="row.dir" link class="name-link" :title="row.name" @click.stop="navigate(row.path)">{{ row.name }}</el-button>
                <el-button v-else-if="previewKind(row.name) && mode !== 'shareddisk'" link class="name-link" :title="row.name" @click.stop="openViewer(row)">{{ row.name }}</el-button>
                <a v-else-if="!row.dir && mode !== 'shareddisk'" class="name-link" :href="downloadHref(row)" :title="row.name" @click.stop>{{ row.name }}</a>
                <span v-else class="name-link" :title="row.name">{{ row.name }}</span>
                <div class="netdisk-file-meta">{{ row.dir ? "-" : fmtBytes(row.size) }} · {{ fmtDate(row.modTime) }}</div>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('common.size')" width="100" class-name="netdisk-size-col" label-class-name="netdisk-size-col">
          <template #default="{ row }">{{ row.dir ? "-" : fmtBytes(row.size) }}</template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('netdisk.colModified')" width="150" class-name="netdisk-time-col" label-class-name="netdisk-time-col">
          <template #default="{ row }"><span :title="new Date(row.modTime).toLocaleString()">{{ fmtDate(row.modTime) }}</span></template>
        </el-table-column>
        <!-- Icon-only actions so every action of a row fits on one line; the
             column is sized from the widest action set actually on screen. -->
        <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" :width="actionsColWidth" fixed="right">
          <template #default="{ row }">
            <div class="row-actions">
              <el-button
                v-for="a in rowActions(row)"
                :key="a.key"
                link
                class="row-action-btn"
                :class="{ 'danger-text': a.danger }"
                :icon="a.icon"
                :title="a.label"
                :aria-label="a.label"
                @click="runRowAction(a, row)"
              />
            </div>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- External Links cards are netdisk-only. -->
    <template v-if="mode === 'netdisk'">
      <div class="card">
        <div class="card-head"><h2>{{ tt("netdisk.externalLinks") }}</h2></div>
        <el-table :data="shares" size="small" :empty-text="tt('netdisk.noExternalLinks')" :row-class-name="({ row }) => [row.expired ? 'row-muted' : '', s.isMobile ? 'row-tappable' : ''].join(' ')" @row-click="onShareRowClick">
          <el-table-column :label="tt('common.name')" :min-width="s.isMobile ? 150 : 180">
            <template #default="{ row }">
              <div class="primary-line" :title="row.name + ((row.paths || []).length ? ' · ' + row.paths.join(', ') : '')">{{ row.name }}</div>
              <div class="secondary-line">{{ (row.paths || []).join(", ") }}</div>
            </template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('netdisk.colLink')" min-width="240">
            <template #default="{ row }">
              <a :href="shareLink(row)" target="_blank" class="share-link" @click.stop>{{ shareLink(row) }}</a>
            </template>
          </el-table-column>
          <el-table-column :label="tt('netdisk.colExpires')" :width="s.isMobile ? 100 : 160">
            <template #default="{ row }">
              <el-tag v-if="row.permanent" size="small">{{ tt("netdisk.permanent") }}</el-tag>
              <el-tag v-else-if="row.expired" size="small" type="danger">{{ tt("netdisk.expired") }}</el-tag>
              <span v-else>{{ row.expiresAt ? new Date(row.expiresAt).toLocaleString() : tt("netdisk.7days") }}</span>
            </template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('netdisk.colAccess')" width="110">
            <template #default="{ row }">
              <span :title="row.hasPassword ? tt('netdisk.extractionCode') : ''">{{ row.hasPassword ? (row.password || tt("netdisk.password")) : tt("netdisk.public") }}</span>
            </template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" width="150" fixed="right">
            <template #default="{ row }">
              <el-button link size="small" @click="copyShare(row)">{{ tt("common.copy") }}</el-button>
              <el-button link size="small" class="danger-text" @click="deleteShare(row)">{{ tt("common.delete") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div v-if="isAdmin()" class="card">
        <div class="card-head">
          <h2>{{ tt("netdisk.allExternalLinks") }}</h2>
          <div class="head-actions">
            <el-button size="small" type="danger" plain @click="deleteAdminShares">{{ tt("netdisk.deleteSelected") }}</el-button>
          </div>
        </div>
        <el-table ref="adminSharesTable" :data="adminShares" size="small" :empty-text="tt('netdisk.noExternalLinks')" :row-class-name="({ row }) => (row.expired ? 'row-muted' : '')" @selection-change="onAdminShareSelection">
          <el-table-column type="selection" width="40" />
          <el-table-column v-if="!s.isMobile" :label="tt('netdisk.colOwner')" width="130">
            <template #default="{ row }">{{ displayNameForUsername(row.owner) || row.ownerId }}</template>
          </el-table-column>
          <el-table-column :label="tt('common.name')" min-width="170">
            <template #default="{ row }">
              <div class="primary-line">{{ row.name }}</div>
              <div class="secondary-line">{{ (row.paths || []).join(", ") }}</div>
            </template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('netdisk.colLink')" min-width="240">
            <template #default="{ row }">
              <a :href="shareLink(row)" target="_blank" class="share-link">{{ shareLink(row) }}</a>
            </template>
          </el-table-column>
          <el-table-column :label="tt('netdisk.colExpires')" :width="s.isMobile ? 100 : 160">
            <template #default="{ row }">
              <el-tag v-if="row.permanent" size="small">{{ tt("netdisk.permanent") }}</el-tag>
              <el-tag v-else-if="row.expired" size="small" type="danger">{{ tt("netdisk.expired") }}</el-tag>
              <span v-else>{{ row.expiresAt ? new Date(row.expiresAt).toLocaleString() : tt("netdisk.7days") }}</span>
            </template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('netdisk.colAccess')" width="110">
            <template #default="{ row }">{{ row.hasPassword ? (row.password || tt("netdisk.password")) : tt("netdisk.public") }}</template>
          </el-table-column>
        </el-table>
      </div>
    </template>

    <!-- Phone-width rows: tap a file (or share) to get its actions in a
         bottom sheet. -->
    <action-sheet
      v-model:visible="sheet.visible"
      :title="sheet.row?.name || ''"
      :subtitle="sheetSubtitle"
      :items="sheetItems"
      :columns="4"
      @select="onSheetSelect"
    />
    <action-sheet
      v-model:visible="shareSheet.visible"
      :title="shareSheet.row?.name || ''"
      :subtitle="shareSheet.row?.expired ? tt('netdisk.expired') : ''"
      :items="[{ key: 'copy', label: tt('common.copy'), icon: 'CopyDocument' }, { key: 'delete', label: tt('common.delete'), icon: 'Delete', danger: true }]"
      :columns="4"
      @select="onShareSheetSelect"
    />

    <folder-picker
      v-model="picker.visible"
      :move="picker.move"
      :paths="picker.paths"
      :source-disk="picker.sourceDisk"
      @confirm="onPickerConfirm"
    />
    <share-dialog
      v-model="share.visible"
      :paths="share.paths"
      :name="share.name"
      @done="refresh"
    />
    <viewer-dialog
      v-model="viewer.visible"
      :path="viewer.path"
      :from-backup="viewer.fromBackup"
    />
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api, copyText } from "@/api";
import { store, canMutate, isAdmin, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import { fmtBytes, joinPath } from "@/lib/common.js";
import { beginLocalCopy } from "@/overlays";
import { uploadFilesHandler, dropStream, isUploading } from "@/netdiskUpload";
import FolderPicker from "@/components/netdisk/FolderPicker.vue";
import ShareDialog from "@/components/netdisk/ShareDialog.vue";
import ViewerDialog from "@/components/netdisk/ViewerDialog.vue";
import ActionSheet from "@/components/ActionSheet.vue";
import { previewKind } from "@/lib/preview.js";

export default {
  name: "Netdisk",
  components: { ActionSheet, FolderPicker, ShareDialog, ViewerDialog },
  data() {
    return {
      s: store,
      mode: "netdisk",
      // Independent browsing contexts per disk so toggling doesn't lose state.
      modeState: {
        netdisk: { path: "", selection: [] },
        backup: { path: "", selection: [] },
        shareddisk: { path: "", selection: [] },
      },
      items: [],
      quota: null,
      shares: [],
      adminShares: [],
      backupWarning: "",
      ownFolder: "",
      selection: [],
      adminShareSelection: [],
      dragging: false,
      picker: { visible: false, move: false, paths: [], sourceDisk: "netdisk" },
      share: { visible: false, paths: [], name: "" },
      viewer: { visible: false, path: "", fromBackup: false },
      sheet: { visible: false, row: null },
      shareSheet: { visible: false, row: null },
    };
  },
  computed: {
    path() {
      return this.modeState[this.mode].path;
    },
    backupConfigured() {
      return !!store.me?.backupConfigured;
    },
    sharedDiskConfigured() {
      return !!store.me?.sharedDiskConfigured;
    },
    sortedItems() {
      return [...(this.items || [])].sort((a, b) => {
        if (a.dir !== b.dir) return a.dir ? -1 : 1;
        return String(a.name || "").localeCompare(String(b.name || ""), undefined, { numeric: true, sensitivity: "base" });
      });
    },
    sheetSubtitle() {
      const r = this.sheet.row;
      if (!r) return "";
      return (r.dir ? tt("netdisk.folder") : fmtBytes(r.size)) + " · " + this.fmtDate(r.modTime);
    },
    sheetItems() {
      return this.rowActions(this.sheet.row);
    },
    // Six icon buttons at most (download/rename/copy/move/share/delete); size
    // the fixed column to the widest set on screen so nothing wraps or clips.
    actionsColWidth() {
      let n = 1;
      for (const row of this.sortedItems) n = Math.max(n, this.rowActions(row).length);
      return n * 26 + (n - 1) * 2 + 24;
    },
    fileCount() {
      return this.items.filter((f) => !f.dir).length;
    },
    folderCount() {
      return this.items.length - this.fileCount;
    },
    crumbs() {
      const parts = (this.path || "").split("/").filter(Boolean);
      const out = [];
      let acc = "";
      for (const part of parts) {
        acc = joinPath(acc, part);
        out.push({ name: part, path: acc });
      }
      return out;
    },
    // A regular user may only mkdir inside their own shared-disk subfolder.
    canMkdir() {
      if (this.mode !== "shareddisk") return true;
      if (isAdmin()) return true;
      const own = this.ownFolder;
      if (!own) return false;
      const segs = (this.path || "").split("/").filter(Boolean);
      return segs.length > 0 && segs[0] === own;
    },
    // On the shared disk a selection that mixes other members' rows disables
    // batch delete/move (copy stays available everywhere).
    allOwn() {
      if (this.mode !== "shareddisk" || isAdmin()) return true;
      const own = this.ownFolder;
      return this.selection.every((p) => own && (p || "").split("/")[0] === own);
    },
    quotaHtml() {
      const q = this.quota;
      if (!q) return tt("netdisk.usedN", { used: "0 B" });
      const used = q.usedBytes || 0;
      const estimating = q.usedEstimating ? ` <span class="hint">${tt("netdisk.estimating")}</span>` : "";
      if (q.totalBytes > 0) {
        const pct = Math.min(100, Math.round((used / q.totalBytes) * 100));
        return `${tt("netdisk.usedOfTotal", { used: fmtBytes(used), total: fmtBytes(q.totalBytes) })} <span class="quota-bar"><span style="width:${pct}%"></span></span> ${pct}%${estimating}`;
      }
      if (q.diskFreeBytes != null) {
        return `${tt("netdisk.usedFree", { used: fmtBytes(used), free: fmtBytes(q.diskFreeBytes) })}${estimating}`;
      }
      return `${tt("netdisk.usedN", { used: fmtBytes(used) })}${estimating}`;
    },
  },
  async mounted() {
    await this.refresh();
    // Self-refresh on a slow cadence, like the old self-fetching view.
    this.timer = setInterval(() => {
      if (!document.hidden && !isUploading()) this.refresh();
    }, 15000);
  },
  beforeUnmount() {
    clearInterval(this.timer);
  },
  methods: {
    tt,
    canMutate,
    isAdmin,
    displayNameForUsername,
    previewKind,
    fmtBytes,
    fmtDate(ts) {
      const d = new Date(ts);
      if (Number.isNaN(d.getTime())) return "-";
      const pad = (n) => String(n).padStart(2, "0");
      return `${d.toLocaleDateString()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    },
    async refresh() {
      // A disk whose root was unset can leave a stale tab: fall back to the
      // netdisk, which is always available.
      if (this.mode === "backup" && !this.backupConfigured) this.mode = "netdisk";
      if (this.mode === "shareddisk" && !this.sharedDiskConfigured) this.mode = "netdisk";
      const path = this.path;
      try {
        const listURL = this.mode === "backup"
          ? `/api/netdisk/backup/browse?path=${encodeURIComponent(path)}`
          : this.mode === "shareddisk"
            ? `/api/shareddisk?path=${encodeURIComponent(path)}`
            : `/api/netdisk?path=${encodeURIComponent(path)}`;
        const quotaURL = this.mode === "shareddisk" ? "/api/shareddisk/quota" : "/api/netdisk/quota";
        const [list, quota, shares, adminShares] = await Promise.all([
          api(listURL),
          this.mode === "backup" ? Promise.resolve(null) : api(quotaURL).catch(() => null),
          this.mode === "netdisk" ? api("/api/netdisk/shares").catch(() => []) : Promise.resolve([]),
          this.mode === "netdisk" && isAdmin() ? api("/api/admin/netdisk/shares").catch(() => []) : Promise.resolve([]),
        ]);
        this.items = list.items || [];
        this.quota = this.mode === "backup" ? (list.quota || null) : quota;
        this.shares = shares || [];
        this.adminShares = adminShares || [];
        this.backupWarning = this.mode === "backup" && list.unavailable ? this.backupUnavailableMessage(list.message) : "";
        this.ownFolder = list.ownFolder || "";
        this.modeState[this.mode].path = list.path || "";
        // The folder picker opens same-disk copy/move at the folder currently
        // being browsed; publish the netdisk cursor for it (other disks start
        // at their root).
        if (this.mode === "netdisk") store.netdisk = { ...(store.netdisk || { path: "" }), path: list.path || "" };
      } catch (err) {
        if (this.mode === "backup") {
          this.items = [];
          this.quota = null;
          this.backupWarning = this.backupUnavailableMessage(err);
        } else {
          ElMessage.error(err.message);
        }
      }
    },
    backupUnavailableMessage(errOrMsg) {
      const msg = String(errOrMsg?.message || errOrMsg || "").trim();
      if (!msg) return tt("netdisk.backupUnavailable");
      if (msg.toLowerCase().includes("not configured")) return msg;
      return tt("netdisk.backupUnavailableMsg", { msg });
    },
    switchMode(mode) {
      if (mode === this.mode) return;
      // Park the current disk's selection so toggling back restores it, like
      // the old per-mode modeState.
      this.modeState[this.mode].selection = [...this.selection];
      this.mode = mode;
      this.selection = this.modeState[mode].selection || [];
      this.refresh();
    },
    navigate(path) {
      this.modeState[this.mode].path = path;
      this.selection = [];
      this.modeState[this.mode].selection = [];
      this.refresh();
    },
    goUp() {
      const parts = (this.path || "").split("/").filter(Boolean);
      parts.pop();
      this.navigate(parts.join("/"));
    },
    async mkdir() {
      let name;
      try {
        ({ value: name } = await ElMessageBox.prompt(tt("netdisk.folderNamePrompt"), tt("netdisk.newFolder"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      if (!name) return;
      const base = this.mode === "shareddisk" ? "/api/shareddisk" : this.mode === "backup" ? "/api/netdisk/backup" : "/api/netdisk";
      try {
        await api(`${base}/mkdir`, { method: "POST", body: JSON.stringify({ path: joinPath(this.path, name) }) });
        ElMessage.success(tt("netdisk.folderCreated"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    // A regular user may only mutate shared-disk rows inside their own folder.
    isOwnSharedDiskRow(row) {
      if (isAdmin()) return true;
      const own = this.ownFolder;
      if (!own) return false;
      return (row.path || "").split("/")[0] === own;
    },
    onSelectionChange(rows) {
      this.selection = rows.map((r) => r.path);
    },
    onRowClick(row, _column, e) {
      if (!store.isMobile) return;
      // The row splits at its midpoint: left half opens the item (folder
      // navigation, preview, download), right half pops the action sheet.
      // Rows with no open action (shared-disk files) pop from both halves.
      const open = this.openAction(row);
      if (open) {
        const rect = (e.target.closest("tr") || e.currentTarget).getBoundingClientRect();
        if (e.clientX - rect.left < rect.width / 2) {
          open();
          return;
        }
      }
      this.sheet = { visible: true, row };
    },
    openAction(row) {
      if (row.dir) return () => this.navigate(row.path);
      if (this.mode === "shareddisk") return null;
      if (previewKind(row.name)) return () => this.openViewer(row);
      return () => this.downloadRow(row);
    },
    // One action list per row, shared by the desktop icon column and the
    // phone action sheet so both surfaces can never drift apart.
    rowActions(r) {
      if (!r) return [];
      if (!canMutate()) {
        if (this.mode !== "shareddisk") return [{ key: "download", label: tt("netdisk.download"), icon: "Download" }];
        return [];
      }
      if (this.mode === "shareddisk") {
        const items = [{ key: "copy", label: tt("netdisk.copy"), icon: "CopyDocument" }];
        if (this.isOwnSharedDiskRow(r)) {
          items.push(
            { key: "move", label: tt("netdisk.move"), icon: "Rank" },
            { key: "rename", label: tt("netdisk.rename"), icon: "Edit" },
            { key: "delete", label: tt("common.delete"), icon: "Delete", danger: true },
          );
        }
        return items;
      }
      const items = [
        { key: "download", label: tt("netdisk.download"), icon: "Download" },
        { key: "rename", label: tt("netdisk.rename"), icon: "Edit" },
        { key: "copy", label: tt("netdisk.copy"), icon: "CopyDocument" },
        { key: "move", label: tt("netdisk.move"), icon: "Rank" },
      ];
      if (this.mode === "netdisk") items.push({ key: "share", label: tt("netdisk.share"), icon: "Share" });
      items.push({ key: "delete", label: tt("common.delete"), icon: "Delete", danger: true });
      return items;
    },
    runRowAction(item, row) {
      if (!row) return;
      if (item.key === "download") this.downloadRow(row);
      else if (item.key === "rename") this.rename(row);
      else if (item.key === "copy") this.openPicker(false, [row.path]);
      else if (item.key === "move") this.openPicker(true, [row.path]);
      else if (item.key === "share") this.openShare([row.path], row.name);
      else if (item.key === "delete") this.remove([row.path], row.name);
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      this.runRowAction(item, row);
    },
    onShareRowClick(row) {
      if (!store.isMobile) return;
      this.shareSheet = { visible: true, row };
    },
    onShareSheetSelect(item) {
      const row = this.shareSheet.row;
      this.shareSheet.visible = false;
      if (!row) return;
      if (item.key === "copy") this.copyShare(row);
      else if (item.key === "delete") this.deleteShare(row);
    },
    onAdminShareSelection(rows) {
      this.adminShareSelection = rows.map((r) => r.token);
    },
    downloadHref(f) {
      const base = this.mode === "backup" ? "/api/netdisk/backup/download" : "/api/netdisk/download";
      return `${base}?path=${encodeURIComponent(f.path)}&ts=${Date.now()}`;
    },
    downloadRow(f) {
      window.open(this.downloadHref(f), "_blank");
    },
    async remove(paths, name) {
      try {
        await ElMessageBox.confirm(tt("netdisk.deleteConfirmOne", { name }), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      const endpoint = this.mode === "shareddisk" ? "/api/shareddisk/delete" : this.mode === "backup" ? "/api/netdisk/backup/delete" : "/api/netdisk/delete";
      try {
        await api(endpoint, { method: "POST", body: JSON.stringify({ paths }) });
        ElMessage.success(tt("netdisk.deleted"));
        this.selection = [];
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async batchDelete() {
      const paths = [...this.selection];
      if (!paths.length) return;
      try {
        await ElMessageBox.confirm(tt("netdisk.batchDeleteConfirm", { n: paths.length }), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      const endpoint = this.mode === "shareddisk" ? "/api/shareddisk/delete" : this.mode === "backup" ? "/api/netdisk/backup/delete" : "/api/netdisk/delete";
      try {
        await api(endpoint, { method: "POST", body: JSON.stringify({ paths }) });
        ElMessage.success(tt("netdisk.deleted"));
        this.selection = [];
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    batchCopyMove(move) {
      if (!this.selection.length) return;
      this.openPicker(move, [...this.selection]);
    },
    openPicker(move, paths) {
      const sourceDisk = this.mode;
      this.picker = { visible: true, move, paths, sourceDisk };
    },
    async onPickerConfirm({ items, targetDisk, move }) {
      const sourceDisk = this.mode === "shareddisk" ? "shareddisk" : this.mode === "backup" ? "backup" : "netdisk";
      const sameDisk = sourceDisk === targetDisk;
      const endpoint = sameDisk
        ? (sourceDisk === "backup" ? "/api/netdisk/backup/copy" : "/api/netdisk/copy")
        : "/api/netdisk/transfer";
      const body = sameDisk
        ? { items, move, policy: "rename" }
        : { fromDisk: sourceDisk, toDisk: targetDisk, items, move, policy: "rename" };
      const kind = sameDisk ? (move ? "netdisk.move" : "netdisk.copy") : "netdisk.transfer";
      const data = await this.runWithCopyProgress(kind, items.length, () =>
        api(endpoint, { method: "POST", body: JSON.stringify(body) })
      );
      this.selection = [];
      const errors = (data.results || []).filter((r) => r.status === "error").length;
      if (move) {
        if (errors) ElMessage.warning(tt("netdisk.movedNFailed", { n: data.count || 0, err: errors }));
        else ElMessage.success(tt("netdisk.movedN", { n: data.count || 0 }));
      } else {
        if (errors) ElMessage.warning(tt("netdisk.copiedNFailed", { n: data.count || 0, err: errors }));
        else ElMessage.success(tt("netdisk.copiedN", { n: data.count || 0 }));
      }
      this.refresh();
    },
    // The server processes a bulk copy/move in one synchronous call, so live
    // counts come from a fast side-channel poll of /api/tasks?all=1 (bypassing
    // the Jobs panel's 10s visibility delay) driving the floating overlay.
    async runWithCopyProgress(kind, total, run) {
      const progress = beginLocalCopy(kind, total);
      const startedAfter = Date.now() - 2000; // slack for client/server clock skew
      const poll = setInterval(async () => {
        try {
          const tasks = await api("/api/tasks?all=1");
          const mine = (tasks || [])
            .filter((t) => t.kind === kind && Date.parse(t.startedAt) >= startedAfter)
            .sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt))[0];
          if (mine) progress.update(mine.done, mine.total, mine.message, mine.unit, mine.id);
        } catch { /* best-effort; the overlay just won't advance this tick */ }
      }, 600);
      try {
        return await run();
      } finally {
        clearInterval(poll);
        progress.end();
      }
    },
    batchShare() {
      const paths = [...this.selection];
      if (!paths.length) return;
      const name = paths.length === 1 ? paths[0].split("/").pop() : tt("netdisk.sharedItems");
      this.openShare(paths, name);
    },
    openShare(paths, name) {
      this.share = { visible: true, paths, name };
    },
    shareLink(s) {
      return `${location.origin}/pan/${encodeURIComponent(s.token)}`;
    },
    async copyShare(s) {
      const code = s.password;
      const text = code ? `${this.shareLink(s)}\n${tt("netdisk.extractionCode")}: ${code}` : this.shareLink(s);
      try {
        await copyText(text);
        ElMessage.success(code ? tt("netdisk.linkCodeCopied") : tt("netdisk.linkCopied"));
      } catch {
        ElMessage.error(tt("common.copyFailed"));
      }
    },
    async deleteShare(s) {
      try {
        await api("/api/netdisk/share/delete", { method: "POST", body: JSON.stringify({ token: s.token }) });
        ElMessage.success(tt("netdisk.externalLinkDeleted"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async deleteAdminShares() {
      const tokens = [...this.adminShareSelection];
      if (!tokens.length) {
        ElMessage.info(tt("netdisk.selectLink"));
        return;
      }
      try {
        await ElMessageBox.confirm(tt("netdisk.deleteLinksConfirm", { n: tokens.length }), tt("netdisk.deleteSelected"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/admin/netdisk/shares/delete", { method: "POST", body: JSON.stringify({ tokens }) });
        ElMessage.success(tt("netdisk.externalLinksDeleted"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async rename(f) {
      let next;
      try {
        ({ value: next } = await ElMessageBox.prompt(tt("netdisk.renamePrompt"), tt("netdisk.rename"), {
          inputValue: f.name,
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      if (!next || next === f.name) return;
      const parts = f.path.split("/");
      parts.pop();
      const to = joinPath(parts.join("/"), next);
      const endpoint = this.mode === "shareddisk" ? "/api/shareddisk/rename" : this.mode === "backup" ? "/api/netdisk/backup/rename" : "/api/netdisk/rename";
      try {
        await api(endpoint, { method: "POST", body: JSON.stringify({ from: f.path, to }) });
        ElMessage.success(tt("netdisk.renamed"));
        this.refresh();
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    batchDownload() {
      const paths = [...this.selection];
      if (!paths.length) return;
      const urlFor = (p) => `${this.mode === "backup" ? "/api/netdisk/backup/download" : "/api/netdisk/download"}?path=${encodeURIComponent(p)}&ts=${Date.now()}`;
      if (paths.length === 1 && !this.items.find((f) => f.path === paths[0])?.dir) {
        location.href = urlFor(paths[0]);
        return;
      }
      // Multiple items: download each as a separate zip/file.
      paths.forEach((p, i) => {
        setTimeout(() => {
          const a = document.createElement("a");
          a.href = urlFor(p);
          a.download = "";
          a.click();
        }, i * 300);
      });
    },
    openViewer(f) {
      this.viewer = { visible: true, path: f.path, fromBackup: this.mode === "backup" };
    },
    pickFiles() {
      if (!canMutate()) {
        ElMessage.error(tt("netdisk.readonlyUpload"));
        return;
      }
      this.$refs.filePicker.click();
    },
    pickFolder() {
      if (!canMutate()) {
        ElMessage.error(tt("netdisk.readonlyUpload"));
        return;
      }
      this.$refs.folderPicker.click();
    },
    async onFilesPicked(e) {
      await uploadFilesHandler([...e.target.files], this.path, () => this.refresh());
      e.target.value = "";
    },
    // Drag-drop: folders are not exposed via dataTransfer.files with usable
    // paths, so walk the drop tree via the FileSystemEntry API. The walker
    // streams into a bounded buffer so traversal and upload run in parallel.
    async onDrop(e) {
      this.dragging = false;
      // The upload endpoint only writes into the netdisk; dropping while
      // browsing the backup or shared disk would silently land files under
      // that disk's path in the netdisk, so refuse there.
      if (this.mode !== "netdisk") {
        ElMessage.error(tt("netdisk.uploadNetdiskOnly"));
        return;
      }
      if (!canMutate()) {
        ElMessage.error(tt("netdisk.readonlyUpload"));
        return;
      }
      const items = e.dataTransfer?.items;
      if (!items || !items.length) return;
      await uploadFilesHandler(dropStream(items), this.path, () => this.refresh());
    },
  },
};
</script>

<style scoped>
.stack > * + * { margin-top: 16px; }
.netdisk-toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
.netdisk-title { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.netdisk-mode-toggle { display: inline-flex; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
.netdisk-mode-btn { border: none; background: #fff; padding: 7px 14px; font-size: 13px; cursor: pointer; }
.netdisk-mode-btn + .netdisk-mode-btn { border-left: 1px solid var(--line); }
.netdisk-mode-btn.active { background: var(--brand); color: #fff; }
.netdisk-count { color: var(--muted); font-size: 12.5px; }
.netdisk-actions { margin-left: auto; display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.netdisk-hint { background: var(--fill); border: 1px dashed var(--line); color: var(--muted); font-size: 12.5px; border-radius: 8px; padding: 8px 12px; margin-bottom: 10px; }
.netdisk-hint.warn { background: var(--warn-bg); border-color: var(--warn-line); color: var(--warn-strong); }
.netdisk-pathbar { display: flex; align-items: center; gap: 14px; margin-bottom: 8px; flex-wrap: wrap; }
.netdisk-crumbs { display: flex; align-items: center; flex-wrap: wrap; font-size: 13px; }
.crumb-sep { color: var(--muted); margin: 0 2px; }
.netdisk-used { margin-left: auto; font-size: 12.5px; color: var(--muted); }
.quota-bar { display: inline-block; vertical-align: middle; width: 120px; height: 6px; background: var(--line); border-radius: 3px; overflow: hidden; margin: 0 4px; }
.quota-bar span { display: block; height: 100%; background: var(--brand); }
.netdisk-card.drag-over { outline: 2px dashed var(--brand); outline-offset: -6px; }
.netdisk-file { display: flex; align-items: center; gap: 10px; }
.netdisk-icon { width: 28px; height: 28px; display: flex; align-items: center; justify-content: center; color: var(--brand); flex-shrink: 0; }
.netdisk-icon.folder { color: #e6a23c; }
.netdisk-icon svg { width: 22px; height: 22px; fill: currentColor; }
.netdisk-file-text { min-width: 0; }
/* Size and Modified have their own columns on a wide screen; the meta line
   under the name is the narrow-screen replacement for them (see the media
   query below), not a second copy. */
.netdisk-file-meta { display: none; color: var(--muted); font-size: 12px; }
.name-link { padding: 0; font-weight: 600; }
.name-mobile { color: var(--brand); }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.share-link { color: var(--brand); font-size: 12px; word-break: break-all; }
>>> .row-muted { opacity: 0.55; }

/* Phones: drop the Size and Modified columns and fold both values into the
   meta line under the file name, so the name and the row actions keep the
   width they need. */
@media (max-width: 860px) {
  .netdisk-file-meta { display: block; }
  >>> .netdisk-size-col,
  >>> .netdisk-time-col { display: none; }
}
</style>
