<template>
  <div class="card">
    <div class="card-head">
      <h2>{{ tt("volumes.title") }}</h2>
      <div v-if="canMutate()" class="head-actions">
        <el-button size="small" @click="prune">{{ tt("volumes.pruneUnused") }}</el-button>
        <el-button size="small" type="primary" @click="dialog = true">{{ tt("volumes.newVolume") }}</el-button>
      </div>
    </div>
    <el-table
      :data="s.volumes"
      size="small"
      :empty-text="tt('volumes.noVolumesCreate')"
      :row-class-name="s.isMobile ? 'row-tappable' : ''"
      @row-click="onRowClick"
    >
      <el-table-column :label="tt('common.name')" :min-width="s.isMobile ? 150 : 220">
        <template #default="{ row }">
          <div class="primary-line">{{ row.name }}</div>
          <div class="secondary-line mono">{{ row.fullName || row.name }}</div>
        </template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('volumes.colDriver')" width="100">
        <template #default="{ row }"><span class="secondary-line">{{ row.driver }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.size')" width="100">
        <template #default="{ row }">{{ fmtMB(row.sizeMb) }}</template>
      </el-table-column>
      <el-table-column :label="tt('volumes.colInUse')" :width="s.isMobile ? 88 : 110">
        <template #default="{ row }">
          <el-tag size="small" :type="row.inUse ? 'success' : 'info'">{{ row.inUse ? tt("volumes.inUse") : tt("volumes.free") }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.owner')" width="110">
        <template #default="{ row }"><span class="secondary-line">{{ displayNameForUsername(row.owner) || "—" }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" width="110" fixed="right">
        <template #default="{ row }">
          <el-button link icon="FolderOpened" :title="tt('volumes.browseFiles')" @click="browse(row)" />
          <el-button v-if="canMutate()" link icon="Delete" class="danger-text" :title="tt('volumes.delete')" @click="remove(row)" />
        </template>
      </el-table-column>
    </el-table>

    <!-- Phone-width rows: tap for the bottom action sheet. -->
    <action-sheet
      v-model:visible="sheet.visible"
      :title="sheet.row?.name || ''"
      :subtitle="sheetSubtitle"
      :items="[{ key: 'browse', label: tt('volumes.browseFiles'), icon: 'FolderOpened' }, { key: 'delete', label: tt('volumes.delete'), icon: 'Delete', danger: true, disabled: !canMutate() }]"
      :columns="4"
      @select="onSheetSelect"
    />

    <el-dialog v-model="dialog" :title="tt('volumes.newTitle')" width="420px" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item required>
          <el-input v-model="form.name" :placeholder="tt('volumes.namePlaceholder')" />
        </el-form-item>
        <el-form-item>
          <el-select v-model="form.driver" style="width: 100%">
            <el-option value="local" label="local" />
            <el-option value="nfs" label="nfs" />
          </el-select>
        </el-form-item>
        <p class="hint">{{ tt("volumes.ownedHint") }}</p>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submit">{{ tt("common.create") }}</el-button>
      </template>
    </el-dialog>

    <volume-files-dialog v-model:visible="files.visible" :name="files.name" :display="files.display" />
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, canMutate, refreshSection, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import VolumeFilesDialog from "@/components/volume/VolumeFilesDialog.vue";
import ActionSheet from "@/components/ActionSheet.vue";

export default {
  name: "Volumes",
  components: { ActionSheet, VolumeFilesDialog },
  data() {
    return {
      s: store,
      dialog: false,
      form: { name: "", driver: "local" },
      files: { visible: false, name: "", display: "" },
      sheet: { visible: false, row: null },
    };
  },
  computed: {
    sheetSubtitle() {
      const r = this.sheet.row;
      if (!r) return "";
      return `${r.driver || "local"} · ${this.fmtMB(r.sizeMb)} · ${r.inUse ? tt("volumes.inUse") : tt("volumes.free")}`;
    },
  },
  methods: {
    tt,
    canMutate,
    displayNameForUsername,
    onRowClick(row) {
      if (!store.isMobile) return;
      this.sheet = { visible: true, row };
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      if (!row) return;
      if (item.key === "browse") this.browse(row);
      else if (item.key === "delete" && canMutate()) this.remove(row);
    },
    fmtMB(mb) {
      if (!mb || mb <= 0) return "0 MB";
      if (mb < 1024) return `${Math.round(mb)} MB`;
      return `${(mb / 1024).toFixed(1)} GB`;
    },
    browse(v) {
      this.files = { visible: true, name: v.fullName || v.name, display: v.name };
    },
    async submit() {
      if (!this.form.name.trim()) return;
      try {
        await api("/api/volumes", { method: "POST", body: JSON.stringify({ name: this.form.name.trim(), driver: this.form.driver || "local" }) });
        await refreshSection("volumes");
        this.dialog = false;
        ElMessage.success(tt("volumes.created"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async remove(v) {
      try {
        await ElMessageBox.confirm(tt("volumes.deleteConfirm", { name: v.name }), tt("volumes.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/volumes/delete", { method: "POST", body: JSON.stringify({ name: v.fullName || v.name, force: false }) });
        await refreshSection("volumes");
        ElMessage.success(tt("volumes.deleted"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async prune() {
      try {
        await ElMessageBox.confirm(tt("volumes.pruneConfirm"), tt("volumes.pruneUnused"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        const r = await api("/api/volumes/prune", { method: "POST" });
        await refreshSection("volumes");
        ElMessage.success(tt("volumes.pruneResult", { n: r.removed || 0, size: this.fmtMB((r.bytesFreed || 0) / 1024 / 1024) }));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
.card-head { display: flex; align-items: center; margin-bottom: 12px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.head-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
</style>
