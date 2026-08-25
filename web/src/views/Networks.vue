<template>
  <div class="card">
    <div class="card-head">
      <h2>{{ tt("networks.title") }}</h2>
      <el-button v-if="isAdmin()" type="primary" size="small" @click="dialog = true">{{ tt("networks.newNetwork") }}</el-button>
    </div>
    <p v-if="isAdmin()" class="hint" style="margin: 0 0 10px">{{ tt("networks.adminHint") }}</p>
    <el-table
      :data="s.networks"
      size="small"
      :empty-text="tt('networks.noNetworksCreate')"
      :row-class-name="s.isMobile ? 'row-tappable' : ''"
      @row-click="onRowClick"
    >
      <el-table-column :label="tt('common.name')" :min-width="s.isMobile ? 150 : 200">
        <template #default="{ row }">
          <div class="primary-line">
            {{ row.name }}
            <el-tag v-if="row.system" size="small" type="info">{{ tt("networks.badgeSystem") }}</el-tag>
            <el-tag v-else-if="row.external" size="small" type="info">{{ tt("networks.badgeHost") }}</el-tag>
            <el-tag v-else-if="row.shared" size="small" type="info">{{ tt("networks.badgeShared") }}</el-tag>
            <el-tag v-if="row.internal" size="small" type="info">{{ tt("networks.badgeInternal") }}</el-tag>
            <!-- "forward" says the ports of containers on this network are
                 relayed by mudp instead of published by Docker. -->
            <el-tag v-if="row.forward" size="small" type="success">{{ tt("networks.badgeForward") }}</el-tag>
          </div>
          <div v-if="s.isMobile" class="secondary-line mono">{{ row.subnet || "—" }}</div>
        </template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.driver')" width="100">
        <template #default="{ row }"><span class="secondary-line">{{ row.driver }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('networks.colSubnet')" min-width="140">
        <template #default="{ row }"><span class="secondary-line mono">{{ row.subnet || "—" }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.containers')" width="90">
        <template #default="{ row }">{{ row.containers || 0 }}</template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.owner')" width="110">
        <template #default="{ row }"><span class="secondary-line">{{ row.owner || tt("networks.badgeSystem") }}</span></template>
      </el-table-column>
      <el-table-column v-if="isAdmin() && !s.isMobile" :label="tt('networks.colGroups')" width="140">
        <template #default="{ row }"><span class="secondary-line">{{ groupsCell(row) }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" width="200" fixed="right">
        <template #default="{ row }">
          <el-button link icon="InfoFilled" :title="tt('networks.details')" @click="detail(row)" />
          <!-- Built-ins are never deletable, but "bridge" is still shareable:
               restricting it to groups keeps users off the default network. -->
          <el-button v-if="isAdmin() && (!row.system || row.name === 'bridge')" link @click="openShare(row)">{{ tt("networks.groups") }}</el-button>
          <el-button v-if="isAdmin() && row.attachable" link :class="{ 'ok-text': row.forward }" :title="row.forward ? tt('networks.forwardTitleOn') : tt('networks.forwardTitleOff')" @click="toggleForward(row)">
            {{ row.forward ? tt("networks.forwarding") : tt("networks.forward") }}
          </el-button>
          <el-button v-if="canMutate() && row.canDelete" link icon="Delete" class="danger-text" :title="tt('common.delete')" @click="remove(row)" />
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

    <el-dialog v-model="dialog" :title="tt('networks.newTitle')" width="460px" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item required>
          <el-input v-model="form.name" :placeholder="tt('networks.namePlaceholder')" />
        </el-form-item>
        <el-form-item>
          <el-select v-model="form.driver" style="width: 100%">
            <el-option value="bridge" label="bridge" />
            <el-option value="overlay" label="overlay" />
            <el-option value="macvlan" label="macvlan" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-input v-model="form.subnet" :placeholder="tt('networks.subnetPlaceholder')" />
        </el-form-item>
        <el-collapse>
          <el-collapse-item :title="tt('networks.advancedIpam')">
            <el-input v-model="form.gateway" :placeholder="tt('networks.gatewayPlaceholder')" class="adv-input" />
            <el-input v-model="form.ipRange" :placeholder="tt('networks.ipRangePlaceholder')" class="adv-input" />
            <label class="check"><input v-model="form.ipv6" type="checkbox"> {{ tt("networks.enableIpv6") }}</label>
            <label class="check"><input v-model="form.internal" type="checkbox"> {{ tt("networks.internal") }}</label>
          </el-collapse-item>
        </el-collapse>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submit">{{ tt("common.create") }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="share.visible" :title="tt('networks.groupsTitle', { name: share.name })" width="420px" append-to-body>
      <p class="hint">{{ share.system ? tt("networks.groupsHintSystem") : tt("networks.groupsHint") }}</p>
      <div class="check-grid">
        <label v-for="g in s.groups" :key="g.id" class="check">
          <input v-model="share.groupIds" type="checkbox" :value="g.id" /> {{ g.name }}
        </label>
        <span v-if="!s.groups.length" class="hint">{{ tt("networks.noGroupsYet") }}</span>
      </div>
      <template #footer>
        <el-button @click="share.visible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submitShare">{{ tt("common.save") }}</el-button>
      </template>
    </el-dialog>

    <network-detail-dialog v-model:visible="detailState.visible" :name="detailState.fullName" :display="detailState.name" />
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, isAdmin, canMutate, refreshSection } from "@/store";
import { tt } from "@/i18n";
import NetworkDetailDialog from "@/components/network/NetworkDetailDialog.vue";
import ActionSheet from "@/components/ActionSheet.vue";

export default {
  name: "Networks",
  components: { ActionSheet, NetworkDetailDialog },
  data() {
    return {
      s: store,
      dialog: false,
      sheet: { visible: false, row: null },
      form: { name: "", driver: "bridge", subnet: "", gateway: "", ipRange: "", ipv6: false, internal: false },
      share: { visible: false, fullName: "", name: "", system: false, groupIds: [] },
      detailState: { visible: false, fullName: "", name: "" },
    };
  },
  computed: {
    sheetSubtitle() {
      const r = this.sheet.row;
      if (!r) return "";
      return `${r.driver || "bridge"} · ${r.subnet || "—"}`;
    },
    sheetItems() {
      const r = this.sheet.row;
      if (!r) return [];
      const items = [{ key: "detail", label: tt("networks.details"), icon: "InfoFilled" }];
      if (isAdmin() && (!r.system || r.name === "bridge")) items.push({ key: "groups", label: tt("networks.groups"), icon: "Share" });
      if (isAdmin() && r.attachable) items.push({ key: "forward", label: r.forward ? tt("networks.forwarding") : tt("networks.forward"), icon: "SwitchButton" });
      if (canMutate() && r.canDelete) items.push({ key: "delete", label: tt("common.delete"), icon: "Delete", danger: true });
      return items;
    },
  },
  methods: {
    tt,
    isAdmin,
    canMutate,
    onRowClick(row) {
      if (!store.isMobile) return;
      this.sheet = { visible: true, row };
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      if (!row) return;
      if (item.key === "detail") this.detail(row);
      else if (item.key === "groups") this.openShare(row);
      else if (item.key === "forward") this.toggleForward(row);
      else if (item.key === "delete" && canMutate()) this.remove(row);
    },
    groupsCell(n) {
      const groups = n.groups || [];
      if (groups.length) return groups.join(", ");
      if (n.system) return n.name === "bridge" ? tt("networks.everyone") : "—";
      return "—";
    },
    detail(n) {
      this.detailState = { visible: true, fullName: n.fullName || n.name, name: n.name };
    },
    openShare(n) {
      const current = new Set(n.groups || []);
      this.share = {
        visible: true,
        fullName: n.fullName || n.name,
        name: n.name,
        system: !!n.system,
        groupIds: (store.groups || []).filter((g) => current.has(g.name)).map((g) => g.id),
      };
    },
    async submitShare() {
      try {
        await api("/api/networks/access", { method: "POST", body: JSON.stringify({ name: this.share.fullName, groupIds: this.share.groupIds.map(Number) }) });
        await refreshSection("networks");
        this.share.visible = false;
        ElMessage.success(tt("networks.accessUpdated"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    // Switches one network between Docker publishing and mudp forwarding: on a
    // host whose firewall is owned by something else (e.g. an OpenWrt router),
    // Docker's publishing does not work, while the container's own address
    // does. Applies to containers created from then on and to the ones
    // already on the network.
    async toggleForward(n) {
      const forward = !n.forward;
      if (forward) {
        try {
          await ElMessageBox.confirm(tt("networks.forwardConfirm", { name: n.name }), tt("networks.forward"), {
            confirmButtonText: tt("common.confirm"),
            cancelButtonText: tt("common.cancel"),
            type: "warning",
          });
        } catch { return; }
      }
      try {
        const res = await api("/api/networks/forward", {
          method: "POST",
          body: JSON.stringify({ name: n.fullName || n.name, forward }),
        });
        // The Port forwarding page reads the same configuration, so drop its
        // cache rather than let it show the state from before this click.
        store.forwards = null;
        await refreshSection("networks");
        if (res.warning) {
          ElMessage.warning(tt("networks.forwardSavedWarn", { warn: res.warning }));
        } else {
          ElMessage.success(forward ? tt("networks.forwardOn", { name: n.name }) : tt("networks.forwardOff", { name: n.name }));
        }
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async submit() {
      if (!this.form.name.trim()) return;
      try {
        await api("/api/networks", { method: "POST", body: JSON.stringify({ ...this.form, name: this.form.name.trim() }) });
        await refreshSection("networks");
        this.dialog = false;
        ElMessage.success(tt("networks.created"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async remove(n) {
      try {
        await ElMessageBox.confirm(tt("networks.deleteConfirm", { name: n.name }), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/networks/delete", { method: "POST", body: JSON.stringify({ name: n.fullName || n.name }) });
        await refreshSection("networks");
        ElMessage.success(tt("networks.deleted"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
.card-head { display: flex; align-items: center; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.primary-line { font-weight: 600; display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.secondary-line { color: var(--muted); font-size: 12px; }
.check-grid { display: flex; flex-direction: column; gap: 8px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.adv-input { margin-bottom: 8px; }
.ok-text { color: #10b981 !important; }
</style>
