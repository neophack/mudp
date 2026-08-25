<template>
  <div>
    <div class="toolbar">
      <el-input
        v-model="s.search"
        :placeholder="tt('action.searchContainers')"
        prefix-icon="Search"
        clearable
        style="width: 240px"
      />
      <el-button v-if="canMutate()" type="primary" @click="createVisible = true">{{ tt("action.newContainer") }}</el-button>
      <!-- State filter pills, pushed right. Counts come from the full list so they stay stable. -->
      <div class="filter-bar">
        <button
          v-for="f in filters"
          :key="f.key"
          class="pill"
          :class="{ active: s.containerFilter === f.key }"
          @click="s.containerFilter = f.key"
        >{{ f.label }} <span class="pill-count">{{ f.n }}</span></button>
      </div>
    </div>

    <!-- Batch toolbar appears when one or more containers are selected. -->
    <div v-if="selected.size > 0" class="batch-bar">
      <span class="batch-count">{{ tt("containers.selectedN", { n: selected.size }) }}</span>
      <div v-if="canMutate()" class="batch-actions">
        <el-button size="small" @click="runBatch('start')">{{ tt("containers.batchStart") }}</el-button>
        <el-button size="small" @click="runBatch('stop')">{{ tt("containers.batchStop") }}</el-button>
        <el-button size="small" @click="runBatch('restart')">{{ tt("containers.batchRestart") }}</el-button>
        <el-button size="small" @click="runBatch('unpause')">{{ tt("containers.batchUnpause") }}</el-button>
        <el-button size="small" type="danger" plain @click="runBatch('remove')">{{ tt("containers.batchRemove") }}</el-button>
      </div>
    </div>

    <div class="card">
      <el-table
        :data="filtered"
        size="small"
        row-key="id"
        :empty-text="tt('containers.noMatch')"
        :row-class-name="s.isMobile ? 'row-tappable' : ''"
        @selection-change="onSelectionChange"
        @row-click="onRowClick"
      >
        <el-table-column v-if="canMutate() && !s.isMobile" type="selection" width="36" :selectable="() => true" reserve-selection />
        <el-table-column :label="tt('containers.colContainer')" :min-width="s.isMobile ? 150 : 200">
          <template #default="{ row }">
            <div class="primary-line">
              {{ row.name || row.fullName }}
              <el-tag v-if="row.forwarded" size="small" type="info" :title="tt('containers.forwardBadgeTitle')">{{ tt("containers.forwardBadge") }}</el-tag>
            </div>
            <div class="secondary-line port-line">
              <template v-for="(l, i) in portLinks(row)" :key="i">
                <span v-if="i" class="sep">·</span>
                <a class="port-link" :href="l.url" target="_blank" rel="noopener" @click.stop>{{ l.text }}</a>
              </template>
              <span v-if="portText(row)">
                <span v-if="portLinks(row).length" class="sep">·</span>{{ portText(row) }}
              </span>
              <span v-else-if="!portLinks(row).length">—</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column v-if="isAdmin() && !s.isMobile" :label="tt('containers.colOwner')" width="110">
          <template #default="{ row }">{{ displayNameForUsername(row.owner) || "—" }}</template>
        </el-table-column>
        <el-table-column :label="tt('containers.colStatus')" :width="s.isMobile ? 92 : 185">
          <template #default="{ row }">
            <el-tag size="small" :type="stateTag(row)" :title="row.status || stateLabel(row)">
              <span class="tag-dot"></span>{{ s.isMobile ? stateLabel(row) : (row.status || stateLabel(row)) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('containers.colImage')" min-width="140">
          <template #default="{ row }"><span class="secondary-line">{{ row.image || row.Image || "—" }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('containers.colResources')" width="160">
          <template #default="{ row }">
            <div class="secondary-line">{{ tt("containers.memDisk", { mem: num(row.memoryMb).toFixed(0), disk: num(row.diskMb).toFixed(0) }) }}</div>
            <div class="secondary-line">{{ tt("containers.gpuLine", { gpu: row.gpu || "none" }) }}</div>
          </template>
        </el-table-column>
        <!-- Icon-only actions on a single line; the fixed column is sized from
             the widest action set on screen (a running row carries the most). -->
        <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" :width="actionsColWidth" fixed="right" class-name="actions-col">
          <template #default="{ row }">
            <div class="row-actions">
              <el-button
                v-for="a in rowActions(row)"
                :key="a.key"
                link
                class="row-action-btn"
                :class="[a.tone ? a.tone + '-text' : '', a.danger ? 'danger-text' : '']"
                :icon="a.icon"
                :disabled="a.disabled"
                :title="a.label"
                :aria-label="a.label"
                @click="runRowAction(a, row)"
              />
            </div>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Phone-width rows: tap a row to get every action in a bottom sheet. -->
    <action-sheet
      v-model:visible="sheet.visible"
      :title="sheet.row?.name || sheet.row?.fullName || ''"
      :subtitle="sheetSubtitle"
      :items="sheetItems"
      :columns="4"
      @select="onSheetSelect"
    >
      <template #meta>
        <div class="sheet-meta">
          <div v-if="isAdmin() && sheet.row?.owner" class="sheet-meta-line">{{ tt("containers.colOwner") }}: {{ displayNameForUsername(sheet.row.owner) }}</div>
          <div v-if="sheet.row?.image || sheet.row?.Image" class="sheet-meta-line">{{ tt("containers.colImage") }}: <span class="mono">{{ sheet.row.image || sheet.row.Image }}</span></div>
          <div v-if="sheet.row" class="sheet-meta-line">{{ tt("containers.memDisk", { mem: num(sheet.row.memoryMb).toFixed(0), disk: num(sheet.row.diskMb).toFixed(0) }) }} · {{ tt("containers.gpuLine", { gpu: sheet.row.gpu || "none" }) }}</div>
        </div>
      </template>
    </action-sheet>

    <create-dialog v-model:visible="createVisible" />
    <logs-dialog v-model:visible="logs.visible" :id="logs.id" :title="logs.title" />
    <terminal-dialog v-model:visible="term.visible" :id="term.id" :title="term.title" />
    <stats-dialog v-model:visible="stats.visible" :id="stats.id" :title="stats.title" />
    <details-dialog v-model:visible="det.visible" :id="det.id" :title="det.title" />
    <files-dialog v-model:visible="files.visible" :id="files.id" :title="files.title" />
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, refreshAll, isAdmin, canMutate, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import ActionSheet from "@/components/ActionSheet.vue";
import CreateDialog from "@/components/container/CreateDialog.vue";
import LogsDialog from "@/components/container/LogsDialog.vue";
import TerminalDialog from "@/components/container/TerminalDialog.vue";
import StatsDialog from "@/components/container/StatsDialog.vue";
import DetailsDialog from "@/components/container/DetailsDialog.vue";
import FilesDialog from "@/components/container/FilesDialog.vue";

function matchesAnyId(fullId, prefixes) {
  return prefixes.some((p) => fullId === p || (fullId && fullId.startsWith(p)));
}

export default {
  name: "Containers",
  components: { ActionSheet, CreateDialog, LogsDialog, TerminalDialog, StatsDialog, DetailsDialog, FilesDialog },
  data() {
    return {
      s: store,
      selected: new Set(),
      createVisible: false,
      logs: { visible: false, id: "", title: "" },
      term: { visible: false, id: "", title: "" },
      stats: { visible: false, id: "", title: "" },
      det: { visible: false, id: "", title: "" },
      files: { visible: false, id: "", title: "" },
      sheet: { visible: false, row: null },
      pendingActions: new Set(),
    };
  },
  computed: {
    byState() {
      const f = store.containerFilter || "all";
      const list = store.containers || [];
      if (f === "all") return list;
      return list.filter((c) => {
        const s = (c.state || "").toLowerCase();
        if (f === "running") return s === "running";
        if (f === "paused") return s === "paused";
        if (f === "stopped") return s !== "running" && s !== "paused";
        return true;
      });
    },
    sheetSubtitle() {
      const r = this.sheet.row;
      if (!r) return "";
      return r.status || this.stateLabel(r);
    },
    sheetItems() {
      return this.rowActions(this.sheet.row);
    },
    // Ten icons at most (a running container); size the fixed column to the
    // widest set actually rendered so the icons never wrap onto a second line.
    actionsColWidth() {
      let n = 1;
      for (const row of this.filtered) n = Math.max(n, this.rowActions(row).length);
      return n * 26 + (n - 1) * 2 + 24;
    },
    filtered() {
      const q = (store.search || "").trim().toLowerCase();
      if (!q) return this.byState;
      return this.byState.filter(
        (c) =>
          (c.name || c.fullName || "").toLowerCase().includes(q) ||
          (c.image || "").toLowerCase().includes(q)
      );
    },
    filters() {
      const all = store.containers || [];
      const count = (pred) => all.filter(pred).length;
      return [
        { key: "all", label: tt("containers.filterAll"), n: all.length },
        { key: "running", label: tt("containers.filterRunning"), n: count((c) => c.state === "running") },
        { key: "stopped", label: tt("containers.filterStopped"), n: count((c) => c.state !== "running" && c.state !== "paused") },
        { key: "paused", label: tt("containers.filterPaused"), n: count((c) => c.state === "paused") },
      ];
    },
  },
  methods: {
    tt,
    isAdmin,
    canMutate,
    displayNameForUsername,
    num(v) {
      return typeof v === "number" && isFinite(v) ? v : 0;
    },
    pending(row, act) {
      return this.pendingActions.has(row.id + ":" + act);
    },
    stateTag(c) {
      return c.state === "running" ? "success" : c.state === "paused" ? "warning" : "info";
    },
    stateLabel(c) {
      if (c.state === "running") return tt("containers.up");
      if (c.state === "paused") return tt("containers.paused");
      return tt("containers.stopped");
    },
    // Quick links for the mudp-relayed 8080/8090 HTTP(S) endpoints. Docker on
    // Linux binds to 0.0.0.0 so the backend URL contains 127.0.0.1; replace it
    // with the hostname the browser is already connected to.
    portLinks(c) {
      const fixUrl = (url) => url ? url.replace(/^(https?:\/\/)(127\.0\.0\.1|0\.0\.0\.0|::1|localhost)(:\d+)/, `$1${location.hostname}$3`) : url;
      const out = [];
      if (c.http8080Url) out.push({ url: fixUrl(c.http8080Url), text: "8080 ↗" });
      if (c.http8090Url) out.push({ url: fixUrl(c.http8090Url), text: "8090 ↗" });
      if (c.https8080Url) out.push({ url: fixUrl(c.https8080Url), text: "8080 🔒↗" });
      if (c.https8090Url) out.push({ url: fixUrl(c.https8090Url), text: "8090 🔒↗" });
      return out;
    },
    // The published port mappings, as Docker reports them.
    portText(c) {
      return (c.ports || []).join(", ");
    },
    onSelectionChange(rows) {
      this.selected = new Set(rows.map((r) => r.id));
    },
    onRowClick(row) {
      if (!store.isMobile) return;
      this.sheet = { visible: true, row };
    },
    // One action list per row, shared by the desktop icon column and the phone
    // action sheet so the two surfaces can never drift apart.
    rowActions(r) {
      if (!r) return [];
      const running = r.state === "running";
      const paused = r.state === "paused";
      const items = [
        { key: "logs", label: tt("containers.actLogs"), icon: "Document" },
        { key: "start", label: tt("containers.actStart"), icon: "VideoPlay", tone: "ok", disabled: running || this.pending(r, "start") },
        { key: "stop", label: tt("containers.actStop"), icon: "VideoPause", tone: "warn", disabled: (!running && !paused) || this.pending(r, "stop") },
        { key: "restart", label: tt("containers.actRestart"), icon: "Refresh", disabled: (!running && !paused) || this.pending(r, "restart") },
      ];
      if (running) items.push({ key: "pause", label: tt("containers.actPause"), icon: "Remove", disabled: this.pending(r, "pause") });
      if (paused) items.push({ key: "unpause", label: tt("containers.actUnpause"), icon: "VideoPlay", tone: "ok", disabled: this.pending(r, "unpause") });
      items.push({ key: "files", label: tt("containers.actFiles"), icon: "FolderOpened" });
      if (running) {
        items.push({ key: "terminal", label: tt("containers.actConsole"), icon: "Monitor" });
        items.push({ key: "stats", label: tt("containers.actStats"), icon: "DataLine" });
      }
      items.push({ key: "inspect", label: tt("containers.actDetails"), icon: "InfoFilled" });
      items.push({ key: "remove", label: tt("containers.actDelete"), icon: "Delete", danger: true, disabled: this.pending(r, "remove") });
      return items;
    },
    runRowAction(item, row) {
      if (!row) return;
      if (["start", "stop", "restart", "pause", "unpause", "remove"].includes(item.key)) {
        this.action(row, item.key);
      } else {
        this.open(item.key, row);
      }
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      this.runRowAction(item, row);
    },
    open(kind, row) {
      const id = row.id;
      const title = row.name || row.fullName;
      if (kind === "logs") this.logs = { visible: true, id, title };
      else if (kind === "terminal") this.term = { visible: true, id, title };
      else if (kind === "stats") this.stats = { visible: true, id, title };
      else if (kind === "inspect") this.det = { visible: true, id, title };
      else if (kind === "files") this.files = { visible: true, id, title };
    },
    async action(row, action) {
      if (action === "remove") {
        try {
          await ElMessageBox.confirm(tt("containers.confirmDeleteOne", { name: row.name || row.fullName }), tt("common.actions"), {
            confirmButtonText: tt("common.confirm"),
            cancelButtonText: tt("common.cancel"),
            type: "warning",
          });
        } catch { return; }
      }
      const id = row.id;
      const key = id + ":" + action;
      this.pendingActions.add(key);
      try {
        await api("/api/containers/action", { method: "POST", body: JSON.stringify({ id, action }) });
        if (action === "remove") {
          store.containers = (store.containers || []).filter((c) => c.id !== id && !(c.id && c.id.startsWith(id)));
        }
        await refreshAll();
        ElMessage.success(tt("containers.done"));
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.pendingActions.delete(key);
      }
    },
    async runBatch(action) {
      const ids = [...this.selected];
      if (ids.length === 0) return;
      if (action === "remove") {
        try {
          await ElMessageBox.confirm(tt("containers.confirmRemoveN", { n: ids.length }), tt("common.actions"), {
            confirmButtonText: tt("common.confirm"),
            cancelButtonText: tt("common.cancel"),
            type: "warning",
          });
        } catch { return; }
      }
      try {
        const res = await api("/api/containers/batch", {
          method: "POST",
          body: JSON.stringify({ ids, action }),
        });
        if (action === "remove") {
          store.containers = (store.containers || []).filter((c) => !matchesAnyId(c.id, ids));
        }
        await refreshAll();
        this.selected = new Set();
        const okN = (res.ok || []).length;
        const failN = (res.failed || []).length;
        if (failN === 0) ElMessage.success(tt("containers.batchResult", { ok: okN, action }));
        else ElMessage.warning(tt("containers.batchResultPartial", { ok: okN, fail: failN }));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
.filter-bar { display: flex; gap: 8px; flex-wrap: wrap; margin-left: auto; }
.pill {
  border: 1px solid var(--line);
  background: var(--card);
  border-radius: 16px;
  padding: 5px 14px;
  font-size: 12.5px;
  cursor: pointer;
  color: var(--muted);
}
.pill.active { border-color: var(--brand); color: var(--brand); background: var(--brand-tint); }
.pill-count { opacity: 0.7; margin-left: 4px; }
.batch-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px 14px;
  background: var(--brand-tint);
  border: 1px solid var(--brand-tint-line);
  border-radius: 10px;
  padding: 8px 14px;
  margin-bottom: 12px;
}
.batch-actions { display: flex; flex-wrap: wrap; gap: 6px; }
.batch-actions .el-button { margin-left: 0; }
.batch-count { font-weight: 600; color: var(--brand); font-size: 13px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.port-link { color: var(--brand); }
.sep { margin: 0 4px; color: var(--muted); }
.ok-text { color: #10b981 !important; }
.warn-text { color: #e6a23c !important; }
.tag-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
.sheet-meta { display: flex; flex-direction: column; gap: 3px; margin: 8px 0 12px; }
.sheet-meta-line { color: var(--muted); font-size: 12px; word-break: break-word; }
</style>
