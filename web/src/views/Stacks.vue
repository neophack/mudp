<template>
  <div class="card">
    <div class="card-head">
      <h2>{{ tt("stacks.title") }}</h2>
      <el-button v-if="canMutate()" type="primary" size="small" @click="openEditor(null)">{{ tt("stacks.newStack") }}</el-button>
    </div>
    <el-table
      :data="s.stacks"
      size="small"
      :empty-text="tt('stacks.noStacksCreate')"
      :row-class-name="s.isMobile ? 'row-tappable' : ''"
      @row-click="onRowClick"
    >
      <el-table-column :label="tt('common.name')" :min-width="s.isMobile ? 150 : 200">
        <template #default="{ row }">
          <div class="primary-line">{{ row.name }}</div>
          <div class="secondary-line mono">{{ row.projectName }}</div>
        </template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('stacks.colServices')" width="90">
        <template #default="{ row }">{{ row.services || 0 }}</template>
      </el-table-column>
      <el-table-column :label="tt('common.status')" :width="s.isMobile ? 92 : 130">
        <template #default="{ row }">
          <el-tag size="small" :type="statusTag(row)">
            <span class="tag-dot"></span>{{ statusText(row) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.owner')" width="110">
        <template #default="{ row }"><span class="secondary-line">{{ displayNameForUsername(row.owner) || "—" }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('stacks.colUpdated')" width="160">
        <template #default="{ row }"><span class="secondary-line">{{ fmtTime(row.updatedAt) }}</span></template>
      </el-table-column>
      <el-table-column v-if="canMutate() && !s.isMobile" :label="tt('common.actions')" width="170" fixed="right">
        <template #default="{ row }">
          <el-button link icon="VideoPlay" class="ok-text" :title="tt('stacks.up')" @click="deploy(row)" />
          <el-button link icon="SwitchButton" class="warn-text" :title="tt('stacks.down')" @click="down(row)" />
          <el-button link icon="Edit" :title="tt('stacks.edit')" @click="loadAndEdit(row)" />
          <el-button link icon="Delete" class="danger-text" :title="tt('stacks.delete')" @click="remove(row)" />
        </template>
      </el-table-column>
    </el-table>

    <!-- Phone-width rows: tap for the bottom action sheet. -->
    <action-sheet
      v-model:visible="sheet.visible"
      :title="sheet.row?.name || ''"
      :subtitle="sheetSubtitle"
      :items="sheetItems"
      :columns="4"
      @select="onSheetSelect"
    />

    <!-- Editor -->
    <el-dialog v-model="editor.visible" :title="editor.isNew ? tt('stacks.newTitle') : tt('stacks.editTitle', { name: editor.name })" width="720px" top="4vh" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item required>
          <el-input v-model="editor.name" :placeholder="tt('stacks.namePlaceholder')" :disabled="!editor.isNew" />
        </el-form-item>
        <el-form-item :label="tt('stacks.envLabel')">
          <el-input v-model="editor.env" type="textarea" :rows="4" class="mono" spellcheck="false" />
        </el-form-item>
        <el-form-item :label="tt('stacks.composeLabel')">
          <el-input v-model="editor.composeYaml" type="textarea" :rows="14" class="mono" spellcheck="false" />
        </el-form-item>
        <p class="hint">{{ tt("stacks.composeHint") }}</p>
      </el-form>
      <template #footer>
        <el-button @click="editor.visible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" :loading="editor.saving" @click="save">{{ tt("common.save") }}</el-button>
      </template>
    </el-dialog>

    <stack-run-dialog v-model:visible="run.visible" :title="run.title" />
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api, readCSRFCookie, readSSE } from "@/api";
import { store, canMutate, refreshSection, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import { registerJob } from "@/jobs";
import { stackRun } from "@/components/stack/stackRun";
import StackRunDialog from "@/components/stack/StackRunDialog.vue";
import ActionSheet from "@/components/ActionSheet.vue";

const SAMPLE = `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: \${DB_PASSWORD}
`;

function envJSONToText(json) {
  try {
    const obj = JSON.parse(json || "{}");
    return Object.entries(obj).map(([k, v]) => `${k}=${v}`).join("\n");
  } catch {
    return "";
  }
}

export default {
  name: "Stacks",
  components: { ActionSheet, StackRunDialog },
  data() {
    return {
      s: store,
      editor: { visible: false, isNew: true, id: 0, name: "", env: "", composeYaml: "", saving: false },
      run: { visible: false, title: "" },
      sheet: { visible: false, row: null },
    };
  },
  computed: {
    sheetSubtitle() {
      const r = this.sheet.row;
      if (!r) return "";
      return `${this.statusText(r)}${r.updatedAt ? " · " + this.fmtTime(r.updatedAt) : ""}`;
    },
    sheetItems() {
      if (!canMutate() || !this.sheet.row) return [];
      return [
        { key: "up", label: tt("stacks.up"), icon: "VideoPlay" },
        { key: "down", label: tt("stacks.down"), icon: "SwitchButton" },
        { key: "edit", label: tt("stacks.edit"), icon: "Edit" },
        { key: "delete", label: tt("stacks.delete"), icon: "Delete", danger: true },
      ];
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
      if (!row || !canMutate()) return;
      if (item.key === "up") this.deploy(row);
      else if (item.key === "down") this.down(row);
      else if (item.key === "edit") this.loadAndEdit(row);
      else if (item.key === "delete") this.remove(row);
    },
    statusTag(s) {
      const allRunning = s.services > 0 && s.running === s.services;
      const someRunning = s.running > 0 && !allRunning;
      return allRunning ? "success" : someRunning ? "warning" : "info";
    },
    statusText(s) {
      if (s.services > 0) return `${s.running}/${s.services} up`;
      return "—";
    },
    fmtTime(iso) {
      if (!iso) return "—";
      const t = new Date(iso);
      return isNaN(t) ? iso : t.toLocaleString();
    },
    openEditor(stack) {
      this.editor = {
        visible: true,
        isNew: !stack,
        id: stack?.id || 0,
        name: stack?.name || "",
        env: stack ? envJSONToText(stack.envJson) : "DB_PASSWORD=change-me\n",
        composeYaml: stack?.composeYaml ?? SAMPLE,
        saving: false,
      };
    },
    async loadAndEdit(row) {
      try {
        const s = await api("/api/stacks/get?id=" + row.id);
        this.openEditor(s);
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async save() {
      const e = this.editor;
      if (e.isNew && !e.name.trim()) return;
      const payload = { composeYaml: e.composeYaml, env: e.env };
      if (e.isNew) payload.name = e.name.trim();
      else payload.id = e.id;
      e.saving = true;
      try {
        const r = await api("/api/stacks", { method: "POST", body: JSON.stringify(payload) });
        await refreshSection("stacks");
        e.visible = false;
        ElMessage.success(e.isNew ? tt("stacks.stackSaved") : tt("stacks.stackUpdated"));
        if (e.isNew && r.id) this.runDeploy(r.id);
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        e.saving = false;
      }
    },
    deploy(row) {
      this.runDeploy(row.id);
    },
    async down(row) {
      try {
        await ElMessageBox.confirm(tt("stacks.downConfirm"), tt("stacks.down"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      this.runDown(row.id);
    },
    async remove(row) {
      try {
        await ElMessageBox.confirm(tt("stacks.deleteConfirm", { name: row.name }), tt("stacks.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/stacks/delete", { method: "POST", body: JSON.stringify({ id: row.id }) });
        await refreshSection("stacks", "containers");
        ElMessage.success(tt("stacks.deleted"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    runDeploy(id) {
      // Reopen the existing terminal if the same stack is still deploying.
      if (stackRun.active && stackRun.stackId === id && stackRun.verb === "up") {
        this.run = { visible: true, title: tt("stacks.deployTitle") };
        return;
      }
      Object.assign(stackRun, { active: true, stackId: id, verb: "up", lines: [], error: "", done: false });
      this.run = { visible: true, title: tt("stacks.deployTitle") };
      this.runStackStream(id, "up", "deployed");
    },
    runDown(id) {
      if (stackRun.active && stackRun.stackId === id && stackRun.verb === "down") {
        this.run = { visible: true, title: tt("stacks.downTitle") };
        return;
      }
      Object.assign(stackRun, { active: true, stackId: id, verb: "down", lines: [], error: "", done: false });
      this.run = { visible: true, title: tt("stacks.downTitle") };
      this.runStackStream(id, "down", "torn down");
    },
    async runStackStream(id, verb) {
      const stack = (store.stacks || []).find((x) => x.id === id);
      const stackName = stack?.name || `stack ${id}`;
      const job = registerJob({ kind: `stack.${verb}`, name: stackName });
      const isActive = () => stackRun.stackId === id && stackRun.verb === verb;
      try {
        const res = await fetch(`/api/stacks/${verb}/stream`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json", Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || store.csrfToken || "" },
          body: JSON.stringify({ id }),
          signal: job.signal,
        });
        if (!res.ok) {
          const data = await res.json().catch(() => ({}));
          const verbKey = verb === "up" ? "stacks.deployFailed" : "stacks.downFailed";
          const msg = data.error || tt(verbKey, { status: res.status });
          job.error(msg);
          if (isActive()) {
            stackRun.lines.push(msg);
            stackRun.error = msg;
            stackRun.active = false;
            stackRun.done = true;
          }
          ElMessage.error(msg);
          return;
        }
        await readSSE(res, (event, data) => {
          // Ignore stale output if the user switched to a different stack/verb.
          if (!isActive()) return;
          if (event === "progress") {
            const line = data.message || "";
            stackRun.lines.push(line);
            job.log(line);
          } else if (event === "error") {
            const msg = data.message || tt("stacks.cancelled").toLowerCase();
            stackRun.lines.push(`[error] ${msg}`);
            job.error(msg);
            stackRun.error = msg;
            stackRun.active = false;
            stackRun.done = true;
            ElMessage.error(msg);
          } else if (event === "done" || event === "cancelled") {
            const ok = event === "done";
            stackRun.lines.push(`[${event}] ${data.message || ""}`);
            if (ok) job.done(data.message || tt("stacks.complete"));
            else job.cancel();
            stackRun.active = false;
            stackRun.done = true;
            ElMessage.success(ok ? tt("stacks.stackDeployed") : tt("stacks.stackDown"));
            refreshSection("stacks", "containers");
            if (ok) {
              setTimeout(() => {
                if (stackRun.stackId === id && stackRun.verb === verb) {
                  this.run.visible = false;
                }
              }, 1200);
            }
          }
        });
      } catch (err) {
        if (job.signal.aborted) {
          job.cancel();
          if (isActive()) {
            const msg = tt("stacks.cancelled");
            stackRun.lines.push(`[cancelled] ${msg}`);
            stackRun.error = msg;
            stackRun.active = false;
            stackRun.done = true;
          }
        } else {
          job.error(err.message);
          if (isActive()) {
            stackRun.lines.push(err.message);
            stackRun.error = err.message;
            stackRun.active = false;
            stackRun.done = true;
          }
          ElMessage.error(err.message);
        }
      }
    },
  },
};
</script>

<style scoped>
.card-head { display: flex; align-items: center; margin-bottom: 12px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.tag-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
.ok-text { color: #10b981 !important; }
.warn-text { color: #e6a23c !important; }
</style>
