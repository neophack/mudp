<template>
  <div class="stack">
    <div v-if="data.warning" class="card">
      <div class="error-box">{{ tt("forwards.someNotRunning", { warn: data.warning }) }}</div>
    </div>

    <div class="card">
      <div class="card-head">
        <h2>{{ tt("forwards.activeForwards") }}</h2>
        <el-button type="primary" size="small" @click="openAdd">{{ tt("forwards.addForward") }}</el-button>
      </div>
      <p class="hint">{{ tt("forwards.hint") }}</p>
      <el-table
        :data="rules"
        size="small"
        :empty-text="tt('forwards.noRules')"
        :row-class-name="s.isMobile ? 'row-tappable' : ''"
        @row-click="onRowClick"
      >
        <el-table-column :label="tt('forwards.colHostPort')" :width="s.isMobile ? 96 : 110">
          <template #default="{ row }"><span class="primary-line mono">{{ row.hostPort }}/{{ row.proto }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('common.user')" width="110">
          <template #default="{ row }"><span class="secondary-line">{{ row.owner || "—" }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('containers.colContainer')" :min-width="s.isMobile ? 130 : 150">
          <template #default="{ row }">
            <div class="secondary-line">{{ row.name || "—" }}</div>
            <div v-if="row.note && row.note !== row.name" class="secondary-line hint">{{ row.note }}</div>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('forwards.colTarget')" width="150">
          <template #default="{ row }"><span class="secondary-line mono">{{ row.targetIp || "?" }}:{{ row.targetPort }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('forwards.colSource')" width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="row.source === 'manual' ? 'warning' : 'info'">{{ row.source === "manual" ? tt("forwards.manual") : tt("forwards.container") }}</el-tag>
            <el-tag v-if="row.requireLogin" size="small" type="success" :title="tt('forwards.requireLoginHint')">{{ tt("forwards.requireLogin") }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('forwards.colConnections')" width="110">
          <template #default="{ row }"><span class="secondary-line">{{ tt("forwards.connNow", { now: row.active ?? 0, total: row.total ?? 0 }) }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" width="120" fixed="right">
          <template #default="{ row }">
            <el-button v-if="row.source === 'manual' && row.manualId" link icon="Delete" class="danger-text" :title="tt('common.delete')" @click="remove(row)" />
            <span v-else class="hint">{{ tt("forwards.fromContainer") }}</span>
          </template>
        </el-table-column>
      </el-table>

      <!-- Phone-width rows: tap for the bottom action sheet. -->
      <action-sheet
        v-model:visible="sheet.visible"
        :title="sheet.row ? sheet.row.hostPort + '/' + sheet.row.proto : ''"
        :subtitle="sheetSubtitle"
        :items="[{ key: 'delete', label: tt('common.delete'), icon: 'Delete', danger: true, disabled: !(sheet.row?.source === 'manual' && sheet.row?.manualId) }]"
        :columns="4"
        @select="onSheetSelect"
      />
    </div>

    <div class="card">
      <div class="card-head">
        <h2>{{ tt("forwards.forwardingNetworks") }}</h2>
        <el-tag size="small" :type="networks.length ? 'success' : 'info'">
          {{ networks.length ? tt("forwards.nSelected", { n: networks.length }) : tt("forwards.none") }}
        </el-tag>
      </div>
      <p class="hint">{{ tt("forwards.netHint") }}</p>
      <div v-if="attachableNetworks.length" class="check-grid">
        <label v-for="n in attachableNetworks" :key="n.fullName || n.name" class="check">
          <input v-model="selectedNetworks" type="checkbox" :value="n.fullName || n.name" />
          {{ n.name }} <span class="hint">{{ n.subnet || n.driver || "" }}</span>
        </label>
      </div>
      <p v-else class="hint">{{ tt("forwards.noAttachable") }}</p>
      <el-input v-model="networksRaw" type="textarea" :rows="2" placeholder="e.g. openwrt-lan" />
      <el-button type="primary" size="small" style="margin-top: 10px" :loading="savingNets" @click="saveNetworks">{{ tt("forwards.saveNetworks") }}</el-button>
    </div>

    <div class="card">
      <div class="card-head">
        <h2>{{ tt("forwardAuth.title") }}</h2>
        <el-tag size="small" :type="auth.enabled ? 'success' : 'info'">{{ auth.enabled ? tt("forwardAuth.enabled") : tt("forwardAuth.disabled") }}</el-tag>
      </div>
      <p class="hint">{{ tt("forwardAuth.hint") }}</p>
      <label class="check"><input v-model="authForm.enabled" type="checkbox"> {{ tt("forwardAuth.enable") }}</label>
      <el-input v-model="authForm.consoleUrl" :placeholder="tt('forwardAuth.consoleUrlPlaceholder')" style="margin-top: 10px" />
      <p class="hint">{{ tt("forwardAuth.consoleUrlHint") }}</p>
      <el-button type="primary" size="small" :loading="savingAuth" @click="saveAuth">{{ tt("common.save") }}</el-button>
    </div>

    <el-dialog v-model="add.visible" :title="tt('forwards.addTitle')" width="480px" append-to-body>
      <p class="hint">{{ tt("forwards.addHint") }}</p>
      <div class="row2">
        <el-input v-model="add.form.hostPort" type="number" :placeholder="tt('forwards.hostPortPlaceholder')" />
        <el-select v-model="add.form.proto" style="width: 110px">
          <el-option value="tcp" label="TCP" />
          <el-option value="udp" label="UDP" />
        </el-select>
      </div>
      <div class="field-label" style="margin-top: 10px">{{ tt("forwards.targetContainer") }}</div>
      <el-select v-model="add.form.containerId" style="width: 100%" size="small">
        <el-option value="" :label="tt('forwards.fixedInstead')" />
        <el-option v-for="tg in targets" :key="tg.id" :value="tg.id" :label="targetLabel(tg)" />
      </el-select>
      <div class="row2" style="margin-top: 10px">
        <el-input v-model="add.form.targetIp" :placeholder="tt('forwards.targetIpPlaceholder')" />
        <el-input v-model="add.form.targetPort" type="number" :placeholder="tt('forwards.targetPortPlaceholder')" />
      </div>
      <el-input v-model="add.form.note" :placeholder="tt('forwards.notePlaceholder')" style="margin-top: 10px" />
      <!-- Login verification only works for TCP (the gate peeks an HTTP
           request), so the checkbox is disabled for UDP. -->
      <label class="check" style="margin-top: 10px" :class="{ locked: add.form.proto === 'udp' }">
        <input v-model="add.form.requireLogin" type="checkbox" :disabled="add.form.proto === 'udp'" />
        {{ tt("forwards.requireLogin") }} <span class="hint">{{ tt("forwards.requireLoginHint") }}</span>
      </label>
      <template #footer>
        <el-button @click="add.visible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submitAdd">{{ tt("forwards.addBtn") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store } from "@/store";
import { tt } from "@/i18n";
import ActionSheet from "@/components/ActionSheet.vue";

export default {
  name: "Forwards",
  components: { ActionSheet },
  data() {
    return {
      s: store,
      loaded: false,
      error: "",
      auth: { enabled: false, consoleUrl: "" },
      authForm: { enabled: false, consoleUrl: "" },
      selectedNetworks: [],
      networksRaw: "",
      savingNets: false,
      savingAuth: false,
      sheet: { visible: false, row: null },
      // el-dialog renders its slot (hidden) at mount, so the form must be a
      // real object from the start, not filled in lazily by openAdd().
      add: {
        visible: false,
        form: { hostPort: "", proto: "tcp", containerId: "", targetIp: "", targetPort: "", note: "", requireLogin: false },
      },
    };
  },
  computed: {
    data() {
      return store.forwards || { rules: [], networks: [], targets: [] };
    },
    rules() {
      return this.data.rules || [];
    },
    sheetSubtitle() {
      const r = this.sheet.row;
      if (!r) return "";
      return `${r.name || "—"} → ${r.targetIp || "?"}:${r.targetPort}`;
    },
    networks() {
      return this.data.networks || [];
    },
    targets() {
      return (this.data.targets || []).slice().sort((a, b) => (a.name || "").localeCompare(b.name || ""));
    },
    attachableNetworks() {
      return (store.networks || []).filter((n) => n.attachable);
    },
  },
  async mounted() {
    await this.reload();
    // Login-gating config is small and rarely changed; fetch once alongside
    // the page data so the settings card is ready without a flicker.
    try {
      this.auth = await api("/api/admin/forward/auth");
    } catch {
      this.auth = { enabled: false, consoleUrl: "" };
    }
    this.authForm = { ...this.auth };
    this.syncNetworkForm();
  },
  methods: {
    tt,
    onRowClick(row) {
      if (!store.isMobile) return;
      this.sheet = { visible: true, row };
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      if (!row || item.key !== "delete") return;
      if (row.source === "manual" && row.manualId) this.remove(row);
      else ElMessage.info(tt("forwards.fromContainer"));
    },
    // A failed fetch keeps the last view rather than blanking a page an admin
    // may be watching a live connection count on.
    async reload() {
      try {
        store.forwards = await api("/api/admin/forwards");
        this.syncNetworkForm();
        this.loaded = true;
      } catch (err) {
        this.error = err.message;
        store.forwards = store.forwards || { rules: [], networks: [], targets: [] };
      }
      return store.forwards;
    },
    syncNetworkForm() {
      const selected = store.forwards?.networks || [];
      this.selectedNetworks = this.attachableNetworks
        .filter((n) => selected.some((sel) => sel === n.fullName || sel === n.name))
        .map((n) => n.fullName || n.name);
      // Configured names matching no network on this host (a stack that is not
      // up, say) go in the free-text field so saving doesn't drop them.
      const known = (store.networks || []).flatMap((n) => [n.fullName, n.name]);
      this.networksRaw = selected.filter((sel) => !known.includes(sel)).join("\n");
    },
    targetLabel(tg) {
      const where = tg.ip ? tg.ip : `${tg.state || "stopped"}, no address yet`;
      const ports = (tg.ports || []).length ? ` — ${tg.ports.join(", ")}` : "";
      return `${tg.name} (${tg.owner || "—"}, ${where})${ports}`;
    },
    openAdd() {
      this.add = {
        visible: true,
        form: { hostPort: "", proto: "tcp", containerId: "", targetIp: "", targetPort: "", note: "", requireLogin: false },
      };
    },
    async submitAdd() {
      const f = this.add.form;
      const payload = {
        hostPort: Number(f.hostPort) || 0,
        proto: f.proto || "tcp",
        containerId: f.containerId || "",
        targetIp: (f.targetIp || "").trim(),
        targetPort: Number(f.targetPort) || 0,
        note: (f.note || "").trim(),
        requireLogin: f.proto === "udp" ? false : !!f.requireLogin,
      };
      try {
        const res = await api("/api/admin/forwards", { method: "POST", body: JSON.stringify(payload) });
        await this.reload();
        this.add.visible = false;
        if (res.warning) ElMessage.warning(tt("forwards.addedWithWarn", { warn: res.warning }));
        else ElMessage.success(tt("forwards.forwardAdded"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async remove(rule) {
      const label = `${rule.hostPort}/${rule.proto}`;
      try {
        await ElMessageBox.confirm(tt("forwards.deleteConfirm", { label }), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/admin/forwards/delete", { method: "POST", body: JSON.stringify({ id: rule.manualId }) });
        await this.reload();
        ElMessage.success(tt("forwards.forwardDeleted"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveNetworks() {
      this.savingNets = true;
      try {
        const res = await api("/api/admin/network/forward", {
          method: "POST",
          body: JSON.stringify({ networks: this.selectedNetworks, networksRaw: this.networksRaw }),
        });
        await this.reload();
        if (res.warning) ElMessage.warning(tt("forwards.savedWithWarn", { warn: res.warning }));
        else ElMessage.success(tt("forwards.networksSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.savingNets = false;
      }
    },
    async saveAuth() {
      this.savingAuth = true;
      try {
        await api("/api/admin/forward/auth", {
          method: "POST",
          body: JSON.stringify({ enabled: !!this.authForm.enabled, consoleUrl: (this.authForm.consoleUrl || "").trim() }),
        });
        this.auth = { enabled: !!this.authForm.enabled, consoleUrl: (this.authForm.consoleUrl || "").trim() };
        ElMessage.success(tt("forwardAuth.saved"));
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.savingAuth = false;
      }
    },
  },
};
</script>

<style scoped>
.stack > * + * { margin-top: 16px; }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.row2 { display: flex; gap: 8px; }
.field-label { font-size: 12.5px; color: #475569; font-weight: 600; margin-bottom: 6px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.check.locked { opacity: 0.5; }
.check-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 6px; margin-bottom: 10px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.error-box { background: #fef2f2; color: #b91c1c; border: 1px solid #fecaca; border-radius: 8px; padding: 10px 12px; }
</style>
