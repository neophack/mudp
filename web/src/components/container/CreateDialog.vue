<template>
  <el-dialog :model-value="visible" :title="tt('create.title')" width="620px" top="4vh" append-to-body @update:model-value="$emit('update:visible', $event)">
    <template v-if="!progress.active">
      <el-form label-position="top" size="small" class="compact" @submit.prevent="submit">
        <el-form-item :label="tt('common.name')" required>
          <el-input v-model="form.name" :placeholder="tt('create.namePlaceholder')" />
        </el-form-item>
        <el-form-item required>
          <el-select v-model="form.image" style="width: 100%" :placeholder="tt('create.selectImage')" @change="applyPreset">
            <el-option v-for="img in images" :key="img.name" :value="img.name" :label="img.name + (hasPreset(img) ? ' ⚙' : '')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="tt('common.gpu')">
          <el-select v-model="form.gpus" style="width: 100%">
            <el-option value="none" :label="tt('create.noGpu')" />
            <el-option value="all" :label="tt('create.allGpus')" />
            <el-option v-for="i in gpuIndexes" :key="i" :value="String(i)" :label="`GPU ${i}`" />
          </el-select>
        </el-form-item>
        <el-form-item :label="tt('create.envPlaceholder')">
          <el-input v-model="form.env" type="textarea" :rows="3" :placeholder="tt('create.envPlaceholder')" />
        </el-form-item>
        <el-form-item :label="tt('create.portsPlaceholder')">
          <el-input v-model="form.ports" type="textarea" :rows="2" :placeholder="portPlaceholder" />
        </el-form-item>
        <el-form-item :label="tt('create.mountsPlaceholder')">
          <el-input v-model="form.mounts" type="textarea" :rows="2" :placeholder="mountsPlaceholder" />
        </el-form-item>
        <el-form-item v-if="attachableNetworks.length" :label="tt('create.networks')">
          <div class="check-grid">
            <label v-for="n in attachableNetworks" :key="n.fullName || n.name" class="check" :class="{ locked: isNetworkLocked(n) }">
              <input
                v-model="form.networks"
                type="checkbox"
                :value="n.fullName || n.name"
                :disabled="isNetworkLocked(n)"
              />
              {{ n.name }}
              <span v-if="originLabel(n)" class="hint">({{ originLabel(n) }})</span>
              <span v-if="n.forward" class="hint">· {{ tt("create.netHintForward") }}</span>
              <span v-if="isNetworkLocked(n)" class="hint">· {{ tt("create.networkNotInPool") }}</span>
            </label>
          </div>
          <p v-if="forwardNets.length" class="hint">{{ tt("create.forwardHint", { nets: forwardNets.join(", ") }) }}</p>
        </el-form-item>
        <el-form-item :label="tt('create.restartPolicy')">
          <el-select v-model="form.restartPolicy" style="width: 100%">
            <el-option value="unless-stopped" :label="tt('create.policyUnlessStopped')" />
            <el-option value="always" :label="tt('create.policyAlways')" />
            <el-option value="on-failure" :label="tt('create.policyOnFailure')" />
            <el-option value="no" :label="tt('create.policyNo')" />
          </el-select>
        </el-form-item>
        <div class="check-grid">
          <label class="check"><input v-model="form.forward8080" type="checkbox"> {{ tt("create.forward8080") }}</label>
          <label class="check"><input v-model="form.forward8090" type="checkbox"> {{ tt("create.forward8090") }}</label>
          <label class="check"><input v-model="form.mountNetdisk" type="checkbox"> {{ tt("create.mountNetdisk") }}</label>
          <label class="check"><input v-model="form.mountShm" type="checkbox"> {{ tt("create.mountShm") }}</label>
        </div>
        <!-- Offered once the caller's group actually has a shared-disk root; the
             caller's own read-only/read-write access is their persistent
             shared-disk setting, not chosen here. -->
        <div v-if="s.me?.sharedDiskConfigured" class="shared-disk-section">
          <label class="check"><input v-model="form.mountSharedDisk" type="checkbox"> {{ tt("create.mountSharedDisk") }}</label>
          <p class="hint">{{ tt("create.sharedDiskAccessHint") }}</p>
        </div>
        <!-- Collapsible advanced block. Empty fields inherit the image defaults
             (the backend treats them as "unset"). -->
        <el-collapse class="advanced-block">
          <el-collapse-item :title="tt('create.advanced')">
            <el-input v-model="form.command" :placeholder="tt('create.commandPlaceholder')" class="adv-input" />
            <el-input v-model="form.entrypoint" :placeholder="tt('create.entrypointPlaceholder')" class="adv-input" />
            <el-input v-model="form.workingDir" :placeholder="tt('create.workingDirPlaceholder')" class="adv-input" />
            <el-input v-model="form.hostname" :placeholder="tt('create.hostnamePlaceholder')" class="adv-input" />
            <el-input v-model="form.runUser" :placeholder="tt('create.runUserPlaceholder')" class="adv-input" />
            <div class="advanced-row">
              <el-input v-model="form.cpuLimit" type="number" min="0" step="0.5" :placeholder="tt('create.cpuPlaceholder')" />
              <el-input v-model="form.memoryMb" type="number" min="0" :placeholder="tt('create.memoryPlaceholder')" />
              <el-input v-model="form.pidsLimit" type="number" min="0" :placeholder="tt('create.pidsPlaceholder')" />
            </div>
            <!-- cap-add grants host-wide privileges (admins only); cap-drop only
                 removes privileges and stays available to all users. -->
            <el-input v-if="isAdmin()" v-model="form.capAdd" :placeholder="tt('create.capAddPlaceholder')" class="adv-input" />
            <el-input v-model="form.capDrop" :placeholder="tt('create.capDropPlaceholder')" class="adv-input" />
            <el-input v-model="form.labels" type="textarea" :rows="2" :placeholder="tt('create.labelsPlaceholder')" />
          </el-collapse-item>
        </el-collapse>
      </el-form>
    </template>
    <template v-else>
      <ol class="steps">
        <li v-for="st in progress.steps" :key="st.stage" class="step" :class="st.state">
          <span class="step-icon">
            <el-icon v-if="st.state === 'active'" class="is-loading"><Loading /></el-icon>
            <template v-else-if="st.state === 'done'">✓</template>
            <template v-else-if="st.state === 'error'">✗</template>
            <template v-else>○</template>
          </span>
          <span class="step-label">{{ stageLabel(st.stage) }}</span>
          <span class="step-msg">{{ st.message || "" }}</span>
        </li>
      </ol>
      <div v-if="progress.error" class="error-box">✗ {{ progress.error }}</div>
      <pre ref="log" class="log-output create-log">{{ progress.logs }}</pre>
    </template>
    <template #footer>
      <template v-if="!progress.active">
        <el-button @click="$emit('update:visible', false)">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" :loading="submitting" @click="submit">{{ tt("create.createAndStart") }}</el-button>
      </template>
      <template v-else>
        <el-button v-if="progress.error" type="primary" @click="submit">{{ tt("common.retry") }}</el-button>
        <el-button @click="$emit('update:visible', false)">{{ progress.error ? tt("common.close") : tt("common.hide") }}</el-button>
      </template>
    </template>
  </el-dialog>
</template>

<script>
import { ElMessage } from "element-plus";
import { api, readCSRFCookie, readSSE } from "@/api";
import { store, refreshAll, isAdmin } from "@/store";
import { tt } from "@/i18n";
import { registerJob } from "@/jobs";

const STAGE_ORDER = ["image", "create", "start", "refresh", "done"];

function blankForm() {
  return {
    name: "",
    image: "",
    gpus: "none",
    env: "",
    ports: "",
    mounts: "",
    networks: [],
    restartPolicy: "unless-stopped",
    forward8080: false,
    forward8090: false,
    mountNetdisk: true,
    mountShm: true,
    mountSharedDisk: false,
    command: "",
    entrypoint: "",
    workingDir: "",
    hostname: "",
    runUser: "",
    cpuLimit: "",
    memoryMb: "",
    pidsLimit: "",
    capAdd: "",
    capDrop: "",
    labels: "",
  };
}

// csvList splits a comma/whitespace-separated string into trimmed tokens.
function csvList(raw) {
  return String(raw || "").split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
}

// kvLines parses "key=value" lines into an object, skipping invalid lines.
function kvLines(raw) {
  const out = {};
  for (const line of String(raw || "").split("\n")) {
    const idx = line.indexOf("=");
    if (idx <= 0) continue;
    const k = line.slice(0, idx).trim();
    const v = line.slice(idx + 1).trim();
    if (k) out[k] = v;
  }
  return Object.keys(out).length ? out : undefined;
}

export default {
  name: "CreateDialog",
  props: {
    visible: { type: Boolean, default: false },
  },
  data() {
    return {
      s: store,
      form: blankForm(),
      progress: { active: false, steps: [], logs: "", error: "" },
      submitting: false,
      // lastPayload backs the Retry button: resubmit the original request
      // instead of wiping the form.
      lastPayload: null,
    };
  },
  computed: {
    images() { return store.images || []; },
    // Only networks the server marked attachable: the user's own, group-shared
    // grants, and "bridge" unless restricted. host/none can't be joined, so
    // surfacing them here would only produce "attach failed" errors.
    attachableNetworks() { return (store.networks || []).filter((n) => n.attachable); },
    forwardNets() { return (store.networks || []).filter((n) => n.forward && n.attachable).map((n) => n.name); },
    gpuIndexes() {
      const count = Number(store.gpuCount || 0);
      if (count > 0) return Array.from({ length: count }, (_, i) => i);
      // Unknown count (non-GPU host or probe still in flight): keep the legacy
      // 0/1 fallback so users aren't blocked on a slow GPU probe.
      return [0, 1];
    },
    portPlaceholder() {
      const prefix = Number(store.me?.portPrefix || 0);
      const hint = prefix > 0 ? tt("create.portHintAssigned", { lo: prefix * 100, hi: prefix * 100 + 99 }) : tt("create.portHintAsk");
      return `${tt("create.portsPlaceholder")}\n${hint}`;
    },
    mountsPlaceholder() {
      const vols = (store.volumes || []).map((v) => v.name);
      return tt("create.mountsPlaceholder") + (vols.length ? "\n" + tt("create.mountsAvail") + vols.join(", ") : "");
    },
    networkPool() {
      const img = this.images.find((i) => i.name === this.form.image);
      const p = img && img.preset;
      return p && p.selectableNetworks && p.selectableNetworks.length ? new Set(p.selectableNetworks) : null;
    },
  },
  watch: {
    visible(v) {
      if (v) {
        this.form = blankForm();
        this.progress = { active: false, steps: [], logs: "", error: "" };
      }
    },
    "progress.logs"() {
      this.$nextTick(() => {
        if (this.$refs.log) this.$refs.log.scrollTop = this.$refs.log.scrollHeight;
      });
    },
  },
  methods: {
    tt,
    isAdmin,
    hasPreset(img) {
      const p = img.preset;
      return p && (p.gpus || (p.ports && p.ports.length) || p.description);
    },
    originLabel(n) {
      return n.system ? tt("create.netHintSystem") : n.external ? tt("create.netHintHost") : n.shared ? tt("create.netHintShared") : "";
    },
    isNetworkLocked(n) {
      const pool = this.networkPool;
      return !!pool && !pool.has(n.fullName || n.name);
    },
    stageLabel(stage) {
      return tt("create.stage" + stage.charAt(0).toUpperCase() + stage.slice(1));
    },
    markStage(stage, message, st) {
      let existing = this.progress.steps.find((s) => s.stage === stage);
      if (!existing) this.progress.steps.push({ stage, message, state: st });
      else {
        existing.message = message;
        existing.state = st;
      }
      this.progress.steps.sort((a, b) => {
        const ai = STAGE_ORDER.indexOf(a.stage);
        const bi = STAGE_ORDER.indexOf(b.stage);
        return (ai < 0 ? 99 : ai) - (bi < 0 ? 99 : bi);
      });
    },
    // Fills the form from the selected image's admin-defined preset so the
    // per-image conventions (ports, env, networks) apply automatically; the
    // user can still override anything. Env placeholders are resolved through
    // the server so {{random_password}}/{{sequence}} expand to fresh values.
    async applyPreset(imageName) {
      const img = this.images.find((i) => i.name === imageName);
      // Switching to an image without a preset must also unwind the previous
      // preset's pre-checked networks, or the create payload carries them over.
      if (!img || !img.preset) {
        this.form.networks = [];
        return;
      }
      const p = img.preset;
      if (p.gpus) this.form.gpus = p.gpus;
      // Preset ports are container-side only; render as ":container" so the
      // backend auto-allocates a host port from the user's range.
      if (p.ports && p.ports.length) this.form.ports = p.ports.map((c) => ":" + c).join("\n");
      if (p.restartPolicy) this.form.restartPolicy = p.restartPolicy;
      this.form.forward8080 = !!p.forward8080;
      this.form.forward8090 = !!p.forward8090;
      if (p.mountNetdisk !== undefined) this.form.mountNetdisk = p.mountNetdisk;
      if (p.mountShm !== undefined) this.form.mountShm = p.mountShm;
      if (p.mountSharedDisk !== undefined) this.form.mountSharedDisk = p.mountSharedDisk;
      // Default networks are pre-checked only where they survive the pool filter.
      const pool = this.networkPool;
      this.form.networks = (p.networks || []).filter((key) => !pool || pool.has(key));
      if (p.env && p.env.length) {
        this.form.env = p.env.join("\n");
        try {
          const resolved = await api("/api/images/preset/resolve", {
            method: "POST",
            body: JSON.stringify({ imageId: Number(img.id) }),
          });
          // Bail if the user switched images while the request was in flight,
          // so a slow response can't clobber a newer selection.
          if (resolved.env && this.form.image === imageName) {
            this.form.env = resolved.env.join("\n");
          }
        } catch (err) {
          ElMessage.error(err.message);
        }
      }
    },
    submit() {
      if (this.progress.active) {
        if (this.lastPayload) this.streamCreate(this.lastPayload);
        return;
      }
      if (!this.form.name.trim() || !this.form.image) {
        ElMessage.warning(tt("create.selectImage"));
        return;
      }
      const f = this.form;
      const payload = {
        name: f.name.trim(),
        image: f.image,
        gpus: f.gpus,
        env: String(f.env || "").split(/\n+/).map((s) => s.trim()).filter(Boolean),
        ports: f.ports,
        mounts: f.mounts,
        networks: [...f.networks],
        restartPolicy: f.restartPolicy || "unless-stopped",
        forward8080: !!f.forward8080,
        forward8090: !!f.forward8090,
        mountNetdisk: !!f.mountNetdisk,
        mountShm: !!f.mountShm,
        mountSharedDisk: !!f.mountSharedDisk,
        // Advanced overrides (all optional; backend ignores empties/zeros).
        command: (f.command || "").trim(),
        entrypoint: (f.entrypoint || "").trim(),
        workingDir: (f.workingDir || "").trim(),
        hostname: (f.hostname || "").trim(),
        runUser: (f.runUser || "").trim(),
        // CPU cores → nanocpus (1 core = 1e9). 0/empty means unlimited.
        nanoCpus: (() => {
          const cores = parseFloat(f.cpuLimit);
          return isFinite(cores) && cores > 0 ? Math.round(cores * 1e9) : 0;
        })(),
        memoryMb: parseInt(f.memoryMb, 10) || 0,
        pidsLimit: parseInt(f.pidsLimit, 10) || 0,
        capAdd: csvList(f.capAdd),
        capDrop: csvList(f.capDrop),
        labels: kvLines(f.labels),
      };
      this.lastPayload = payload;
      this.streamCreate(payload);
    },
    async streamCreate(payload) {
      this.progress = { active: true, steps: [], logs: "", error: "" };
      const job = registerJob({ kind: "container.create", name: payload.name || "new container" });
      try {
        const headers = { "Content-Type": "application/json", Accept: "text/event-stream" };
        const csrfToken = readCSRFCookie() || store.csrfToken || "";
        if (csrfToken) headers["X-CSRF-Token"] = csrfToken;
        const res = await fetch("/api/containers/create/stream", {
          method: "POST",
          credentials: "same-origin",
          headers,
          body: JSON.stringify(payload),
          signal: job.signal,
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
            const stage = data.stage || "info";
            const message = data.message || "";
            this.progress.steps.forEach((s) => {
              if (s.state === "active") s.state = stage === "done" ? "done" : "done";
            });
            this.markStage(stage, message, stage === "done" ? "done" : "active");
            this.progress.logs += `[${stage}] ${message}\n`;
            job.log(message);
          } else if (event === "error") {
            this.progress.steps.forEach((s) => {
              if (s.state === "active") s.state = "error";
            });
            this.progress.error = data.message || tt("create.creationFailed");
            this.progress.logs += `[error] ${this.progress.error}\n`;
            job.error(this.progress.error);
            ElMessage.error(this.progress.error);
          } else if (event === "done") {
            this.progress.active = false;
            job.done(data.message || tt("create.created"));
            ElMessage.success(tt("create.created"));
            refreshAll();
            setTimeout(() => this.$emit("update:visible", false), 700);
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
  },
};
</script>

<style scoped>
.check-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 6px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.check.locked { opacity: 0.5; }
.shared-disk-section { margin-top: 8px; }
.advanced-block { margin-top: 10px; border-top: 1px dashed var(--line); }
.adv-input { margin-bottom: 8px; }
.advanced-row { display: flex; gap: 8px; margin-bottom: 8px; }
.steps { list-style: none; margin: 0 0 10px; padding: 0; }
.step { display: flex; gap: 10px; align-items: baseline; padding: 4px 0; font-size: 13px; }
.step.done .step-icon { color: var(--ok); }
.step.error .step-icon { color: var(--danger); }
.step-msg { color: var(--muted); }
.error-box { background: #fef2f2; color: #b91c1c; border: 1px solid #fecaca; border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; }
.create-log { max-height: 220px; }
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
</style>
