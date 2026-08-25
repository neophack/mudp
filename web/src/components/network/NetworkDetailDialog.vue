<template>
  <el-dialog :model-value="visible" :title="tt('netdetail.title', { name: display })" width="700px" top="5vh" append-to-body @update:model-value="onVisible">
    <div v-if="!loaded" class="empty-state">{{ tt("common.loadingDots") }}</div>
    <div v-else-if="error" class="error-box">✗ {{ error }}</div>
    <template v-else>
      <dl class="detail">
        <dt>{{ tt("common.name") }}</dt><dd>{{ n.name }}</dd>
        <dt>{{ tt("netdetail.fullName") }}</dt><dd class="mono">{{ n.fullName }}</dd>
        <dt>{{ tt("common.driver") }}</dt><dd>{{ n.driver || "—" }}</dd>
        <dt>{{ tt("netdetail.subnet") }}</dt><dd class="mono">{{ n.subnet || "auto" }}</dd>
        <dt>{{ tt("netdetail.gateway") }}</dt><dd class="mono">{{ n.gateway || "—" }}</dd>
        <dt v-if="n.ipRange">{{ tt("netdetail.ipRange") }}</dt><dd v-if="n.ipRange" class="mono">{{ n.ipRange }}</dd>
        <dt>{{ tt("netdetail.ipv6") }}</dt><dd>{{ n.ipv6 ? tt("netdetail.enabled") : tt("netdetail.disabled") }}</dd>
        <dt>{{ tt("networks.badgeInternal") }}</dt><dd>{{ n.internal ? tt("netdetail.internalOn") : tt("netdetail.disabled") }}</dd>
        <dt>{{ tt("netdetail.connectedContainers") }}</dt><dd>{{ tt("netdetail.attached", { n: (n.containers || []).length }) }}</dd>
      </dl>
      <section class="detail-settings">
        <h3>{{ tt("netdetail.connectedContainers") }}</h3>
        <el-table :data="n.containers || []" size="small" :empty-text="tt('netdetail.noContainers')">
          <el-table-column :label="tt('containers.colContainer')">
            <template #default="{ row }">
              <div class="primary-line">{{ row.name }}</div>
              <div class="secondary-line mono">{{ row.id.slice(0, 12) }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="tt('netdetail.colIpv4')" width="130">
            <template #default="{ row }"><span class="mono">{{ row.ipv4 || "—" }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('netdetail.colIpv6')" width="180">
            <template #default="{ row }"><span class="mono">{{ row.ipv6 || "—" }}</span></template>
          </el-table-column>
          <el-table-column v-if="canMutate()" :label="tt('common.actions')" width="70">
            <template #default="{ row }">
              <el-button link icon="SwitchButton" class="warn-text" :title="tt('netdetail.detach')" @click="detach(row)" />
            </template>
          </el-table-column>
        </el-table>
      </section>
      <!-- Picker of the user's running containers not yet on this network. -->
      <section v-if="canMutate() && attachCandidates.length" class="detail-settings">
        <h3>{{ tt("netdetail.attachTitle") }}</h3>
        <div class="attach-row">
          <el-select v-model="attachId" size="small" style="flex: 1">
            <el-option v-for="c in attachCandidates" :key="c.id" :value="c.id" :label="c.name || c.fullName" />
          </el-select>
          <el-button type="primary" size="small" :loading="attaching" @click="attach">{{ tt("netdetail.attach") }}</el-button>
        </div>
      </section>
    </template>
  </el-dialog>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, canMutate, refreshSection } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "NetworkDetailDialog",
  props: {
    visible: { type: Boolean, default: false },
    name: { type: String, default: "" },
    display: { type: String, default: "" },
  },
  data() {
    return {
      n: null,
      error: "",
      loaded: false,
      attachId: "",
      attaching: false,
    };
  },
  computed: {
    attachCandidates() {
      const attached = new Set((this.n?.containers || []).map((c) => c.id));
      return (store.containers || []).filter((c) => c.state === "running" && !attached.has(c.id));
    },
  },
  watch: {
    visible(v) {
      if (v) this.load();
      else this.n = null;
    },
  },
  methods: {
    tt,
    canMutate,
    onVisible(v) {
      this.$emit("update:visible", v);
    },
    async load() {
      this.loaded = false;
      this.error = "";
      try {
        this.n = await api("/api/networks/inspect?name=" + encodeURIComponent(this.name));
        this.attachId = this.attachCandidates.length ? this.attachCandidates[0].id : "";
      } catch (err) {
        this.error = err.message;
        this.n = null;
      } finally {
        this.loaded = true;
      }
    },
    async attach() {
      if (!this.attachId || this.attaching) return;
      this.attaching = true;
      try {
        await api("/api/networks/connect", {
          method: "POST",
          body: JSON.stringify({ name: this.name, containerId: this.attachId }),
        });
        ElMessage.success(tt("netdetail.attachedOk"));
        await refreshSection("containers");
        await this.load();
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.attaching = false;
      }
    },
    async detach(c) {
      try {
        await ElMessageBox.confirm(tt("netdetail.detachConfirm"), tt("netdetail.detach"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/networks/disconnect", {
          method: "POST",
          body: JSON.stringify({ name: this.name, containerId: c.id }),
        });
        ElMessage.success(tt("netdetail.detachedOk"));
        await refreshSection("containers");
        await this.load();
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
.detail-settings { margin-bottom: 12px; }
.detail-settings h3 { margin: 0 0 8px; font-size: 13.5px; }
.attach-row { display: flex; gap: 10px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.warn-text { color: #e6a23c !important; }
.error-box { background: #fef2f2; color: #b91c1c; border: 1px solid #fecaca; border-radius: 8px; padding: 10px 12px; }
</style>
