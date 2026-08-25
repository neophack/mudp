<template>
  <div class="settings-page">
    <!-- Personal section: iOS-style grouped list. Selects save on change;
         text inputs keep an explicit save per row. -->
    <p class="group-caption">{{ tt("settings.sectionPersonal") }}</p>
    <div class="card group">
      <div class="row">
        <span class="row-icon tint-blue"><v-icon name="languages" :size="16" /></span>
        <div class="row-main">
          <div class="row-title">{{ tt("settings.language") }}</div>
          <div class="row-desc">{{ tt("settings.languageSub") }}</div>
        </div>
        <el-select v-model="userLanguage" size="small" class="row-select" @change="saveUserLanguage">
          <el-option v-for="lang in langs" :key="lang" :value="lang" :label="langName(lang)" />
        </el-select>
      </div>

      <!-- Shared-disk access: the caller's persistent preference for their
           subfolder (read-only default or read-write). Hidden when the group
           has no shared-disk root configured. -->
      <div v-if="s.me?.sharedDiskConfigured" class="row">
        <span class="row-icon tint-purple"><v-icon name="disks" :size="16" /></span>
        <div class="row-main">
          <div class="row-title">{{ tt("settings.sharedDiskAccess") }}</div>
          <div class="row-desc">{{ tt("settings.sharedDiskAccessHint") }}</div>
        </div>
        <el-select v-model="sharedDiskReadWrite" size="small" class="row-select" @change="saveSharedDisk">
          <el-option value="ro" :label="tt('settings.sharedDiskReadOnly')" />
          <el-option value="rw" :label="tt('settings.sharedDiskReadWrite')" />
        </el-select>
      </div>
      <!-- Appearance: macOS segmented control, light / dark / follow system. -->
      <div class="row">
        <span class="row-icon tint-indigo"><v-icon :name="s.isDark ? 'moon' : 'sun'" :size="16" /></span>
        <div class="row-main">
          <div class="row-title">{{ tt("settings.appearance") }}</div>
          <div class="row-desc">{{ tt("settings.appearanceHint") }}</div>
        </div>
        <div class="theme-segment">
          <button
            v-for="opt in ['auto', 'light', 'dark']"
            :key="opt"
            class="theme-seg-btn"
            :class="{ active: s.theme === opt }"
            type="button"
            @click="pickTheme(opt)"
          >{{ tt("theme." + opt) }}</button>
        </div>
      </div>
    </div>

    <!-- Personal Feishu custom-bot webhook for process-exit notifications. -->
    <div class="card group">
      <div class="row wrap">
        <span class="row-icon tint-teal"><v-icon name="bell" :size="16" /></span>
        <div class="row-main">
          <div class="row-title">{{ tt("settings.feishuNotify") }}</div>
          <div class="row-desc">{{ tt("settings.feishuNotifyHint") }}</div>
        </div>
        <div class="row-input">
          <el-input v-model="webhook" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/…" size="small" />
          <el-button size="small" @click="testWebhook">{{ tt("settings.testWebhook") }}</el-button>
          <el-button type="primary" size="small" @click="saveWebhook">{{ tt("common.save") }}</el-button>
        </div>
      </div>
    </div>

    <!-- Admin section -->
    <template v-if="isAdmin()">
      <p class="group-caption">{{ tt("settings.sectionAdmin") }}</p>
      <div class="card group">
        <div class="row">
          <span class="row-icon tint-blue"><v-icon name="languages" :size="16" /></span>
          <div class="row-main">
            <div class="row-title">{{ tt("settings.defaultLanguage") }}</div>
            <div class="row-desc">{{ tt("admin.userCanOverride") }}</div>
          </div>
          <el-select v-model="defaultLanguage" size="small" class="row-select" @change="saveDefaultLanguage">
            <el-option v-for="lang in langs" :key="lang" :value="lang" :label="langName(lang)" />
          </el-select>
        </div>

        <div class="row wrap">
          <span class="row-icon tint-green"><v-icon name="pencil" :size="16" /></span>
          <div class="row-main">
            <div class="row-title">{{ tt("settings.siteSettings") }}</div>
            <div class="row-desc">{{ tt("settings.siteNameHint") }}</div>
          </div>
          <div class="row-input">
            <el-input v-model="siteName" :placeholder="tt('settings.siteNamePlaceholder')" size="small" />
            <el-button type="primary" size="small" @click="saveSite">{{ tt("settings.saveSite") }}</el-button>
          </div>
        </div>

        <div class="row wrap">
          <span class="row-icon tint-indigo"><v-icon name="users" :size="16" /></span>
          <div class="row-main">
            <div class="row-title">{{ tt("settings.userCapacity") }}</div>
            <div class="row-desc">{{ tt("settings.userCapacityHint") }}</div>
          </div>
          <div class="row-input">
            <el-input v-model="capacity" type="number" min="1" max="9999" :placeholder="tt('settings.userCapacityPlaceholder')" size="small" />
            <el-button type="primary" size="small" @click="saveCapacity">{{ tt("settings.saveUserCapacity") }}</el-button>
          </div>
        </div>

        <div class="row wrap">
          <span class="row-icon tint-orange"><v-icon name="building" :size="16" /></span>
          <div class="row-main">
            <div class="row-title">{{ tt("settings.companyRestriction") }}</div>
            <div class="row-desc">{{ tt("settings.companyRestrictionHint") }}</div>
          </div>
          <div class="row-input">
            <el-input v-model="tenantKey" :placeholder="tt('settings.tenantKeyPlaceholder')" size="small" />
            <el-button type="primary" size="small" @click="saveCompany">{{ tt("settings.saveCompany") }}</el-button>
          </div>
        </div>
      </div>

      <!-- Registries -->
      <div class="card">
        <div class="form-card-head">
          <div class="form-card-titles">
            <h2>{{ tt("settings.registries") }}</h2>
            <p>{{ tt("settings.registriesSub") }}</p>
          </div>
          <el-button type="primary" size="small" @click="openRegistry(null)">{{ tt("settings.addRegistry") }}</el-button>
        </div>
        <el-table
          :data="registries"
          size="small"
          :empty-text="tt('settings.noRegistries')"
          :row-class-name="s.isMobile ? 'row-tappable' : ''"
          @row-click="onRowClick"
        >
          <el-table-column :label="tt('common.name')" min-width="110">
            <template #default="{ row }">
              <div><span class="primary-line">{{ row.name }}</span></div>
              <div v-if="s.isMobile" class="secondary-line mono">{{ row.url }}</div>
            </template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('settings.colUrl')" min-width="200">
            <template #default="{ row }"><span class="secondary-line mono">{{ row.url }}</span></template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('settings.colUsername')" width="140">
            <template #default="{ row }"><span class="secondary-line">{{ row.username || "-" }}</span></template>
          </el-table-column>
          <el-table-column v-if="!s.isMobile" :label="tt('common.actions')" width="170" fixed="right">
            <template #default="{ row }">
              <el-button link size="small" @click="testRegistry(row)">{{ tt("settings.test") }}</el-button>
              <el-button link size="small" @click="openRegistry(row)">{{ tt("common.edit") }}</el-button>
              <el-button link size="small" class="danger-text" @click="deleteRegistry(row)">{{ tt("common.delete") }}</el-button>
            </template>
          </el-table-column>
        </el-table>

        <!-- Phone-width rows: tap for the bottom action sheet. -->
        <action-sheet
          v-model:visible="sheet.visible"
          :title="sheet.row?.name || ''"
          :subtitle="sheet.row?.url || ''"
          :items="[
            { key: 'test', label: tt('settings.test'), icon: 'CircleCheck' },
            { key: 'edit', label: tt('common.edit'), icon: 'Edit' },
            { key: 'delete', label: tt('common.delete'), icon: 'Delete', danger: true },
          ]"
          :columns="3"
          @select="onSheetSelect"
        />
      </div>

      <!-- MCP external publish -->
      <div class="card">
        <div class="form-card-head">
          <div class="form-card-titles">
            <h2>{{ tt("settings.mcpExternal") }}</h2>
            <p>{{ tt("settings.mcpExternalSub") }}</p>
          </div>
          <el-tag v-if="mcpRemote" size="small" :type="mcpRemote.running ? 'success' : 'info'">
            {{ mcpRemote.running ? tt("settings.mcpListening", { addr: mcpRemote.listenAddr || "" }) : tt("settings.mcpStopped") }}
          </el-tag>
        </div>
        <div class="switch-row">
          <span>{{ tt("settings.mcpEnableExternal") }}</span>
          <el-switch v-model="mcpForm.enabled" />
        </div>
        <div class="form-grid">
          <el-input v-model="mcpForm.domain" :placeholder="tt('settings.mcpDomainPlaceholder')" size="small" />
          <el-input v-model="mcpForm.port" type="number" min="1024" max="65535" :placeholder="tt('settings.mcpPortPlaceholder')" size="small" />
          <el-input v-model="mcpForm.safeNetwork" :placeholder="tt('settings.mcpSafeNetPlaceholder')" size="small" />
        </div>
        <p class="hint">{{ tt("settings.mcpSafeHint") }}</p>
        <p class="hint">{{ tt("settings.mcpTunnel") }}<span class="mono">cloudflared tunnel --url http://127.0.0.1:{{ mcpForm.port || 19090 }}</span></p>
        <p v-if="mcpRemote && mcpRemote.baseUrl" class="hint" v-html="tt('settings.mcpUsersSee', { url: `<span class='mono'>${mcpRemote.baseUrl}</span>` })"></p>
        <div class="form-actions">
          <el-button type="primary" size="small" @click="saveMcpRemote">{{ tt("settings.saveExternal") }}</el-button>
        </div>
      </div>

      <!-- Feishu SSO -->
      <div class="card">
        <div class="form-card-head">
          <div class="form-card-titles">
            <h2>{{ tt("settings.feishuSso") }}</h2>
            <p>{{ tt("settings.feishuSsoSub") }}</p>
          </div>
        </div>
        <div class="switch-row">
          <span>{{ tt("settings.enableFeishu") }}</span>
          <el-switch v-model="feishu.enabled" />
        </div>
        <div class="form-grid">
          <el-input v-model="feishu.appId" :placeholder="tt('settings.appIdPlaceholder')" size="small" />
          <el-input v-model="feishu.appSecret" type="password" show-password :placeholder="feishu.appSecret ? tt('settings.appSecretKeep') : tt('settings.appSecretPlaceholder')" size="small" />
        </div>
        <p class="hint">{{ tt("settings.callbackUrl") }}<span class="mono">{{ location.origin }}/api/feishu/callback</span></p>
        <div class="form-actions">
          <el-button type="primary" size="small" @click="saveFeishu">{{ tt("settings.saveFeishu") }}</el-button>
        </div>
      </div>
    </template>

    <el-dialog v-model="registryDialog.visible" :title="registryDialog.existing ? tt('settings.editRegistry', { name: registryDialog.existing.name }) : tt('settings.addRegistryTitle')" width="440px" append-to-body>
      <el-input v-model="registryDialog.form.name" :placeholder="tt('settings.regNamePlaceholder')" size="small" class="mb" />
      <el-input v-model="registryDialog.form.url" :placeholder="tt('settings.regUrlPlaceholder')" size="small" class="mb" />
      <el-input v-model="registryDialog.form.username" :placeholder="tt('settings.regUserPlaceholder')" size="small" class="mb" />
      <el-input v-model="registryDialog.form.token" type="password" show-password :placeholder="registryDialog.existing ? tt('settings.appSecretKeep') : tt('settings.regTokenPlaceholder2')" size="small" />
      <template #footer>
        <el-button @click="registryDialog.visible = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="saveRegistry">{{ tt("common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api } from "@/api";
import { store, isAdmin, applySiteName, setTheme } from "@/store";
import { tt, setLanguage } from "@/i18n";
import { SUPPORTED_LANGS, getLanguageName, getCurrentLanguage } from "@/lib/i18n.js";
import VIcon from "@/components/VIcon.vue";
import ActionSheet from "@/components/ActionSheet.vue";

export default {
  name: "Settings",
  components: { VIcon, ActionSheet },
  data() {
    return {
      s: store,
      sheet: { visible: false, row: null },
      langs: SUPPORTED_LANGS,
      userLanguage: getCurrentLanguage(),
      defaultLanguage: "en_US",
      sharedDiskReadWrite: store.me?.sharedDiskReadWrite ? "rw" : "ro",
      webhook: "",
      siteName: "",
      capacity: "",
      tenantKey: "",
      registries: [],
      feishu: { appId: "", appSecret: "", enabled: false },
      mcpForm: { enabled: false, port: 19090, domain: "", safeNetwork: "openwrt-lan" },
      mcpRemote: null,
      registryDialog: { visible: false, existing: null, form: { name: "", url: "", username: "", token: "" } },
    };
  },
  computed: {
    location() { return window.location; },
  },
  async mounted() {
    try {
      const res = await api("/api/me/feishu_webhook");
      this.webhook = res.webhook || "";
    } catch { /* empty */ }
    this.defaultLanguage = store.me?.defaultLanguage || "en_US";
    if (!isAdmin()) return;
    const [site, capacity, company, registries, feishu, mcp] = await Promise.all([
      api("/api/admin/settings/site").catch(() => ({ siteName: "" })),
      api("/api/admin/settings/capacity").catch(() => ({ capacity: 50 })),
      api("/api/admin/settings/company").catch(() => ({ tenantKey: "" })),
      api("/api/registries").catch(() => []),
      api("/api/settings/feishu").catch(() => ({ appId: "", appSecret: "", enabled: false })),
      api("/api/admin/mcp/remote").catch(() => null),
    ]);
    this.siteName = site.siteName || "";
    this.capacity = String(capacity.capacity || 50);
    this.tenantKey = company.tenantKey || "";
    this.registries = registries || [];
    this.feishu = { appId: feishu.appId || "", appSecret: feishu.appSecret || "", enabled: !!feishu.enabled };
    if (mcp) {
      this.mcpRemote = mcp;
      this.mcpForm = {
        enabled: !!mcp.enabled,
        port: mcp.port || 19090,
        domain: mcp.domain || "",
        safeNetwork: mcp.safeNetwork || "openwrt-lan",
      };
    }
  },
  methods: {
    tt,
    isAdmin,
    onRowClick(row) {
      if (!store.isMobile) return;
      this.sheet = { visible: true, row };
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      if (!row) return;
      if (item.key === "test") this.testRegistry(row);
      else if (item.key === "edit") this.openRegistry(row);
      else if (item.key === "delete") this.deleteRegistry(row);
    },
    langName: getLanguageName,
    pickTheme(pref) {
      setTheme(pref);
    },
    async saveUserLanguage() {
      try {
        await setLanguage(this.userLanguage);
        ElMessage.success(tt("settings.languageChanged"));
        // Reload so every part of the app picks up the new language.
        setTimeout(() => location.reload(), 500);
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveSharedDisk() {
      const readWrite = this.sharedDiskReadWrite === "rw";
      try {
        await api("/api/user/shareddisk-access", { method: "POST", body: JSON.stringify({ readWrite }) });
        if (store.me) store.me.sharedDiskReadWrite = readWrite;
        ElMessage.success(tt("settings.sharedDiskAccessSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveWebhook() {
      try {
        const res = await api("/api/me/feishu_webhook", { method: "POST", body: JSON.stringify({ webhook: this.webhook || "" }) });
        this.webhook = res.webhook || "";
        ElMessage.success(tt("settings.webhookSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async testWebhook() {
      try {
        await api("/api/me/feishu_webhook/test", { method: "POST" });
        ElMessage.success(tt("settings.webhookTestOk"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveDefaultLanguage() {
      try {
        await api("/api/admin/settings/language", {
          method: "POST",
          body: JSON.stringify({ defaultLanguage: this.defaultLanguage }),
        });
        ElMessage.success(tt("admin.saved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveSite() {
      try {
        const res = await api("/api/admin/settings/site", {
          method: "POST",
          body: JSON.stringify({ siteName: this.siteName || "" }),
        });
        this.siteName = res.siteName || "";
        applySiteName(this.siteName);
        ElMessage.success(tt("settings.siteSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveCapacity() {
      try {
        const res = await api("/api/admin/settings/capacity", {
          method: "POST",
          body: JSON.stringify({ capacity: Number(this.capacity) || 50 }),
        });
        this.capacity = String(res.capacity);
        ElMessage.success(tt("settings.userCapacitySaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveCompany() {
      try {
        const res = await api("/api/admin/settings/company", {
          method: "POST",
          body: JSON.stringify({ tenantKey: this.tenantKey || "" }),
        });
        this.tenantKey = res.tenantKey || "";
        ElMessage.success(tt("settings.companySaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveFeishu() {
      try {
        await api("/api/settings/feishu", {
          method: "POST",
          body: JSON.stringify({
            appId: this.feishu.appId,
            appSecret: this.feishu.appSecret || "",
            enabled: !!this.feishu.enabled,
          }),
        });
        store.feishu = !!this.feishu.enabled;
        ElMessage.success(tt("settings.feishuSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async saveMcpRemote() {
      try {
        const res = await api("/api/admin/mcp/remote", {
          method: "POST",
          body: JSON.stringify({
            enabled: !!this.mcpForm.enabled,
            port: Number(this.mcpForm.port) || 0,
            domain: this.mcpForm.domain || "",
            safeNetwork: this.mcpForm.safeNetwork || "",
          }),
        });
        this.mcpRemote = res;
        // The MCP page builds its links from the user-facing copy of this
        // config, so drop it too rather than show a stale domain.
        store.mcpRemote = null;
        ElMessage.success(res.running ? tt("settings.mcpLive") : tt("settings.mcpSaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    openRegistry(r) {
      this.registryDialog = {
        visible: true,
        existing: r || null,
        form: { name: r?.name || "", url: r?.url || "", username: r?.username || "", token: "" },
      };
    },
    async saveRegistry() {
      const payload = { ...this.registryDialog.form };
      if (this.registryDialog.existing) payload.id = this.registryDialog.existing.id;
      try {
        await api("/api/registries", { method: "POST", body: JSON.stringify(payload) });
        this.registries = (await api("/api/registries").catch(() => this.registries)) || [];
        this.registryDialog.visible = false;
        ElMessage.success(tt("settings.registrySaved"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async deleteRegistry(r) {
      try {
        await ElMessageBox.confirm(tt("settings.deleteRegistryConfirm", { name: r.name }), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/registries/delete", { method: "POST", body: JSON.stringify({ id: r.id }) });
        this.registries = this.registries.filter((x) => x.id !== r.id);
        ElMessage.success(tt("settings.registryDeleted"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async testRegistry(r) {
      try {
        await api("/api/registries/test", { method: "POST", body: JSON.stringify({ id: r.id }) });
        ElMessage.success(tt("settings.loginSuccessful"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>

<style scoped>
/* iOS Settings-style single column: caption + grouped card of rows. */
.settings-page { max-width: 780px; margin: 0 auto; }
.settings-page > * + * { margin-top: 16px; }
.group-caption { margin: 4px 6px -6px; font-size: 12px; font-weight: 600; color: var(--muted); }

.row { display: flex; align-items: center; gap: 12px; padding: 13px 16px; }
.group .row + .row { border-top: 1px solid var(--line); }
.row.wrap { flex-wrap: wrap; }
.row-icon {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
}
.tint-blue { background: #3370ff; }
.tint-green { background: var(--ok); }
.tint-purple { background: #8b5cf6; }
.tint-teal { background: #14b8a6; }
.tint-indigo { background: #6366f1; }
.tint-orange { background: var(--warn); }
.row-main { flex: 1; min-width: 0; }
.row-title { font-size: 13.5px; font-weight: 600; }
.row-desc { font-size: 12px; color: var(--muted); margin-top: 1px; }
.row-select { width: 150px; flex-shrink: 0; }
/* Text-input rows: the editor takes the full width under the title. */
.row-input { flex-basis: 100%; display: flex; gap: 8px; margin-top: 10px; }
.row-input .el-input { flex: 1; }

/* Larger form cards (registries / MCP / Feishu SSO). */
.form-card-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 12px; flex-wrap: wrap; }
.form-card-titles h2 { margin: 0; font-size: 14px; }
.form-card-titles p { margin: 2px 0 0; color: var(--muted); font-size: 12px; }
.switch-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 13.5px; font-weight: 600; padding: 4px 0 12px; }
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.form-grid .el-input:last-child:nth-child(odd) { grid-column: 1 / -1; }
.form-actions { display: flex; justify-content: flex-end; margin-top: 12px; }

.mb { margin-bottom: 8px; }
/* macOS segmented control for the appearance picker. */
.theme-segment {
  display: flex;
  background: var(--fill);
  border-radius: 8px;
  padding: 2px;
  flex-shrink: 0;
}
.theme-seg-btn {
  border: none;
  background: transparent;
  border-radius: 6px;
  padding: 5px 12px;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
  white-space: nowrap;
}
.theme-seg-btn.active {
  background: var(--card);
  color: var(--ink);
  font-weight: 600;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.14);
}
@media (max-width: 640px) {
  .row:has(.theme-segment) { flex-wrap: wrap; }
  .theme-segment { flex-basis: 100%; }
  .theme-seg-btn { flex: 1; }
}
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
@media (max-width: 640px) {
  .form-grid { grid-template-columns: minmax(0, 1fr); }
  .form-grid .el-input:last-child:nth-child(odd) { grid-column: auto; }
  .row-select { width: 132px; }
}
</style>
