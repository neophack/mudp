<template>
  <el-dialog :model-value="visible" :title="tt('details.title', { name: title })" width="720px" top="4vh" append-to-body @update:model-value="onVisible">
    <div v-if="!loaded" class="empty-state">{{ tt("common.loadingDots") }}</div>
    <template v-else-if="i">
      <dl class="detail">
        <dt>{{ tt("details.colName") }}</dt><dd>{{ i.name }}</dd>
        <dt>{{ tt("details.colId") }}</dt><dd class="mono">{{ i.id }}</dd>
        <dt>{{ tt("details.colState") }}</dt>
        <dd>
          <el-tag size="small" :type="i.state === 'running' ? 'success' : 'info'">{{ i.state }}</el-tag>
        </dd>
        <dt>{{ tt("details.colImage") }}</dt><dd>{{ i.imageName || i.image }}</dd>
        <dt>{{ tt("details.colCreated") }}</dt><dd>{{ i.createdAt ? new Date(i.createdAt * 1000).toLocaleString() : "-" }}</dd>
        <dt>{{ tt("details.colGpu") }}</dt><dd>{{ i.gpu || "none" }}</dd>
        <dt>{{ tt("details.colRunAs") }}</dt><dd>{{ i.user || "image default" }}</dd>
        <dt>{{ tt("details.colIp") }}</dt><dd>{{ i.ipAddress || "-" }}</dd>
        <dt>{{ tt("details.colEntrypoint") }}</dt><dd class="mono">{{ (i.entrypoint || []).join(" ") || "-" }}</dd>
        <dt>{{ tt("details.colCommand") }}</dt><dd class="mono">{{ (i.cmd || []).join(" ") || "-" }}</dd>
        <dt>{{ tt("details.colPorts") }}</dt>
        <dd>
          <template v-if="(i.ports || []).length">
            <span v-for="(p, idx) in i.ports" :key="idx">
              {{ p.hostPort ? `${p.hostPort}:${p.privatePort}/${p.type}` : `${p.privatePort}/${p.type}` }}
              <!-- A forwarded port is relayed by mudp rather than published by
                   Docker; `docker ps` will not show it, so say so here. -->
              <span v-if="p.forwarded" class="hint">(mudp forward)</span>
              <span v-if="idx < i.ports.length - 1">, </span>
            </span>
          </template>
          <template v-else>-</template>
        </dd>
        <dt>{{ tt("details.colMounts") }}</dt>
        <dd>{{ (i.mounts || []).map((m) => `${m.source} → ${m.target} (${m.type}, ${m.readOnly ? "ro" : "rw"})`).join(", ") || "-" }}</dd>
        <dt>{{ tt("details.colEnvironment") }}</dt><dd class="mono detail-env">{{ (i.env || []).join("\n") || "-" }}</dd>
      </dl>

      <!-- Duplicate is available to any mutating role; commit is admin-only. -->
      <section v-if="canMutate()" class="card detail-settings">
        <div class="card-head"><h2>{{ tt("details.actions") }}</h2></div>
        <div class="page-actions">
          <el-button size="small" :title="tt('details.dupTitle')" @click="duplicate">{{ tt("details.duplicate") }}</el-button>
          <el-button v-if="isAdmin()" size="small" :title="tt('details.commitTitle')" @click="commit">{{ tt("details.commit") }}</el-button>
        </div>
      </section>

      <section class="card detail-settings">
        <div class="card-head">
          <h2>{{ tt("details.settings") }}</h2>
          <el-button v-if="canMutate()" type="primary" size="small" :loading="saving" @click="saveSettings">{{ tt("common.save") }}</el-button>
        </div>
        <div class="field-label">{{ tt("create.restartPolicy") }}</div>
        <el-select v-if="canMutate()" v-model="editRestart" size="small" style="width: 100%">
          <el-option value="unless-stopped" :label="tt('create.policyUnlessStopped')" />
          <el-option value="always" :label="tt('create.policyAlways')" />
          <el-option value="on-failure" :label="tt('create.policyOnFailure')" />
          <el-option value="no" :label="tt('create.policyNo')" />
        </el-select>
        <div v-else>{{ i.restartPolicy || "-" }}</div>
        <div class="field-label" style="margin-top: 12px">{{ tt("create.networks") }}</div>
        <div v-if="availNetworks.length" class="check-grid">
          <label v-for="n in availNetworks" :key="n.fullName || n.name" class="check" :class="{ locked: isLocked(n) }">
            <input
              v-model="editNetworks"
              type="checkbox"
              :value="n.fullName || n.name"
              :disabled="!canMutate() || isLocked(n)"
            />
            {{ n.name }}
            <span v-if="originOf(n)" class="hint">({{ originOf(n) }})</span>
            <span v-if="isLocked(n)" class="hint">· {{ tt("create.networkNotInPool") }}</span>
          </label>
        </div>
        <p v-else class="hint">{{ tt("details.noCustomNets") }}</p>
        <p class="hint" style="margin-top: 8px">{{ tt("details.netHint") }}</p>
      </section>

      <!-- Collapsed block that lazily loads Docker's full inspect JSON. -->
      <section class="card detail-settings">
        <el-collapse @change="onRawExpand">
          <el-collapse-item :title="tt('details.rawJson')" name="raw">
            <p v-if="!rawJson" class="hint">{{ tt("details.rawHint") }}</p>
            <pre v-else class="log-output raw-json">{{ rawJson }}</pre>
          </el-collapse-item>
        </el-collapse>
      </section>
    </template>
    <div v-else class="error-box">✗ {{ error }}</div>
  </el-dialog>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, isAdmin, canMutate, refreshSection } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "DetailsDialog",
  props: {
    visible: { type: Boolean, default: false },
    id: { type: String, default: "" },
    title: { type: String, default: "" },
  },
  data() {
    return {
      i: null,
      error: "",
      loaded: false,
      editRestart: "unless-stopped",
      editNetworks: [],
      saving: false,
      rawJson: "",
    };
  },
  computed: {
    // The image's admin-defined selectable-network pool: when set, networks
    // outside it can't be newly attached — mirroring the create form.
    pool() {
      const img = (store.images || []).find((im) => im.name === (this.i?.imageName || this.i?.image));
      const p = img && img.preset;
      return p && p.selectableNetworks && p.selectableNetworks.length ? new Set(p.selectableNetworks) : null;
    },
    availNetworks() {
      return (store.networks || []).filter((n) => n.attachable);
    },
  },
  watch: {
    visible(v) {
      if (v) this.load();
      else {
        this.i = null;
        this.rawJson = "";
      }
    },
  },
  methods: {
    tt,
    isAdmin,
    canMutate,
    onVisible(v) {
      this.$emit("update:visible", v);
    },
    async load() {
      this.loaded = false;
      this.error = "";
      try {
        const i = await api("/api/containers/inspect?id=" + encodeURIComponent(this.id));
        this.i = i;
        this.editRestart = (i.restartPolicy || "unless-stopped").toLowerCase();
        const current = new Set((i.networks || []).map((n) => n.name));
        this.editNetworks = this.availNetworks
          .filter((n) => current.has(n.name) || current.has(n.fullName || n.name))
          .map((n) => n.fullName || n.name);
      } catch (err) {
        this.error = err.message;
        this.i = null;
      } finally {
        this.loaded = true;
      }
    },
    originOf(n) {
      return n.system ? "system" : n.external ? "host" : n.shared ? "shared" : "";
    },
    isLocked(n) {
      return !!this.pool && !this.pool.has(n.fullName || n.name);
    },
    // Lazy-load the full inspect document on first expand, so the dialog stays
    // fast for the common case.
    async onRawExpand(names) {
      if (!names || !names.includes("raw") || this.rawJson) return;
      try {
        const res = await fetch("/api/containers/inspect/raw?id=" + encodeURIComponent(this.id), { credentials: "same-origin" });
        const text = await res.text();
        let pretty = text;
        try { pretty = JSON.stringify(JSON.parse(text), null, 2); } catch { /* not JSON */ }
        this.rawJson = pretty;
      } catch (err) {
        this.rawJson = "✗ " + err.message;
      }
    },
    async saveSettings() {
      this.saving = true;
      try {
        await api("/api/containers/update", {
          method: "POST",
          body: JSON.stringify({ id: this.id, restartPolicy: this.editRestart, networks: this.editNetworks }),
        });
        ElMessage.success(tt("details.settingsSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.saving = false;
      }
    },
    async duplicate() {
      const base = this.i?.name ? String(this.i.name).replace(/-?copy.*$/i, "") : "container";
      let newName;
      try {
        ({ value: newName } = await ElMessageBox.prompt(tt("details.dupPrompt"), tt("details.duplicate"), {
          inputValue: base + "-copy",
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      if (!newName || !newName.trim()) return;
      try {
        await api("/api/containers/duplicate", {
          method: "POST",
          body: JSON.stringify({ id: this.id, name: newName.trim() }),
        });
        await refreshSection("containers");
        ElMessage.success(tt("details.duplicated"));
        this.$emit("update:visible", false);
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async commit() {
      let name;
      let tag;
      try {
        ({ value: name } = await ElMessageBox.prompt(tt("details.commitPrompt"), tt("details.commit"), {
          inputValue: this.i?.imageName || "my-image",
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      if (!name || !name.trim()) return;
      try {
        ({ value: tag } = await ElMessageBox.prompt(tt("details.commitTagPrompt"), tt("details.commit"), {
          inputValue: "latest",
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { tag = "latest"; }
      try {
        const res = await api("/api/containers/commit", {
          method: "POST",
          body: JSON.stringify({ id: this.id, name: name.trim(), tag: (tag || "").trim(), comment: "" }),
        });
        ElMessage.success(tt("details.committedAs", { name: res.name }));
        await refreshSection("images");
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
dl.detail { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0 0 14px; font-size: 13px; }
dl.detail dt { color: var(--muted); }
dl.detail dd { margin: 0; word-break: break-word; }
.detail-env { white-space: pre-wrap; }
.detail-settings { margin-bottom: 12px; }
.card-head { display: flex; align-items: center; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 13.5px; flex: 1; }
.check-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 6px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.check.locked { opacity: 0.5; }
.field-label { font-size: 12.5px; color: #475569; font-weight: 600; margin-bottom: 6px; }
.raw-json { max-height: 320px; }
.log-output {
  background: #0f172a;
  color: #cbd5e1;
  border-radius: 8px;
  padding: 10px;
  font-size: 12px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0;
}
.error-box { background: #fef2f2; color: #b91c1c; border: 1px solid #fecaca; border-radius: 8px; padding: 10px 12px; }
</style>
