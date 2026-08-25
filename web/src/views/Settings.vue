<template>
  <div class="stack">
    <!-- Personal section -->
    <div class="section-head">
      <v-icon name="security" />
      <div class="section-head-text">
        <h3>{{ tt("settings.sectionPersonal") }}</h3>
        <p class="hint">{{ tt("settings.sectionPersonalSub") }}</p>
      </div>
    </div>
    <div class="settings-grid">
      <div class="card">
        <div class="card-head"><h2>{{ tt("settings.language") }}</h2><span class="card-head-sub">{{ tt("settings.languageSub") }}</span></div>
        <label class="field-label">{{ tt("settings.currentLanguage") }}:</label>
        <el-select v-model="userLanguage" size="small" style="width: 100%">
          <el-option v-for="lang in langs" :key="lang" :value="lang" :label="langName(lang)" />
        </el-select>
        <el-button type="primary" size="small" style="margin-top: 10px" @click="saveUserLanguage">{{ tt("common.save") }}</el-button>
      </div>

      <!-- Shared-disk access: the caller's persistent preference for their
           subfolder (read-only default or read-write). Hidden when the group
           has no shared-disk root configured. -->
      <div v-if="s.me?.sharedDiskConfigured" class="card">
        <div class="card-head"><h2>{{ tt("settings.sharedDiskAccess") }}</h2><span class="card-head-sub">{{ tt("settings.sharedDiskAccessSub") }}</span></div>
        <p class="hint">{{ tt("settings.sharedDiskAccessHint") }}</p>
        <label class="field-label">{{ tt("settings.sharedDiskAccessLabel") }}:</label>
        <el-select v-model="sharedDiskReadWrite" size="small" style="width: 100%">
          <el-option value="ro" :label="tt('settings.sharedDiskReadOnly')" />
          <el-option value="rw" :label="tt('settings.sharedDiskReadWrite')" />
        </el-select>
        <el-button type="primary" size="small" style="margin-top: 10px" @click="saveSharedDisk">{{ tt("common.save") }}</el-button>
      </div>

      <!-- Personal Feishu custom-bot webhook for process-exit notifications. -->
      <div class="card">
        <div class="card-head"><h2>{{ tt("settings.feishuNotify") }}</h2><span class="card-head-sub">{{ tt("settings.feishuNotifySub") }}</span></div>
        <p class="hint">{{ tt("settings.feishuNotifyHint") }}</p>
        <el-input v-model="webhook" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/…" size="small" />
        <div class="page-actions" style="margin-top: 10px">
          <el-button type="primary" size="small" @click="saveWebhook">{{ tt("common.save") }}</el-button>
          <el-button size="small" @click="testWebhook">{{ tt("settings.testWebhook") }}</el-button>
        </div>
      </div>
    </div>

    <!-- Admin section -->
    <template v-if="isAdmin()">
      <div class="section-head">
        <v-icon name="settings" />
        <div class="section-head-text">
          <h3>{{ tt("settings.sectionAdmin") }}</h3>
          <p class="hint">{{ tt("settings.sectionAdminSub") }}</p>
        </div>
      </div>
      <div class="settings-grid">
        <div class="card">
          <div class="card-head"><h2>{{ tt("settings.defaultLanguage") }}</h2><span class="card-head-sub">{{ tt("settings.defaultLanguageSub") }}</span></div>
          <p class="hint">{{ tt("admin.userCanOverride") }}</p>
          <label class="field-label">{{ tt("admin.defaultLanguage") }}:</label>
          <el-select v-model="defaultLanguage" size="small" style="width: 100%">
            <el-option v-for="lang in langs" :key="lang" :value="lang" :label="langName(lang)" />
          </el-select>
          <p class="hint">{{ tt("admin.newUsersWillUse") }}</p>
          <el-button type="primary" size="small" @click="saveDefaultLanguage">{{ tt("common.save") }}</el-button>
        </div>

        <div class="card">
          <div class="card-head"><h2>{{ tt("settings.siteSettings") }}</h2><span class="card-head-sub">{{ tt("settings.siteNameSub") }}</span></div>
          <p class="hint">{{ tt("settings.siteNameHint") }}</p>
          <el-input v-model="siteName" :placeholder="tt('settings.siteNamePlaceholder')" size="small" />
          <el-button type="primary" size="small" style="margin-top: 10px" @click="saveSite">{{ tt("settings.saveSite") }}</el-button>
        </div>

        <div class="card">
          <div class="card-head"><h2>{{ tt("settings.userCapacity") }}</h2><span class="card-head-sub">{{ tt("settings.userCapacityHint") }}</span></div>
          <p class="hint">{{ tt("settings.userCapacityHint") }}</p>
          <el-input v-model="capacity" type="number" min="1" max="9999" :placeholder="tt('settings.userCapacityPlaceholder')" size="small" />
          <el-button type="primary" size="small" style="margin-top: 10px" @click="saveCapacity">{{ tt("settings.saveUserCapacity") }}</el-button>
        </div>

        <div class="card">
          <div class="card-head"><h2>{{ tt("settings.companyRestriction") }}</h2><span class="card-head-sub">{{ tt("settings.companyRestrictionSub") }}</span></div>
          <p class="hint">{{ tt("settings.companyRestrictionHint") }}</p>
          <el-input v-model="tenantKey" :placeholder="tt('settings.tenantKeyPlaceholder')" size="small" />
          <el-button type="primary" size="small" style="margin-top: 10px" @click="saveCompany">{{ tt("settings.saveCompany") }}</el-button>
        </div>

        <!-- Registries (wide) -->
        <div class="card span-2">
          <div class="card-head">
            <h2>{{ tt("settings.registries") }}</h2>
            <el-button type="primary" size="small" @click="openRegistry(null)">{{ tt("settings.addRegistry") }}</el-button>
          </div>
          <span class="card-head-sub block">{{ tt("settings.registriesSub") }}</span>
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
          <div class="card-head">
            <h2>{{ tt("settings.mcpExternal") }}</h2>
            <el-tag v-if="mcpRemote" size="small" :type="mcpRemote.running ? 'success' : 'info'">
              {{ mcpRemote.running ? tt("settings.mcpListening", { addr: mcpRemote.listenAddr || "" }) : tt("settings.mcpStopped") }}
            </el-tag>
          </div>
          <span class="card-head-sub block">{{ tt("settings.mcpExternalSub") }}</span>
          <p class="hint">{{ tt("settings.mcpHint") }}</p>
          <el-input v-model="mcpForm.domain" :placeholder="tt('settings.mcpDomainPlaceholder')" size="small" class="mb" />
          <el-input v-model="mcpForm.port" type="number" min="1024" max="65535" :placeholder="tt('settings.mcpPortPlaceholder')" size="small" class="mb" />
          <el-input v-model="mcpForm.safeNetwork" :placeholder="tt('settings.mcpSafeNetPlaceholder')" size="small" class="mb" />
          <label class="check"><input v-model="mcpForm.enabled" type="checkbox"> {{ tt("settings.mcpEnableExternal") }}</label>
          <p class="hint">{{ tt("settings.mcpSafeHint") }}</p>
          <p class="hint">{{ tt("settings.mcpTunnel") }}<span class="mono">cloudflared tunnel --url http://127.0.0.1:{{ mcpForm.port || 19090 }}</span></p>
          <p v-if="mcpRemote && mcpRemote.baseUrl" class="hint" v-html="tt('settings.mcpUsersSee', { url: `<span class='mono'>${mcpRemote.baseUrl}</span>` })"></p>
          <el-button type="primary" size="small" style="margin-top: 10px" @click="saveMcpRemote">{{ tt("settings.saveExternal") }}</el-button>
        </div>

        <!-- Feishu SSO -->
        <div class="card">
          <div class="card-head"><h2>{{ tt("settings.feishuSso") }}</h2><span class="card-head-sub">{{ tt("settings.feishuSsoSub") }}</span></div>
          <p class="hint">{{ tt("settings.feishuHint") }}</p>
          <el-input v-model="feishu.appId" :placeholder="tt('settings.appIdPlaceholder')" size="small" class="mb" />
          <el-input v-model="feishu.appSecret" type="password" show-password :placeholder="feishu.appSecret ? tt('settings.appSecretKeep') : tt('settings.appSecretPlaceholder')" size="small" class="mb" />
          <label class="check"><input v-model="feishu.enabled" type="checkbox"> {{ tt("settings.enableFeishu") }}</label>
          <p class="hint">{{ tt("settings.callbackUrl") }}<span class="mono">{{ location.origin }}/api/feishu/callback</span></p>
          <el-button type="primary" size="small" style="margin-top: 10px" @click="saveFeishu">{{ tt("settings.saveFeishu") }}</el-button>
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
import { store, isAdmin, applySiteName } from "@/store";
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
.stack > * + * { margin-top: 16px; }
.section-head { display: flex; align-items: center; gap: 12px; color: #334155; }
.section-head h3 { margin: 0; font-size: 14.5px; }
.section-head p { margin: 2px 0 0; }
.settings-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; align-items: start; }
.span-2 { grid-column: span 2; }
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; display: flex; align-items: center; gap: 8px; }
.card-head-sub { color: var(--muted); font-size: 12px; }
.card-head-sub.block { display: block; width: 100%; margin-bottom: 8px; }
.field-label { font-size: 12.5px; color: #475569; font-weight: 600; display: block; margin-bottom: 6px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; margin: 8px 0; }
.mb { margin-bottom: 8px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
@media (max-width: 1000px) { .settings-grid { grid-template-columns: minmax(0, 1fr); } .span-2 { grid-column: span 1; } }
</style>
