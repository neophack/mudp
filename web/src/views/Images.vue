<template>
  <div class="card">
    <div class="card-head">
      <h2>{{ tt("images.title") }}</h2>
      <!-- Image lifecycle (pull/build/import/register/delete) is admin-only:
           only admins curate the image catalog. -->
      <div v-if="isAdmin()" class="head-actions">
        <el-button size="small" :title="tt('images.buildTitle')" @click="openBuild">{{ tt("images.build") }}</el-button>
        <el-button size="small" :title="tt('images.importTitle')" @click="openImport">{{ tt("images.import") }}</el-button>
        <el-button size="small" :title="tt('images.registerTitle')" @click="openRegister">{{ tt("images.register") }}</el-button>
        <el-button size="small" type="primary" @click="openPull">{{ tt("images.pull") }}</el-button>
      </div>
    </div>
    <el-table
      :data="s.images"
      size="small"
      :empty-text="isAdmin() ? tt('images.noImagesAdmin') : tt('images.noImages')"
      :row-class-name="s.isMobile && isAdmin() ? 'row-tappable' : ''"
      @row-click="onRowClick"
    >
      <el-table-column :label="tt('common.name')" :min-width="s.isMobile ? 150 : 230">
        <template #default="{ row }">
          <div class="primary-line">
            {{ row.name }}
            <el-tag v-if="row.isStale" size="small" type="warning" :title="tt('images.staleTitle')">{{ tt("images.staleBadge") }}</el-tag>
          </div>
          <div class="secondary-line mono">{{ row.dockerRef }}</div>
          <div v-if="row.preset && row.preset.description" class="secondary-line">📝 {{ row.preset.description }}</div>
        </template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('images.colSource')" min-width="160">
        <template #default="{ row }"><span class="secondary-line">{{ row.sourceRef }}</span></template>
      </el-table-column>
      <el-table-column v-if="isAdmin() && !s.isMobile" :label="tt('images.colVisible')" width="130">
        <template #default="{ row }"><span class="secondary-line">{{ (row.groups || []).join(", ") || tt("images.allUsers") }}</span></template>
      </el-table-column>
      <el-table-column v-if="!s.isMobile" :label="tt('images.colDefaults')" min-width="220">
        <template #default="{ row }"><span class="secondary-line">{{ presetSummary(row.preset) }}</span></template>
      </el-table-column>
      <el-table-column v-if="isAdmin() && !s.isMobile" :label="tt('common.actions')" width="120" fixed="right">
        <template #default="{ row }">
          <el-button v-if="row.isStale" link icon="Refresh" :title="tt('images.reRegisterTitle')" @click="openReRegister(row)" />
          <el-button link icon="Setting" :title="tt('images.presetHint')" @click="openPreset(row)" />
          <el-button link icon="Delete" class="danger-text" :title="tt('common.delete')" @click="deleteImage(row)" />
        </template>
      </el-table-column>
    </el-table>

    <!-- Phone-width admin rows: tap for the bottom action sheet. -->
    <action-sheet
      v-model:visible="sheet.visible"
      :title="sheet.row?.name || ''"
      :subtitle="sheet.row?.dockerRef || ''"
      :items="sheetItems"
      :columns="4"
      @select="onSheetSelect"
    />

    <!-- Pull -->
    <el-dialog v-model="dialogs.pull" :title="tt('images.pullTitle')" width="480px" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item required>
          <el-input v-model="pullForm.sourceRef" :placeholder="tt('images.pullSourcePlaceholder')" />
        </el-form-item>
        <el-form-item>
          <el-input v-model="pullForm.name" :placeholder="tt('images.pullNamePlaceholder')" />
        </el-form-item>
        <div class="check-grid">
          <label v-for="g in s.groups" :key="g.id" class="check">
            <input v-model="pullForm.groupIds" type="checkbox" :value="g.id" /> {{ g.name }}
          </label>
          <span v-if="!s.groups.length" class="hint">{{ tt("networks.noGroupsYet") }}</span>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="dialogs.pull = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submitPull">{{ tt("images.pullPublish") }}</el-button>
      </template>
    </el-dialog>

    <!-- Build -->
    <el-dialog v-model="dialogs.build" :title="tt('images.buildTitle2')" width="560px" append-to-body top="4vh">
      <el-form label-position="top" size="small">
        <el-form-item required>
          <el-input v-model="buildForm.tags" :placeholder="tt('images.tagsPlaceholder')" />
        </el-form-item>
        <el-form-item>
          <el-input v-model="buildForm.name" :placeholder="tt('images.namePlaceholder2')" />
        </el-form-item>
        <div class="check-grid">
          <label v-for="g in s.groups" :key="g.id" class="check">
            <input v-model="buildForm.groupIds" type="checkbox" :value="g.id" /> {{ g.name }}
          </label>
        </div>
        <el-form-item :label="tt('images.buildArgsPlaceholder')">
          <el-input v-model="buildForm.buildArgs" type="textarea" :rows="2" :placeholder="tt('images.buildArgsPlaceholder')" />
        </el-form-item>
        <el-form-item :label="tt('images.dockerfile')">
          <el-input v-model="buildForm.dockerfile" type="textarea" :rows="10" class="mono" spellcheck="false" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogs.build = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submitBuild">{{ tt("images.build") }}</el-button>
      </template>
    </el-dialog>

    <!-- Import -->
    <el-dialog v-model="dialogs.import" :title="tt('images.importTitle2')" width="440px" append-to-body>
      <input type="file" accept=".tar" @change="importFile = $event.target.files[0]" />
      <p class="hint">{{ tt("images.importHint") }}</p>
      <template #footer>
        <el-button @click="dialogs.import = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submitImport">{{ tt("images.import2") }}</el-button>
      </template>
    </el-dialog>

    <!-- Register (existing local image) -->
    <el-dialog v-model="dialogs.register" :title="tt('images.registerTitle')" width="440px" append-to-body>
      <el-form label-position="top" size="small">
        <el-form-item required>
          <el-input v-model="registerForm.dockerRef" :placeholder="tt('images.registerRefPlaceholder')" />
        </el-form-item>
        <el-form-item>
          <el-input v-model="registerForm.name" :placeholder="tt('images.registerNamePlaceholder')" />
        </el-form-item>
        <div class="check-grid">
          <label v-for="g in s.groups" :key="g.id" class="check">
            <input v-model="registerForm.groupIds" type="checkbox" :value="g.id" /> {{ g.name }}
          </label>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="dialogs.register = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submitRegister">{{ tt("images.register2") }}</el-button>
      </template>
    </el-dialog>

    <!-- Re-register (fix stale rows whose name is a raw image ID) -->
    <el-dialog v-model="dialogs.reregister" :title="tt('images.reRegisterTitle')" width="440px" append-to-body>
      <p class="hint">{{ tt("images.reRegisterHint", { name: reregisterName }) }}</p>
      <el-input v-model="reregisterForm.name" :placeholder="tt('images.reRegisterNamePlaceholder')" />
      <template #footer>
        <el-button @click="dialogs.reregister = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" @click="submitReRegister">{{ tt("images.reRegister") }}</el-button>
      </template>
    </el-dialog>

    <!-- Preset editor (admin) -->
    <el-dialog v-model="dialogs.preset" :title="tt('images.titleEdit', { name: presetName })" width="640px" top="3vh" append-to-body>
      <p class="hint">{{ tt("images.presetHint") }}</p>
      <el-form label-position="top" size="small">
        <el-form-item>
          <el-input v-model="presetForm.description" :placeholder="tt('images.descPlaceholder')" />
        </el-form-item>
        <el-form-item :label="tt('images.gpus')">
          <el-select v-model="presetForm.gpus" style="width: 100%">
            <el-option value="" :label="tt('images.gpusUserDecides')" />
            <el-option value="none" label="none" />
            <el-option value="all" label="all" />
          </el-select>
        </el-form-item>
        <el-form-item :label="tt('images.envLabel')">
          <el-input v-model="presetForm.env" type="textarea" :rows="3" spellcheck="false" />
          <div class="env-gen">
            <el-select v-model="envGenType" size="small" style="width: 220px">
              <el-option value="random_password:16" :label="tt('images.genRandomPassword')" />
              <el-option value="random_number:6" :label="tt('images.genRandomNumber')" />
              <el-option value="random_string:12" :label="tt('images.genRandomString')" />
              <el-option value="sequence:4" :label="tt('images.genSequence')" />
            </el-select>
            <el-button size="small" @click="insertEnvGen">{{ tt("images.genInsert") }}</el-button>
          </div>
          <p class="hint">{{ tt("images.envGenHint") }}</p>
        </el-form-item>
        <el-form-item :label="tt('images.portsLabel')">
          <el-input v-model="presetForm.ports" type="textarea" :rows="2" spellcheck="false" />
        </el-form-item>
        <div class="check-grid">
          <label class="check"><input v-model="presetForm.forward8080" type="checkbox"> {{ tt("images.forward8080") }}</label>
          <label class="check"><input v-model="presetForm.forward8090" type="checkbox"> {{ tt("images.forward8090") }}</label>
          <label class="check"><input v-model="presetForm.mountNetdisk" type="checkbox"> {{ tt("images.presetNetdisk") }}</label>
          <label class="check"><input v-model="presetForm.mountShm" type="checkbox"> {{ tt("images.presetShm") }}</label>
          <label class="check"><input v-model="presetForm.mountSharedDisk" type="checkbox"> {{ tt("images.presetSharedDisk") }}</label>
        </div>
        <el-form-item :label="tt('images.loginHttpsLabel')">
          <p class="hint" style="margin-top: 0">{{ tt("images.loginHttpsHint") }}</p>
          <div class="check-grid">
            <label class="check"><input v-model="presetForm.requireLogin8080" type="checkbox"> {{ tt("images.requireLogin8080") }}</label>
            <label class="check"><input v-model="presetForm.https8080" type="checkbox"> {{ tt("images.https8080") }}</label>
            <label class="check"><input v-model="presetForm.requireLogin8090" type="checkbox"> {{ tt("images.requireLogin8090") }}</label>
            <label class="check"><input v-model="presetForm.https8090" type="checkbox"> {{ tt("images.https8090") }}</label>
          </div>
        </el-form-item>
        <el-form-item :label="tt('images.novncPassword8080Label')">
          <div class="two-col">
            <el-input v-model="presetForm.novncPasswordEnv8080" placeholder="VNC_PW" />
            <el-input v-model="presetForm.novncPasswordParam8080" placeholder="password" />
          </div>
          <p class="hint">{{ tt("images.novncPassword8080Hint") }}</p>
        </el-form-item>
        <el-form-item :label="tt('images.novncPassword8090Label')">
          <div class="two-col">
            <el-input v-model="presetForm.novncPasswordEnv8090" placeholder="JUPYTER_TOKEN" />
            <el-input v-model="presetForm.novncPasswordParam8090" placeholder="tkn" />
          </div>
          <p class="hint">{{ tt("images.novncPassword8090Hint") }}</p>
        </el-form-item>
        <!-- Selectable networks are the candidate pool for this image; the
             default networks (pre-checked on create) must live inside it. -->
        <template v-if="poolNetworks.length">
          <el-form-item :label="tt('images.selectableNetworks')">
            <div class="check-grid">
              <label v-for="n in poolNetworks" :key="n.fullName || n.name" class="check">
                <input v-model="presetForm.selectableNetworks" type="checkbox" :value="n.fullName || n.name" /> {{ n.name }}
              </label>
            </div>
            <p class="hint">{{ tt("images.selectableNetworksHint") }}</p>
          </el-form-item>
          <el-form-item :label="tt('images.defaultNetworks')">
            <div class="check-grid">
              <label v-for="val in presetForm.selectableNetworks" :key="val" class="check">
                <input v-model="presetForm.networks" type="checkbox" :value="val" /> {{ networkNameOf(val) }}
              </label>
              <span v-if="!presetForm.selectableNetworks.length" class="hint">{{ tt("images.defaultNetworksHint") }}</span>
            </div>
            <p class="hint">{{ tt("images.defaultNetworksHint") }}</p>
          </el-form-item>
        </template>
        <el-form-item :label="tt('images.restartPolicy')">
          <el-select v-model="presetForm.restartPolicy" style="width: 100%">
            <el-option value="" :label="tt('images.gpusUserDecides')" />
            <el-option value="unless-stopped" label="unless-stopped" />
            <el-option value="always" label="always" />
            <el-option value="on-failure" label="on-failure" />
            <el-option value="no" label="no" />
          </el-select>
        </el-form-item>
        <el-form-item :label="tt('images.devices')">
          <el-input v-model="presetForm.devices" type="textarea" :rows="4" spellcheck="false" />
        </el-form-item>
        <el-form-item :label="tt('images.cdiDevices')">
          <el-input v-model="presetForm.cdiDevices" type="textarea" :rows="2" spellcheck="false" />
        </el-form-item>
        <el-form-item :label="tt('images.visibleTo')">
          <div class="check-grid">
            <label v-for="g in s.groups" :key="g.id" class="check">
              <input v-model="presetGroupIds" type="checkbox" :value="g.id" /> {{ g.name }}
            </label>
            <span v-if="!s.groups.length" class="hint">{{ tt("images.noGroupsHint") }}</span>
          </div>
          <p class="hint">{{ tt("images.visibleHint") }}</p>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogs.preset = false">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" :loading="presetSaving" @click="submitPreset">{{ tt("common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- Streaming progress (pull / build / import) -->
    <el-dialog v-model="dialogs.progress" :title="progressTitle" width="640px" top="5vh" append-to-body>
      <sse-progress :state="progress" @retry="progressRetry" @hide="dialogs.progress = false" />
    </el-dialog>
  </div>
</template>

<script>
import { ElMessage, ElMessageBox } from "element-plus";
import { api, readCSRFCookie, readSSE } from "@/api";
import { store, isAdmin, refreshSection } from "@/store";
import { tt } from "@/i18n";
import { registerJob } from "@/jobs";
import SseProgress from "@/components/SseProgress.vue";
import ActionSheet from "@/components/ActionSheet.vue";

const SAMPLE_DOCKERFILE = `FROM ubuntu:22.04
RUN apt-get update && apt-get install -y curl
WORKDIR /workspace
`;

// Pre-fills the devices field for images that need direct /dev access to
// NVIDIA GPUs (e.g. runtimes without --gpus/CDI support).
const DEFAULT_NVIDIA_DEVICES = [
  "/dev/nvidia0",
  "/dev/nvidia1",
  "/dev/nvidia2",
  "/dev/nvidia3",
  "/dev/nvidiactl",
  "/dev/nvidia-uvm",
  "/dev/nvidia-uvm-tools",
];

function lines(raw) {
  return String(raw || "").split("\n").map((s) => s.trim()).filter(Boolean);
}

function parseKV(raw) {
  const out = {};
  for (const line of String(raw).split("\n")) {
    const [k, ...rest] = line.split("=");
    if (k && rest.length) out[k.trim()] = rest.join("=").trim();
  }
  return out;
}

// The editable shape of an image preset; p defaults to an empty preset so the
// same factory seeds the dialog before it is ever opened.
function presetFormFrom(p = {}) {
  return {
    description: p.description || "",
    gpus: p.gpus || "",
    env: (p.env || []).join("\n"),
    ports: (p.ports || []).join("\n"),
    forward8080: !!p.forward8080,
    forward8090: !!p.forward8090,
    mountNetdisk: !!p.mountNetdisk,
    mountShm: !!p.mountShm,
    mountSharedDisk: !!p.mountSharedDisk,
    requireLogin8080: !!p.requireLogin8080,
    requireLogin8090: !!p.requireLogin8090,
    https8080: !!p.https8080,
    https8090: !!p.https8090,
    novncPasswordEnv8080: p.novncPasswordEnv8080 || "",
    novncPasswordParam8080: p.novncPasswordParam8080 || "",
    novncPasswordEnv8090: p.novncPasswordEnv8090 || "",
    novncPasswordParam8090: p.novncPasswordParam8090 || "",
    selectableNetworks: (p.selectableNetworks || []).slice(),
    networks: (p.networks || []).slice(),
    restartPolicy: p.restartPolicy || "",
    devices: (p.devices && p.devices.length ? p.devices : DEFAULT_NVIDIA_DEVICES).join("\n"),
    cdiDevices: (p.cdiDevices || []).join("\n"),
  };
}

export default {
  name: "Images",
  components: { SseProgress, ActionSheet },
  data() {
    return {
      s: store,
      dialogs: { pull: false, build: false, import: false, register: false, reregister: false, preset: false, progress: false },
      sheet: { visible: false, row: null },
      pullForm: { sourceRef: "", name: "", groupIds: [] },
      buildForm: { tags: "", name: "", groupIds: [], buildArgs: "", dockerfile: SAMPLE_DOCKERFILE },
      registerForm: { dockerRef: "", name: "", groupIds: [] },
      reregisterForm: { imageId: 0, name: "" },
      // el-dialog renders its slot (hidden) at mount, so the form must be a
      // real object from the start; openPreset() refills it from the preset.
      presetForm: presetFormFrom(),
      envGenType: "random_password:16",
      presetGroupIds: [],
      presetImageId: 0,
      presetName: "",
      presetSaving: false,
      reregisterName: "",
      importFile: null,
      progress: { active: false, label: "", logs: "", error: "" },
      progressTitle: "",
      progressKind: "",
      progressRetryFn: null,
    };
  },
  computed: {
    poolNetworks() {
      return (store.networks || []).filter((n) => !n.system);
    },
    sheetItems() {
      const r = this.sheet.row;
      if (!r) return [];
      const items = [];
      if (r.isStale) items.push({ key: "reregister", label: tt("images.reRegister"), icon: "Refresh" });
      items.push(
        { key: "preset", label: tt("images.presetHint"), icon: "Setting" },
        { key: "delete", label: tt("common.delete"), icon: "Delete", danger: true },
      );
      return items;
    },
  },
  methods: {
    tt,
    isAdmin,
    onRowClick(row) {
      if (!store.isMobile || !isAdmin()) return;
      this.sheet = { visible: true, row };
    },
    onSheetSelect(item) {
      const row = this.sheet.row;
      this.sheet.visible = false;
      if (!row) return;
      if (item.key === "reregister") this.openReRegister(row);
      else if (item.key === "preset") this.openPreset(row);
      else if (item.key === "delete") this.deleteImage(row);
    },
    presetSummary(p) {
      if (!p) return "—";
      const bits = [];
      if (p.gpus) bits.push("GPU:" + p.gpus);
      if ((p.ports || []).length) bits.push("ports:" + p.ports.join(","));
      if ((p.devices || []).length) bits.push("dev:" + p.devices.length);
      if ((p.cdiDevices || []).length) bits.push("cdi:" + p.cdiDevices.length);
      if (p.novncPasswordEnv8080) bits.push("8080:?" + (p.novncPasswordParam8080 || "password") + "=" + p.novncPasswordEnv8080);
      if (p.novncPasswordEnv8090) bits.push("8090:?" + (p.novncPasswordParam8090 || "tkn") + "=" + p.novncPasswordEnv8090);
      const loginPorts = [p.requireLogin8080 && "8080", p.requireLogin8090 && "8090"].filter(Boolean);
      if (loginPorts.length) bits.push("login:" + loginPorts.join(","));
      const httpsPorts = [p.https8080 && "8080", p.https8090 && "8090"].filter(Boolean);
      if (httpsPorts.length) bits.push("https:" + httpsPorts.join(","));
      return bits.length ? bits.join(" · ") : "—";
    },
    networkNameOf(val) {
      const n = (store.networks || []).find((x) => (x.fullName || x.name) === val);
      return n ? n.name : val;
    },
    openPull() {
      this.pullForm = { sourceRef: "", name: "", groupIds: [] };
      this.dialogs.pull = true;
    },
    openBuild() {
      this.buildForm = { tags: "", name: "", groupIds: [], buildArgs: "", dockerfile: SAMPLE_DOCKERFILE };
      this.dialogs.build = true;
    },
    openImport() {
      this.importFile = null;
      this.dialogs.import = true;
    },
    openRegister() {
      this.registerForm = { dockerRef: "", name: "", groupIds: [] };
      this.dialogs.register = true;
    },
    // Suggest a name from the source ref unless that itself looks like a hash.
    openReRegister(image) {
      let suggested = "";
      if (image.sourceRef) {
        const base = String(image.sourceRef).split("/").pop().split(":")[0];
        suggested = /^[0-9a-f]{12,}$/i.test(base) ? "" : base;
      }
      this.reregisterForm = { imageId: image.id, name: suggested };
      this.reregisterName = image.name;
      this.dialogs.reregister = true;
    },
    openPreset(image) {
      const p = image.preset || {};
      this.presetImageId = image.id;
      this.presetName = image.name;
      this.presetGroupIds = (store.groups || []).filter((g) => (image.groups || []).includes(g.name)).map((g) => g.id);
      this.presetForm = presetFormFrom(p);
      this.dialogs.preset = true;
    },
    insertEnvGen() {
      this.presetForm.env = (this.presetForm.env ? this.presetForm.env + "\n" : "") + "{{" + this.envGenType + "}}";
    },
    async submitPreset() {
      const f = this.presetForm;
      // Client-side mirror of the backend ValidatePreset check: a default
      // network must be among the selectable pool (immediate feedback, no
      // round-trip).
      if (f.selectableNetworks.length && f.networks.some((n) => !f.selectableNetworks.includes(n))) {
        ElMessage.error(tt("images.defaultNetworkNotSelectable"));
        return;
      }
      const preset = {
        description: (f.description || "").trim(),
        gpus: (f.gpus || "").trim(),
        env: lines(f.env),
        ports: lines(f.ports),
        forward8080: f.forward8080 || undefined,
        forward8090: f.forward8090 || undefined,
        mountNetdisk: f.mountNetdisk || undefined,
        mountShm: f.mountShm || undefined,
        mountSharedDisk: f.mountSharedDisk || undefined,
        requireLogin8080: f.requireLogin8080 || undefined,
        requireLogin8090: f.requireLogin8090 || undefined,
        https8080: f.https8080 || undefined,
        https8090: f.https8090 || undefined,
        novncPasswordEnv8080: (f.novncPasswordEnv8080 || "").trim(),
        novncPasswordParam8080: (f.novncPasswordParam8080 || "").trim(),
        novncPasswordEnv8090: (f.novncPasswordEnv8090 || "").trim(),
        novncPasswordParam8090: (f.novncPasswordParam8090 || "").trim(),
        selectableNetworks: f.selectableNetworks,
        networks: f.networks,
        restartPolicy: (f.restartPolicy || "").trim(),
        devices: lines(f.devices),
        cdiDevices: lines(f.cdiDevices),
      };
      this.presetSaving = true;
      try {
        await Promise.all([
          api("/api/images/preset", { method: "POST", body: JSON.stringify({ imageId: Number(this.presetImageId), preset }) }),
          api("/api/images/groups", { method: "POST", body: JSON.stringify({ imageId: Number(this.presetImageId), groupIds: this.presetGroupIds.map(Number) }) }),
        ]);
        this.dialogs.preset = false;
        await refreshSection("images");
        ElMessage.success(tt("images.presetUpdated"));
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.presetSaving = false;
      }
    },
    async deleteImage(image) {
      try {
        await ElMessageBox.confirm(tt("images.deleteConfirm"), tt("common.delete"), {
          confirmButtonText: tt("common.confirm"),
          cancelButtonText: tt("common.cancel"),
          type: "warning",
        });
      } catch { return; }
      try {
        await api("/api/images/delete", {
          method: "POST",
          body: JSON.stringify({ imageId: Number(image.id), dockerRef: image.dockerRef }),
        });
        await refreshSection("images");
        ElMessage.success(tt("images.imageDeleted"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async submitRegister() {
      const payload = {
        dockerRef: this.registerForm.dockerRef.trim(),
        name: this.registerForm.name.trim(),
        groupIds: this.registerForm.groupIds.map(Number),
      };
      if (!payload.dockerRef) {
        ElMessage.warning(tt("images.tagRequired"));
        return;
      }
      try {
        await api("/api/images/register", { method: "POST", body: JSON.stringify(payload) });
        this.dialogs.register = false;
        await refreshSection("images");
        ElMessage.success(tt("images.imageRegistered"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    async submitReRegister() {
      const name = this.reregisterForm.name.trim();
      if (!name) {
        ElMessage.warning(tt("images.reRegisterNameRequired"));
        return;
      }
      try {
        await api("/api/images/reregister", {
          method: "POST",
          body: JSON.stringify({ imageId: Number(this.reregisterForm.imageId), name }),
        });
        this.dialogs.reregister = false;
        await refreshSection("images");
        ElMessage.success(tt("images.imageReRegistered"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
    submitImport() {
      if (!this.importFile) {
        ElMessage.warning(tt("images.selectTar"));
        return;
      }
      this.streamImport(this.importFile);
    },
    // Common SSE job runner for pull/build/import.
    async runStream({ kind, title, label, url, body, signal, job, onDone, retry }) {
      this.progress = { active: true, label, logs: "", error: "" };
      this.progressTitle = title;
      this.progressKind = kind;
      this.progressRetryFn = retry || null;
      this.dialogs.progress = true;
      try {
        const headers = { Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || store.csrfToken || "" };
        if (body !== undefined && !(body instanceof File)) headers["Content-Type"] = "application/json";
        const res = await fetch(url, {
          method: "POST",
          credentials: "same-origin",
          headers,
          body: body === undefined ? undefined : body instanceof File ? body : JSON.stringify(body),
          signal,
        });
        if (!res.ok) {
          const data = await res.json().catch(() => ({}));
          this.progress.error = data.error || tt("create.requestFailed", { status: res.status });
          this.progress.active = false;
          job.error(this.progress.error);
          ElMessage.error(this.progress.error);
          return;
        }
        await readSSE(res, (event, data) => {
          if (event === "progress") {
            const line = data.message || "";
            this.progress.logs += line + "\n";
            job.log(line);
          } else if (event === "error") {
            this.progress.error = data.message || tt("images.pullFailed");
            this.progress.logs += `[error] ${this.progress.error}\n`;
            this.progress.active = false;
            job.error(this.progress.error);
            ElMessage.error(this.progress.error);
          } else if (event === "done") {
            this.progress.active = false;
            if (onDone) onDone(data);
            refreshSection("images");
            setTimeout(() => { this.dialogs.progress = false; }, 800);
          } else if (event === "cancelled") {
            this.progress.active = false;
            this.progress.logs += `[cancelled] ${data.message || ""}\n`;
            job.cancel();
          }
        });
      } catch (err) {
        this.progress.active = false;
        if (job.signal.aborted) {
          this.progress.error = tt("create.cancelled");
          job.cancel();
        } else {
          this.progress.error = err.message;
          job.error(err.message);
        }
        this.progress.logs += `[error] ${this.progress.error}\n`;
        ElMessage.error(this.progress.error);
      }
    },
    progressRetry() {
      if (this.progressRetryFn) this.progressRetryFn();
      else this.dialogs.progress = false;
    },
    submitPull() {
      const payload = {
        sourceRef: this.pullForm.sourceRef.trim(),
        name: this.pullForm.name.trim(),
        groupIds: this.pullForm.groupIds.map(Number),
      };
      if (!payload.sourceRef) return;
      this.dialogs.pull = false;
      const job = registerJob({ kind: "image.pull", name: payload.sourceRef });
      this.runStream({
        kind: "pull",
        title: tt("images.pullTitle"),
        label: tt("images.pulling", { name: payload.sourceRef }),
        url: "/api/images/pull/stream",
        body: payload,
        signal: job.signal,
        job,
        onDone: (data) => {
          this.progress.logs += `[done] ${tt("images.publishedAs", { ref: data.dockerRef })}\n`;
          job.done(tt("images.publishedAs", { ref: data.dockerRef }));
          ElMessage.success(tt("images.imagePulled"));
        },
        retry: () => this.openPull(),
      });
    },
    submitBuild() {
      const payload = {
        tags: String(this.buildForm.tags || "").split(",").map((s) => s.trim()).filter(Boolean),
        name: (this.buildForm.name || "").trim(),
        groupIds: this.buildForm.groupIds.map(Number),
        buildArgs: parseKV(this.buildForm.buildArgs || ""),
        dockerfile: this.buildForm.dockerfile,
      };
      if (payload.tags.length === 0 || !payload.dockerfile) {
        ElMessage.warning(tt("images.tagsRequired"));
        return;
      }
      this.dialogs.build = false;
      const name = payload.tags.join(", ");
      const job = registerJob({ kind: "image.build", name });
      this.runStream({
        kind: "build",
        title: tt("images.buildTitle2"),
        label: tt("images.building", { name }),
        url: "/api/images/build/stream",
        body: payload,
        signal: job.signal,
        job,
        onDone: (data) => {
          const tagList = (data.tags || []).join(", ");
          this.progress.logs += `[done] ${tt("images.built", { tags: tagList })}\n`;
          job.done(tt("images.built", { tags: tagList }));
          ElMessage.success(tt("images.imageBuilt"));
        },
        retry: () => this.openBuild(),
      });
    },
    async streamImport(file) {
      this.dialogs.import = false;
      const job = registerJob({ kind: "image.import", name: file.name });
      this.progress = { active: true, label: tt("images.importing"), logs: tt("images.importing") + "\n", error: "" };
      this.progressTitle = tt("images.importTitle2");
      this.progressKind = "import";
      this.progressRetryFn = null;
      this.dialogs.progress = true;
      try {
        const res = await fetch("/api/images/import", {
          method: "POST",
          credentials: "same-origin",
          body: file,
          headers: { Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || store.csrfToken || "" },
          signal: job.signal,
        });
        if (!res.ok) {
          const data = await res.json().catch(() => ({}));
          this.progress.error = data.error || tt("images.importFailed", { status: res.status });
          job.error(this.progress.error);
          ElMessage.error(this.progress.error);
          return;
        }
        await readSSE(res, (event, data) => {
          if (event === "progress") {
            const line = data.message || "";
            this.progress.logs += line + "\n";
            job.log(line);
          } else if (event === "error") {
            this.progress.error = data.message || tt("images.importFailed2");
            job.error(this.progress.error);
            ElMessage.error(this.progress.error);
          } else if (event === "done") {
            this.progress.logs += `[done] ${tt("images.imageLoaded")}\n`;
            job.done(tt("images.imageLoaded"));
            ElMessage.success(tt("images.imageImported"));
            refreshSection("images");
            setTimeout(() => { this.dialogs.progress = false; }, 800);
          } else if (event === "cancelled") {
            this.progress.active = false;
            this.progress.logs += `[cancelled] ${data.message || ""}\n`;
            job.cancel();
          }
        });
      } catch (err) {
        if (job.signal.aborted) {
          this.progress.error = tt("create.cancelled");
          job.cancel();
        } else {
          this.progress.error = err.message;
          job.error(err.message);
        }
        ElMessage.error(this.progress.error);
      }
    },
  },
};
</script>

<style scoped>
.card-head { display: flex; align-items: center; margin-bottom: 12px; }
.card-head h2 { margin: 0; font-size: 14px; flex: 1; }
.head-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.primary-line { font-weight: 600; }
.secondary-line { color: var(--muted); font-size: 12px; }
.check-grid { display: flex; flex-wrap: wrap; gap: 8px 14px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.env-gen { display: flex; gap: 8px; align-items: center; margin: 6px 0; }
.two-col { display: flex; gap: 8px; }
</style>
