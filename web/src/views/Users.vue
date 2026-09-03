<template>
  <div class="users-page">
    <!-- Users -->
    <div class="card">
      <div class="form-card-head">
        <div class="form-card-titles">
          <h2>{{ tt("users.users") }}</h2>
          <p>{{ tt("users.usersSub") }}</p>
        </div>
        <el-button type="primary" size="small" @click="openCreateUser">{{ tt("users.newUser") }}</el-button>
      </div>
      <el-table
        :data="s.users"
        size="small"
        :empty-text="tt('users.noUsers')"
        :row-class-name="rowClass"
        @row-click="onUserRowClick"
      >
        <el-table-column :label="tt('common.user')" :min-width="s.isMobile ? 150 : 200">
          <template #default="{ row }">
            <div class="primary-line">
              {{ displayName(row) }}
              <el-tag v-if="row.disabled" size="small" type="info">{{ tt("users.roleDisabled") }}</el-tag>
            </div>
            <div v-if="row.feishuOpenId" class="secondary-line">{{ tt("users.feishuLine", { name: row.feishuOpenId }) }}</div>
            <div class="secondary-line">{{ tt("users.limitLine", { cap: row.containerCap, quota: formatQuota(row.netdiskQuotaBytes) }) }}</div>
            <!-- Phone rows fold the hidden groups/ports columns into the primary cell. -->
            <div v-if="s.isMobile" class="secondary-line">
              {{ tt("users.colGroups") }}: {{ row.group || tt("users.groupsNone") }} · {{ tt("users.colPorts") }}: {{ portsText(row) }}
            </div>
          </template>
        </el-table-column>
        <el-table-column :label="tt('users.colRole')" :width="s.isMobile ? 96 : 110">
          <template #default="{ row }">
            <el-tag v-if="row.role === 'admin'" size="small">{{ tt("users.roleAdminBadge") }}</el-tag>
            <el-tag v-else-if="isPending(row)" size="small" type="warning">{{ tt("users.rolePending") }}</el-tag>
            <el-tag v-else size="small" :type="row.role === 'operator' ? 'success' : 'info'">{{ row.role }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('users.colGroups')" min-width="120">
          <template #default="{ row }"><span class="secondary-line">{{ row.group || tt("users.groupsNone") }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('users.colPorts')" width="110">
          <template #default="{ row }">
            <span class="secondary-line">{{ portsText(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" width="310" fixed="right">
          <template #default="{ row }">
            <el-button link size="small" @click="openGroups(row)">{{ tt("networks.groups") }}</el-button>
            <el-button v-if="isPending(row)" link size="small" class="ok-text" @click="approve(row)">{{ tt("users.approve") }}</el-button>
            <el-button link size="small" @click="openEdit(row)">{{ tt("common.edit") }}</el-button>
            <el-button link size="small" class="warn-text" :disabled="row.id === s.me.id" @click="deactivate(row)">{{ tt("users.deactivate") }}</el-button>
            <el-button link size="small" class="danger-text" :disabled="row.id === s.me.id" @click="remove(row)">{{ tt("common.delete") }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Feishu bot test: push a message to one user's Feishu DM -->
    <div class="card">
      <div class="form-card-head">
        <div class="form-card-titles">
          <h2>{{ tt("users.feishuTest") }}</h2>
          <p>{{ tt("users.feishuTestSub") }}</p>
        </div>
      </div>
      <div class="feishu-test-form">
        <el-select
          v-model="feishuTest.userId"
          class="feishu-test-user"
          filterable
          size="small"
          :placeholder="tt('users.feishuTestUser')"
        >
          <el-option v-for="u in feishuUsers" :key="u.id" :value="u.id" :label="displayName(u) || u.username" />
        </el-select>
        <div class="feishu-test-msg">
          <el-input v-model="feishuTest.message" :placeholder="tt('users.feishuTestMessage')" size="small" @keyup.enter="sendFeishuTest" />
        </div>
        <el-button type="primary" size="small" :disabled="!feishuTest.userId || feishuTestSending" @click="sendFeishuTest">
          {{ tt("users.feishuTestSend") }}
        </el-button>
      </div>
      <p v-if="!feishuUsers.length" class="hint" style="margin: 8px 0 0">{{ tt("users.feishuTestNoUsers") }}</p>
    </div>

    <!-- Groups: one row per group, all per-group settings live in the edit dialog -->
    <div class="card">
      <div class="form-card-head">
        <div class="form-card-titles">
          <h2>{{ tt("users.groupsTitle") }}</h2>
          <p>{{ tt("users.groupsSub") }}</p>
        </div>
        <el-button size="small" @click="createGroup">{{ tt("users.newGroup") }}</el-button>
      </div>
      <el-table
        :data="s.groups"
        size="small"
        :empty-text="tt('users.noGroups')"
        :row-class-name="s.isMobile ? 'row-tappable' : ''"
        @row-click="onGroupRowClick"
      >
        <el-table-column :label="tt('users.colGroup')" :min-width="s.isMobile ? 240 : 120">
          <template #default="{ row }">
            <span class="primary-line">{{ row.name }}</span>
            <!-- Phone rows keep the netdisk root visible; the dialog carries the rest. -->
            <div v-if="s.isMobile" class="secondary-line mono">{{ row.netdiskPath || tt("users.notConfigured") }}</div>
          </template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('users.netdiskRoot')" min-width="170">
          <template #default="{ row }"><span class="secondary-line mono">{{ row.netdiskPath || tt("users.notConfigured") }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('users.backupRoot')" min-width="170">
          <template #default="{ row }"><span class="secondary-line mono">{{ row.backupPath || tt("users.notConfigured") }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" :label="tt('users.sharedDiskRoot')" min-width="170">
          <template #default="{ row }"><span class="secondary-line mono">{{ row.sharedDiskPath || tt("users.notConfigured") }}</span></template>
        </el-table-column>
        <el-table-column :label="tt('users.colLanguage')" :width="s.isMobile ? 80 : 90">
          <template #default="{ row }"><span class="secondary-line">{{ langLabel(row.language) }}</span></template>
        </el-table-column>
        <el-table-column v-if="!s.isMobile" width="70" fixed="right">
          <template #default="{ row }">
            <el-button link size="small" @click="openGroupSettings(row)">{{ tt("common.edit") }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div class="users-duo">
      <!-- Netdisk usage -->
      <div class="card">
        <div class="form-card-head">
          <div class="form-card-titles">
            <h2>{{ tt("users.netdiskUsage") }}</h2>
            <p>{{ tt("users.totalUsed", { size: fmtBytes(totalUsed) }) }}</p>
          </div>
        </div>
        <el-table :data="usageRows" size="small" :empty-text="tt('users.noUsers')">
          <el-table-column :label="tt('common.user')">
            <template #default="{ row }">
              {{ displayName(row) }}
              <el-tag v-if="row.usage && !row.usage.configured" size="small" type="info">{{ tt("users.noPath") }}</el-tag>
              <el-tag v-else-if="row.usage && row.usage.pathMissing" size="small" type="info">{{ tt("users.notCreated") }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="tt('netdisk.usedCol')" width="100">
            <template #default="{ row }">{{ fmtBytes(row.usage?.usedBytes || 0) }}</template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('users.colQuota')" width="130">
            <template #default="{ row }">{{ quotaText(row) }}</template>
          </el-table-column>
          <el-table-column width="160">
            <template #default="{ row }">
              <el-progress
                v-if="(row.usage?.quotaBytes || 0) > 0"
                :percentage="quotaPct(row)"
                :status="quotaPct(row) >= 90 ? 'exception' : quotaPct(row) >= 70 ? 'warning' : undefined"
                :stroke-width="8"
                :show-text="false"
              />
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Long-running tasks -->
      <div class="card">
        <div class="form-card-head">
          <div class="form-card-titles">
            <h2>{{ tt("users.longTasks") }}</h2>
            <p>{{ tt("users.longTasksHint") }}</p>
          </div>
        </div>
        <div v-if="!tasks.length" class="empty-state">{{ tt("users.longTasksNone") }}</div>
        <el-table v-else :data="tasks" size="small">
          <el-table-column v-if="!s.isMobile" :label="tt('users.longTasksUser')" width="130">
            <template #default="{ row }">{{ displayNameForUsername(row.ownerName) || row.ownerName || "—" }}</template>
          </el-table-column>
          <el-table-column :label="tt('users.longTasksTask')" min-width="150">
            <template #default="{ row }">
              <div class="primary-line">{{ taskLabel(row) }}</div>
              <div class="secondary-line">{{ row.name || "" }}</div>
              <!-- Phone rows fold the hidden owner column into the task cell. -->
              <div v-if="s.isMobile" class="secondary-line">{{ displayNameForUsername(row.ownerName) || row.ownerName || "—" }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="tt('users.longTasksProgress')" min-width="140">
            <template #default="{ row }">
              <template v-if="typeof row.progress === 'number' && row.progress >= 0">
                <el-progress :percentage="Math.min(100, row.progress)" :stroke-width="6" />
              </template>
              <span v-else class="hint">{{ row.message || "…" }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="tt('users.longTasksElapsed')" width="100">
            <template #default="{ row }">{{ elapsed(row.startedAt) }}</template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <!-- Phone-width user rows: tap for every action in a bottom sheet. -->
    <action-sheet
      v-model:visible="sheet.visible"
      :title="sheet.row ? (displayName(sheet.row) || sheet.row.username) : ''"
      :subtitle="sheet.row ? roleLabel(sheet.row) : ''"
      :items="sheetItems"
      :columns="4"
      @select="onSheetSelect"
    >
      <template #meta>
        <div class="sheet-meta">
          <div v-if="sheet.row" class="sheet-meta-line">{{ tt("users.limitLine", { cap: sheet.row.containerCap, quota: formatQuota(sheet.row.netdiskQuotaBytes) }) }}</div>
          <div v-if="sheet.row" class="sheet-meta-line">{{ tt("users.colGroups") }}: {{ sheet.row.group || tt("users.groupsNone") }}</div>
          <div v-if="sheet.row" class="sheet-meta-line">{{ tt("users.colPorts") }}: {{ portsText(sheet.row) }}</div>
        </div>
      </template>
    </action-sheet>

    <!-- Create user -->
    <el-dialog v-model="createVisible" :title="tt('users.newUser')" width="460px" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item :label="tt('users.username')">
          <el-input v-model="userForm.username" :placeholder="tt('users.usernamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="tt('users.password')">
          <el-input v-model="userForm.password" type="password" show-password :placeholder="tt('users.passwordPlaceholder')" />
        </el-form-item>
        <el-form-item :label="tt('users.colRole')">
          <el-select v-model="userForm.role" style="width: 100%">
            <el-option v-for="r in roles" :key="r.value" :value="r.value" :label="`${r.label} — ${r.hint}`" />
          </el-select>
        </el-form-item>
        <div class="form-grid">
          <el-form-item :label="tt('users.containerLimit')">
            <el-input v-model="userForm.containerCap" type="number" min="1" />
          </el-form-item>
          <el-form-item :label="tt('users.netdiskQuota')">
            <el-input v-model="userForm.netdiskQuotaGB" type="number" min="0" step="0.1" />
          </el-form-item>
        </div>
        <el-form-item :label="tt('users.colGroups')">
          <div class="check-grid">
            <label v-for="g in s.groups" :key="g.id" class="check">
              <input v-model="userForm.groupId" type="radio" :value="g.id" name="user-group" /> {{ g.name }}
            </label>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="createUser">{{ tt("users.createUser") }}</el-button>
      </template>
    </el-dialog>

    <!-- User groups -->
    <el-dialog v-model="groupsDialog.visible" :title="tt('users.editGroupsTitle', { name: groupsDialog.name })" width="420px" append-to-body>
      <div class="check-grid col">
        <label v-for="g in s.groups" :key="g.id" class="check">
          <input v-model="groupsDialog.groupId" type="radio" :value="g.id" name="edit-user-group" />
          {{ g.name }}
          <span v-if="g.name === 'pending'" class="hint">{{ tt("users.pendingApproval") }}</span>
        </label>
      </div>
      <template #footer>
        <el-button @click="groupsDialog.visible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="saveGroups">{{ tt("common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- User edit -->
    <el-dialog v-model="editDialog.visible" :title="tt('users.editTitle', { name: editDialog.name })" width="460px" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item :label="tt('users.colRole')">
          <el-select v-model="editDialog.role" style="width: 100%">
            <el-option v-for="r in roles" :key="r.value" :value="r.value" :label="`${r.label} — ${r.hint}`" />
          </el-select>
        </el-form-item>
        <div class="form-grid">
          <el-form-item :label="tt('users.containerLimit')">
            <el-input v-model="editDialog.containerCap" type="number" min="1" />
          </el-form-item>
          <el-form-item :label="tt('users.netdiskQuota')">
            <el-input v-model="editDialog.netdiskQuotaGB" type="number" min="0" step="0.1" />
          </el-form-item>
        </div>
        <el-form-item :label="tt('users.portPrefix')">
          <el-input v-model="editDialog.portPrefix" type="number" min="100" max="655" :placeholder="tt('users.portPrefixPlaceholder')" />
        </el-form-item>
        <el-form-item :label="tt('users.newPassword')">
          <el-input v-model="editDialog.password" type="password" show-password :placeholder="tt('users.resetPassword')" autocomplete="new-password" />
          <p class="hint" style="margin: 4px 0 0">{{ tt("users.newPasswordHint") }}</p>
        </el-form-item>
        <label class="check"><input v-model="editDialog.enabled" type="checkbox"> {{ tt("users.accountEnabled") }}</label>
      </el-form>
      <template #footer>
        <el-button @click="editDialog.visible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="saveUser">{{ tt("common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- Group settings: the three roots plus the default language in one place -->
    <el-dialog v-model="groupDialog.visible" :title="tt('users.groupSettingsTitle', { name: groupDialog.name })" width="480px" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item :label="tt('users.netdiskRoot')">
          <el-input v-model="groupDialog.netdiskPath" :placeholder="`/data/netdisk/${groupDialog.name}`" />
        </el-form-item>
        <el-form-item :label="tt('users.backupRoot')">
          <el-input v-model="groupDialog.backupPath" :placeholder="`/mnt/backup/${groupDialog.name}`" />
          <p class="hint" style="margin: 4px 0 0">{{ tt("users.backupHint") }}</p>
        </el-form-item>
        <el-form-item :label="tt('users.sharedDiskRoot')">
          <el-input v-model="groupDialog.sharedDiskPath" :placeholder="`/mnt/shared/${groupDialog.name}`" />
          <p class="hint" style="margin: 4px 0 0">{{ tt("users.sharedDiskHint") }}</p>
        </el-form-item>
        <el-form-item :label="tt('users.colLanguage')">
          <el-select v-model="groupDialog.language" style="width: 100%">
            <!-- The API rejects an empty language, so "not set" can only stay, never come back. -->
            <el-option v-if="!groupDialog.orig?.language" value="" :label="tt('users.notSet')" />
            <el-option value="zh_CN" label="中文" />
            <el-option value="en_US" label="English" />
          </el-select>
          <p class="hint" style="margin: 4px 0 0">{{ tt("users.groupLangHint") }}</p>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="groupDialog.visible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="saveGroupSettings">{{ tt("common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, refreshSection, isAdmin, displayName, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import { registerRouteRefresh, unregisterRouteRefresh } from "@/refresh";
import { fmtBytes } from "@/lib/common.js";
import ActionSheet from "@/components/ActionSheet.vue";

const TASK_KIND_LABEL_KEY = {
  "netdisk.copy": "task.netdiskCopy",
  "netdisk.move": "task.netdiskMove",
  "netdisk.upload": "task.netdiskUpload",
  "netdisk.upload.chunked": "task.netdiskUploadChunked",
  "netdisk.transfer": "task.netdiskTransfer",
  "netdisk.restore": "task.netdiskRestore",
  "backup.run": "task.backupRun",
};

export default {
  name: "Users",
  components: { ActionSheet },
  data() {
    return {
      s: store,
      createVisible: false,
      userForm: { username: "", password: "", role: "user", containerCap: 10, netdiskQuotaGB: 0, groupId: 0 },
      feishuTest: { userId: null, message: "" },
      feishuTestSending: false,
      usage: null,
      tasks: [],
      sheet: { visible: false, row: null },
      groupsDialog: { visible: false, id: 0, name: "", groupId: 0 },
      editDialog: { visible: false, id: 0, name: "", role: "user", containerCap: 10, netdiskQuotaGB: 0, portPrefix: "", password: "", enabled: true },
      groupDialog: { visible: false, id: 0, name: "", netdiskPath: "", backupPath: "", sharedDiskPath: "", language: "", orig: {} },
    };
  },
  computed: {
    roles() {
      return [
        { value: "user", label: tt("users.roleUser"), hint: tt("users.roleUserHint") },
        { value: "operator", label: tt("users.roleOperator"), hint: tt("users.roleOperatorHint") },
        { value: "helpdesk", label: tt("users.roleHelpdesk"), hint: tt("users.roleHelpdeskHint") },
        { value: "readonly", label: tt("users.roleReadonly"), hint: tt("users.roleReadonlyHint") },
        { value: "admin", label: tt("users.roleAdmin"), hint: tt("users.roleAdminHint") },
      ];
    },
    usageMap() {
      const m = {};
      for (const r of this.usage || []) m[r.id] = r;
      return m;
    },
    usageRows() {
      const map = this.usageMap;
      return [...(store.users || [])].sort((a, b) => (map[b.id]?.usedBytes || 0) - (map[a.id]?.usedBytes || 0))
        .map((u) => ({ ...u, usage: map[u.id] }));
    },
    totalUsed() {
      return Object.values(this.usageMap).reduce((s, r) => (s + (r.usedBytes || 0)), 0);
    },
    feishuUsers() {
      return (store.users || []).filter((u) => u.feishuOpenId);
    },
    sheetItems() {
      const row = this.sheet.row;
      if (!row) return [];
      const self = row.id === store.me.id;
      return [
        { key: "groups", label: tt("networks.groups"), icon: "Avatar" },
        ...(this.isPending(row) ? [{ key: "approve", label: tt("users.approve"), icon: "CircleCheck" }] : []),
        { key: "edit", label: tt("common.edit"), icon: "Edit" },
        { key: "deactivate", label: tt("users.deactivate"), icon: "SwitchButton", disabled: self },
        { key: "delete", label: tt("common.delete"), icon: "Delete", danger: true, disabled: self },
      ];
    },
  },
  async mounted() {
    registerRouteRefresh("users", () => this.loadCards());
    await this.loadCards();
  },
  beforeUnmount() {
    unregisterRouteRefresh("users");
  },
  methods: {
    tt,
    displayName,
    displayNameForUsername,
    fmtBytes,
    // The netdisk-usage and long-running-tasks cards are admin-only side data
    // next to the store-backed user/group tables; refresh them with the route.
    async loadCards() {
      if (!isAdmin()) return;
      api("/api/admin/netdisk/usage")
        .then((rows) => { this.usage = rows || []; })
        .catch(() => { this.usage = []; });
      api("/api/admin/tasks")
        .then((rows) => { this.tasks = rows || []; })
        .catch(() => { this.tasks = []; });
    },
    rowClass({ row }) {
      return [row.disabled ? "row-muted" : "", store.isMobile ? "row-tappable" : ""].filter(Boolean).join(" ");
    },
    onUserRowClick(row) {
      if (!store.isMobile) return;
      this.sheet = { visible: true, row };
    },
    onGroupRowClick(row) {
      if (!store.isMobile) return;
      this.openGroupSettings(row);
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      if (!row) return;
      if (item.key === "groups") this.openGroups(row);
      else if (item.key === "approve") this.approve(row);
      else if (item.key === "edit") this.openEdit(row);
      else if (item.key === "deactivate") this.deactivate(row);
      else if (item.key === "delete") this.remove(row);
    },
    portsText(user) {
      return user.portPrefix ? `${user.portPrefix * 100}-${user.portPrefix * 100 + 99}` : tt("users.portsNotAssigned");
    },
    roleLabel(user) {
      if (user.role === "admin") return tt("users.roleAdminBadge");
      if (this.isPending(user)) return tt("users.rolePending");
      const r = this.roles.find((x) => x.value === user.role);
      return r ? r.label : user.role;
    },
    isPending(user) {
      return user.group === "pending";
    },
    langLabel(lang) {
      return lang === "zh_CN" ? "中文" : lang === "en_US" ? "English" : tt("users.notSet");
    },
    formatQuota(bytes) {
      if (!bytes) return tt("users.unlimited");
      const gb = bytes / 1024 / 1024 / 1024;
      if (gb >= 1) return `${gb.toFixed(1)} GB`;
      const mb = bytes / 1024 / 1024;
      if (mb >= 1) return `${mb.toFixed(1)} MB`;
      return `${bytes} B`;
    },
    quotaPct(row) {
      const quota = row.usage?.quotaBytes || 0;
      const used = row.usage?.usedBytes || 0;
      return quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
    },
    quotaText(row) {
      const quota = row.usage?.quotaBytes || 0;
      return quota > 0 ? `${this.quotaPct(row)}% / ${fmtBytes(quota)}` : tt("users.unlimited");
    },
    taskLabel(task) {
      return tt(TASK_KIND_LABEL_KEY[task.kind] || "") || task.kind;
    },
    elapsed(startedAt) {
      const totalSeconds = Math.max(0, Math.floor((Date.now() - Date.parse(startedAt)) / 1000));
      if (totalSeconds < 60) return `${totalSeconds}s`;
      const minutes = Math.floor(totalSeconds / 60);
      if (minutes < 60) return `${minutes}m ${totalSeconds % 60}s`;
      return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
    },
    async createGroup() {
      let name;
      try {
        ({ value: name } = await ElMessageBox.prompt(tt("users.groupCreatePrompt"), tt("users.newGroup"), {
          inputPlaceholder: tt("users.groupNamePlaceholder"),
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      name = (name || "").trim();
      if (!name) return;
      try {
        await api("/api/groups", { method: "POST", body: JSON.stringify({ name }) });
        await refreshSection("users", "groups");
        ElMessage.success(tt("users.groupCreated"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    openCreateUser() {
      this.userForm = { username: "", password: "", role: "user", containerCap: 10, netdiskQuotaGB: 0, groupId: 0 };
      this.createVisible = true;
    },
    async sendFeishuTest() {
      if (!this.feishuTest.userId) return;
      this.feishuTestSending = true;
      try {
        await api("/api/settings/feishu/test", {
          method: "POST",
          body: JSON.stringify({ userId: this.feishuTest.userId, message: this.feishuTest.message || "" }),
        });
        ElMessage.success(tt("users.feishuTestSent"));
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.feishuTestSending = false;
      }
    },
    async createUser() {
      const f = this.userForm;
      const payload = {
        username: f.username,
        password: f.password,
        role: f.role,
        containerCap: Number(f.containerCap || 10),
        netdiskQuotaBytes: Math.round(Number(f.netdiskQuotaGB || 0) * 1024 * 1024 * 1024),
        groupId: Number(f.groupId) || ((store.groups || []).find((g) => g.name === "users") || {}).id || 0,
      };
      try {
        await api("/api/users", { method: "POST", body: JSON.stringify(payload) });
        await refreshSection("users", "groups");
        this.createVisible = false;
        ElMessage.success(tt("users.userCreated"));
        this.userForm = { username: "", password: "", role: "user", containerCap: 10, netdiskQuotaGB: 0, groupId: 0 };
      } catch (err) {
        if (err.message === "user capacity full") ElMessage.error(tt("users.capacityFull"));
        else ElMessage.error(err.message);
      }
    },
    openGroupSettings(group) {
      this.groupDialog = {
        visible: true,
        id: group.id,
        name: group.name,
        netdiskPath: group.netdiskPath || "",
        backupPath: group.backupPath || "",
        sharedDiskPath: group.sharedDiskPath || "",
        language: group.language || "",
        orig: {
          netdiskPath: group.netdiskPath || "",
          backupPath: group.backupPath || "",
          sharedDiskPath: group.sharedDiskPath || "",
          language: group.language || "",
        },
      };
    },
    // Only the fields that changed hit their endpoint; paths may be cleared,
    // the language may not (the API rejects an empty value).
    async saveGroupSettings() {
      const d = this.groupDialog;
      const o = d.orig;
      const calls = [];
      if (d.netdiskPath.trim() !== o.netdiskPath) calls.push(["/api/groups/netdisk", { groupId: d.id, path: d.netdiskPath.trim() }]);
      if (d.backupPath.trim() !== o.backupPath) calls.push(["/api/groups/backup", { groupId: d.id, path: d.backupPath.trim() }]);
      if (d.sharedDiskPath.trim() !== o.sharedDiskPath) calls.push(["/api/groups/shareddisk", { groupId: d.id, path: d.sharedDiskPath.trim() }]);
      if (d.language && d.language !== o.language) calls.push(["/api/admin/group/language", { groupId: d.id, language: d.language }]);
      try {
        for (const [url, body] of calls) await api(url, { method: "POST", body: JSON.stringify(body) });
        await refreshSection("users", "groups");
        this.groupDialog.visible = false;
        if (calls.length) ElMessage.success(tt("users.groupSettingsSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    openGroups(user) {
      this.groupsDialog = {
        visible: true,
        id: user.id,
        name: displayName(user) || user.username,
        groupId: ((store.groups || []).find((g) => g.name === user.group) || {}).id || 0,
      };
    },
    async saveGroups() {
      try {
        await api("/api/users/group", {
          method: "POST",
          body: JSON.stringify({ userId: Number(this.groupsDialog.id), groupId: Number(this.groupsDialog.groupId) }),
        });
        await refreshSection("users", "groups");
        this.groupsDialog.visible = false;
        ElMessage.success(tt("users.groupsUpdated"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    openEdit(user) {
      this.editDialog = {
        visible: true,
        id: user.id,
        name: displayName(user),
        role: user.role || "user",
        containerCap: user.containerCap,
        netdiskQuotaGB: (user.netdiskQuotaBytes / 1024 / 1024 / 1024).toFixed(2),
        portPrefix: user.portPrefix || "",
        password: "",
        enabled: !user.disabled,
      };
    },
    async saveUser() {
      const e = this.editDialog;
      const portPrefixRaw = String(e.portPrefix).trim();
      const payload = {
        id: Number(e.id),
        role: e.role,
        containerCap: Number(e.containerCap || 10),
        netdiskQuotaBytes: Math.round(Number(e.netdiskQuotaGB || 0) * 1024 * 1024 * 1024),
        portPrefix: portPrefixRaw === "" ? null : Number(portPrefixRaw),
        password: e.password || "",
        disabled: !e.enabled,
      };
      try {
        await api("/api/users/update", { method: "POST", body: JSON.stringify(payload) });
        await refreshSection("users", "groups");
        this.editDialog.visible = false;
        ElMessage.success(tt("users.userUpdated"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async approve(user) {
      try {
        await ElMessageBox.confirm(tt("users.approveConfirm", { userName: user.username }), tt("users.approve"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        });
      } catch { return; }
      try {
        await api("/api/users/approve", { method: "POST", body: JSON.stringify({ userId: user.id }) });
        await refreshSection("users", "groups");
        ElMessage.success(tt("users.userApproved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async deactivate(user) {
      if (user.id === store.me.id) {
        ElMessage.warning(tt("users.cannotDeactivateSelf"));
        return;
      }
      try {
        await ElMessageBox.confirm(tt("users.deactivateConfirm", { userName: user.username }), tt("users.deactivate"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/users/deactivate", { method: "POST", body: JSON.stringify({ id: user.id }) });
        await refreshSection("users", "groups");
        ElMessage.success(tt("users.userDeactivated"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async remove(user) {
      if (user.id === store.me.id) {
        ElMessage.warning(tt("users.cannotDeleteSelf"));
        return;
      }
      try {
        await ElMessageBox.confirm(tt("users.deleteConfirm", { userName: user.username }), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/users/delete", { method: "POST", body: JSON.stringify({ id: user.id }) });
        await refreshSection("users", "groups");
        ElMessage.success(tt("users.userDeleted"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
.users-duo { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 0 16px; align-items: start; }
.form-card-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 12px; flex-wrap: wrap; }
.form-card-titles h2 { margin: 0; font-size: 14px; }
.form-card-titles p { margin: 2px 0 0; color: var(--muted); font-size: 12px; }
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 8px; }
.feishu-test-form { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.feishu-test-user { width: 200px; flex: none; }
.feishu-test-msg { flex: 1; min-width: 180px; }
.check-grid { display: flex; flex-wrap: wrap; gap: 8px 14px; }
.check-grid.col { flex-direction: column; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.ok-text { color: var(--ok) !important; }
.sheet-meta { display: flex; flex-direction: column; gap: 3px; margin: 8px 0 12px; }
.sheet-meta-line { color: var(--muted); font-size: 12px; word-break: break-word; }
>>> .row-muted { opacity: 0.55; }
@media (max-width: 1100px) {
  .users-duo { grid-template-columns: minmax(0, 1fr); }
}
</style>
