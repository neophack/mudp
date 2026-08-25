<template>
  <div class="users-layout">
    <section class="stack settings-col">
      <div class="card">
        <div class="card-head"><h2>{{ tt("users.newGroup") }}</h2></div>
        <el-form size="small" @submit.prevent="createGroup">
          <el-input v-model="groupForm.name" :placeholder="tt('users.groupNamePlaceholder')" style="margin-bottom: 10px" />
          <el-button type="primary" size="small" native-type="submit">{{ tt("users.createGroup") }}</el-button>
        </el-form>
      </div>

      <div class="card">
        <div class="card-head"><h2>{{ tt("users.newUser") }}</h2></div>
        <el-form size="small" label-position="top" @submit.prevent="createUser">
          <el-input v-model="userForm.username" :placeholder="tt('users.usernamePlaceholder')" style="margin-bottom: 8px" />
          <el-input v-model="userForm.password" type="password" show-password :placeholder="tt('users.passwordPlaceholder')" style="margin-bottom: 8px" />
          <el-select v-model="userForm.role" style="width: 100%; margin-bottom: 8px">
            <el-option v-for="r in roles" :key="r.value" :value="r.value" :label="`${r.label} — ${r.hint}`" />
          </el-select>
          <el-input v-model="userForm.containerCap" type="number" :placeholder="tt('users.containerLimitPlaceholder')" style="margin-bottom: 8px" />
          <el-input v-model="userForm.netdiskQuotaGB" type="number" step="0.1" :placeholder="tt('users.netdiskQuotaPlaceholder')" style="margin-bottom: 8px" />
          <div class="check-grid">
            <label v-for="g in s.groups" :key="g.id" class="check">
              <input v-model="userForm.groupIds" type="checkbox" :value="g.id" /> {{ g.name }}
            </label>
          </div>
          <el-button type="primary" size="small" native-type="submit" style="margin-top: 10px">{{ tt("users.createUser") }}</el-button>
        </el-form>
      </div>

      <div class="card">
        <div class="card-head"><h2>{{ tt("users.groupPaths") }}</h2></div>
        <el-table :data="s.groups" size="small" :empty-text="tt('users.noGroups')">
          <el-table-column :label="tt('users.colGroup')">
            <template #default="{ row }"><span class="primary-line">{{ row.name }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('users.colPath')">
            <template #default="{ row }"><span class="secondary-line mono">{{ row.netdiskPath || tt("users.notConfigured") }}</span></template>
          </el-table-column>
          <el-table-column width="110">
            <template #default="{ row }">
              <el-button link size="small" @click="setPath(row, 'netdisk')">{{ tt("users.setPath") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="card">
        <div class="card-head"><h2>{{ tt("users.groupBackupPaths") }}</h2></div>
        <p class="hint">{{ tt("users.backupHint") }}</p>
        <el-table :data="s.groups" size="small" :empty-text="tt('users.noGroups')">
          <el-table-column :label="tt('users.colGroup')">
            <template #default="{ row }"><span class="primary-line">{{ row.name }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('users.colBackupPath')">
            <template #default="{ row }"><span class="secondary-line mono">{{ row.backupPath || tt("users.notConfigured") }}</span></template>
          </el-table-column>
          <el-table-column width="130">
            <template #default="{ row }">
              <el-button link size="small" @click="setPath(row, 'backup')">{{ tt("users.setBackupPath") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="card">
        <div class="card-head"><h2>{{ tt("users.groupSharedDiskPaths") }}</h2></div>
        <p class="hint">{{ tt("users.sharedDiskHint") }}</p>
        <el-table :data="s.groups" size="small" :empty-text="tt('users.noGroups')">
          <el-table-column :label="tt('users.colGroup')">
            <template #default="{ row }"><span class="primary-line">{{ row.name }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('users.colSharedDiskPath')">
            <template #default="{ row }"><span class="secondary-line mono">{{ row.sharedDiskPath || tt("users.notConfigured") }}</span></template>
          </el-table-column>
          <el-table-column width="150">
            <template #default="{ row }">
              <el-button link size="small" @click="setPath(row, 'shareddisk')">{{ tt("users.setSharedDiskPath") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="card">
        <div class="card-head"><h2>{{ tt("users.groupLanguages") }}</h2></div>
        <p class="hint">{{ tt("users.groupLangHint") }}</p>
        <el-table :data="s.groups" size="small" :empty-text="tt('users.noGroups')">
          <el-table-column :label="tt('users.colGroup')">
            <template #default="{ row }"><span class="primary-line">{{ row.name }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('users.colLanguage')">
            <template #default="{ row }">
              <span class="secondary-line">{{ row.language === "zh_CN" ? "中文" : row.language === "en_US" ? "English" : tt("users.notSet") }}</span>
            </template>
          </el-table-column>
          <el-table-column width="110">
            <template #default="{ row }">
              <el-button link size="small" @click="setGroupLanguage(row)">{{ tt("users.setLanguage") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </section>

    <section class="stack main-col">
      <div class="card">
        <div class="card-head"><h2>{{ tt("users.users") }}</h2></div>
        <el-table :data="s.users" size="small" :empty-text="tt('users.noUsers')" :row-class-name="rowClass">
          <el-table-column :label="tt('common.user')" min-width="200">
            <template #default="{ row }">
              <div class="primary-line">
                {{ displayName(row) }}
                <el-tag v-if="row.disabled" size="small" type="info">{{ tt("users.roleDisabled") }}</el-tag>
              </div>
              <div v-if="row.feishuOpenId" class="secondary-line">{{ tt("users.feishuLine", { name: row.username }) }}</div>
              <div class="secondary-line">{{ tt("users.limitLine", { cap: row.containerCap, quota: formatQuota(row.netdiskQuotaBytes) }) }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="tt('users.colRole')" width="110">
            <template #default="{ row }">
              <el-tag v-if="row.role === 'admin'" size="small">{{ tt("users.roleAdminBadge") }}</el-tag>
              <el-tag v-else-if="isPending(row)" size="small" type="warning">{{ tt("users.rolePending") }}</el-tag>
              <el-tag v-else size="small" :type="row.role === 'operator' ? 'success' : 'info'">{{ row.role }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="tt('users.colGroups')" min-width="120">
            <template #default="{ row }"><span class="secondary-line">{{ (row.groups || []).join(", ") || tt("users.groupsNone") }}</span></template>
          </el-table-column>
          <el-table-column :label="tt('users.colPorts')" width="110">
            <template #default="{ row }">
              <span class="secondary-line">{{ row.portPrefix ? `${row.portPrefix * 100}-${row.portPrefix * 100 + 99}` : tt("users.portsNotAssigned") }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="tt('common.actions')" width="270" fixed="right">
            <template #default="{ row }">
              <el-button link size="small" @click="openGroups(row)">{{ tt("networks.groups") }}</el-button>
              <el-button v-if="isPending(row)" link size="small" class="ok-text" @click="approve(row)">{{ tt("users.approve") }}</el-button>
              <el-button link size="small" @click="openEdit(row)">{{ tt("common.edit") }}</el-button>
              <el-button link size="small" class="warn-text" :disabled="row.id === s.me.id" @click="deactivate(row)">{{ tt("users.deactivate") }}</el-button>
              <el-button link size="small" icon="Delete" class="danger-text" :title="tt('common.delete')" :disabled="row.id === s.me.id" @click="remove(row)" />
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="card">
        <div class="card-head">
          <h2>{{ tt("users.netdiskUsage") }}</h2>
          <span class="hint">{{ tt("users.totalUsed", { size: fmtBytes(totalUsed) }) }}</span>
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
          <el-table-column :label="tt('users.colQuota')" width="130">
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

      <div class="card">
        <div class="card-head"><h2>{{ tt("users.longTasks") }}</h2></div>
        <p class="hint">{{ tt("users.longTasksHint") }}</p>
        <div v-if="!tasks.length" class="empty-state">{{ tt("users.longTasksNone") }}</div>
        <el-table v-else :data="tasks" size="small">
          <el-table-column :label="tt('users.longTasksUser')" width="130">
            <template #default="{ row }">{{ displayNameForUsername(row.ownerName) || row.ownerName || "—" }}</template>
          </el-table-column>
          <el-table-column :label="tt('users.longTasksTask')" min-width="150">
            <template #default="{ row }">
              <div class="primary-line">{{ taskLabel(row) }}</div>
              <div class="secondary-line">{{ row.name || "" }}</div>
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
    </section>

    <!-- User groups -->
    <el-dialog v-model="groupsDialog.visible" :title="tt('users.editGroupsTitle', { name: groupsDialog.name })" width="420px" append-to-body>
      <div class="check-grid col">
        <label v-for="g in s.groups" :key="g.id" class="check">
          <input v-model="groupsDialog.groupIds" type="checkbox" :value="g.id" />
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
        <el-form-item :label="tt('users.containerLimit')">
          <el-input v-model="editDialog.containerCap" type="number" min="1" />
        </el-form-item>
        <el-form-item :label="tt('users.netdiskQuota')">
          <el-input v-model="editDialog.netdiskQuotaGB" type="number" min="0" step="0.1" />
        </el-form-item>
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
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, refreshSection, isAdmin, displayName, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import { registerRouteRefresh, unregisterRouteRefresh } from "@/refresh";
import { fmtBytes } from "@/lib/common.js";

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
  data() {
    return {
      s: store,
      groupForm: { name: "" },
      userForm: { username: "", password: "", role: "user", containerCap: 10, netdiskQuotaGB: 0, groupIds: [] },
      usage: null,
      tasks: [],
      groupsDialog: { visible: false, id: 0, name: "", groupIds: [] },
      editDialog: { visible: false, id: 0, name: "", role: "user", containerCap: 10, netdiskQuotaGB: 0, portPrefix: "", password: "", enabled: true },
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
      return Object.values(this.usageMap).reduce((s, r) => s + (r.usedBytes || 0), 0);
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
      return row.disabled ? "row-muted" : "";
    },
    isPending(user) {
      return (user.groups || []).length > 0 && (user.groups || []).every((g) => g === "pending");
    },
    formatQuota(bytes) {
      if (!bytes) return "unlimited";
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
      if (!this.groupForm.name.trim()) return;
      try {
        await api("/api/groups", { method: "POST", body: JSON.stringify({ name: this.groupForm.name.trim() }) });
        await refreshSection("users", "groups");
        ElMessage.success(tt("users.groupCreated"));
        this.groupForm.name = "";
      } catch (err) {
        ElMessage.error(err.message);
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
        groupIds: f.groupIds.map(Number),
      };
      if (payload.groupIds.length === 0) {
        const defaultGroup = (store.groups || []).find((g) => g.name === "users");
        if (defaultGroup) payload.groupIds = [defaultGroup.id];
      }
      try {
        await api("/api/users", { method: "POST", body: JSON.stringify(payload) });
        await refreshSection("users", "groups");
        ElMessage.success(tt("users.userCreated"));
        this.userForm = { username: "", password: "", role: "user", containerCap: 10, netdiskQuotaGB: 0, groupIds: [] };
      } catch (err) {
        if (err.message === "user capacity full") ElMessage.error(tt("users.capacityFull"));
        else ElMessage.error(err.message);
      }
    },
    async setPath(group, kind) {
      const prompts = {
        netdisk: ["users.netdiskPathPrompt", "/api/groups/netdisk", "users.netdiskPathSaved"],
        backup: ["users.backupPathPrompt", "/api/groups/backup", "users.backupPathSaved"],
        shareddisk: ["users.sharedDiskPathPrompt", "/api/groups/shareddisk", "users.sharedDiskPathSaved"],
      };
      const [promptKey, url, okKey] = prompts[kind];
      const current = kind === "netdisk" ? group.netdiskPath : kind === "backup" ? group.backupPath : group.sharedDiskPath;
      let path;
      try {
        ({ value: path } = await ElMessageBox.prompt(tt(promptKey, { name: group.name }), tt("common.edit"), {
          inputValue: current || "",
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
        }));
      } catch { return; }
      try {
        await api(url, { method: "POST", body: JSON.stringify({ groupId: group.id, path }) });
        await refreshSection("users", "groups");
        ElMessage.success(tt(okKey));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async setGroupLanguage(group) {
      try {
        const { value } = await ElMessageBox({
          title: tt("users.groupLangTitle", { name: group.name }),
          message: this.$createElement("div", [
            this.$createElement("p", tt("users.groupLangPrompt", { name: group.name })),
            this.$createElement("el-radio-group", {
              props: { value: group.language || "" },
              on: { input: (v) => { this._groupLangPick = v; } },
            }, [
              this.$createElement("el-radio", { props: { label: "zh_CN" } }, "中文"),
              this.$createElement("el-radio", { props: { label: "en_US" } }, "English"),
            ]),
          ]),
          showCancelButton: true,
          confirmButtonText: tt("common.save"),
          cancelButtonText: tt("common.cancel"),
        });
        if (value !== "confirm" || !this._groupLangPick) return;
        await api("/api/admin/group/language", {
          method: "POST",
          body: JSON.stringify({ groupId: group.id, language: this._groupLangPick }),
        });
        await refreshSection("users", "groups");
        ElMessage.success(tt("users.groupLangSaved"));
      } catch (err) {
        if (err !== "cancel" && err.message) ElMessage.error(err.message);
      }
    },
    openGroups(user) {
      const current = new Set(user.groups || []);
      this.groupsDialog = {
        visible: true,
        id: user.id,
        name: displayName(user) || user.username,
        groupIds: (store.groups || []).filter((g) => current.has(g.name)).map((g) => g.id),
      };
    },
    async saveGroups() {
      try {
        await api("/api/users/groups", {
          method: "POST",
          body: JSON.stringify({ userId: Number(this.groupsDialog.id), groupIds: this.groupsDialog.groupIds.map(Number) }),
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
.users-layout { display: grid; grid-template-columns: 340px minmax(0, 1fr); gap: 16px; align-items: start; }
.stack > * + * { margin-top: 0; }
.settings-col, .main-col { display: flex; flex-direction: column; }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.check-grid { display: flex; flex-wrap: wrap; gap: 8px 14px; }
.check-grid.col { flex-direction: column; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.ok-text { color: #10b981 !important; }
.warn-text { color: #e6a23c !important; }
>>> .row-muted { opacity: 0.55; }
@media (max-width: 1100px) {
  .users-layout { grid-template-columns: minmax(0, 1fr); }
}
</style>
