<template>
  <!-- Baidu-style share creation: expiry + extraction code, then the ready
       link step with copy chips. -->
  <el-dialog :model-value="visible" :title="step === 'form' ? (paths.length === 1 ? tt('netdisk.shareFile') : tt('netdisk.shareItems')) : tt('netdisk.shareLinkReady')" width="500px" append-to-body @update:model-value="onVisible">
    <p class="hint share-name-line" :title="name">{{ name }}</p>

    <template v-if="step === 'form'">
      <div class="share-row">
        <div class="share-row-label">{{ tt("netdisk.validFor") }}</div>
        <el-radio-group v-model="expiry" size="small">
          <el-radio-button :value="1">{{ tt("netdisk.1day") }}</el-radio-button>
          <el-radio-button :value="7">{{ tt("netdisk.7days") }}</el-radio-button>
          <el-radio-button :value="30">{{ tt("netdisk.30days") }}</el-radio-button>
          <el-radio-button :value="0">{{ tt("netdisk.permanent") }}</el-radio-button>
        </el-radio-group>
      </div>
      <div class="share-row">
        <div class="share-row-label">{{ tt("netdisk.extractionCodeLabel") }}</div>
        <label class="check"><input v-model="usePassword" type="checkbox"> {{ tt("netdisk.protectWithCode") }}</label>
        <div v-if="usePassword" class="share-code-field">
          <el-input v-model="code" maxlength="8" size="small" style="width: 140px" />
          <el-button size="small" @click="code = randomCode(4)">{{ tt("netdisk.random") }}</el-button>
        </div>
      </div>
    </template>

    <template v-else>
      <div class="share-link-row">
        <div class="share-row-label">{{ tt("netdisk.linkLabel") }}</div>
        <div class="copy-chip">
          <code>{{ link }}</code>
          <el-button size="small" @click="copy(link)">{{ tt("common.copy") }}</el-button>
        </div>
      </div>
      <div v-if="usePassword" class="share-link-row">
        <div class="share-row-label">{{ tt("netdisk.codeLabel") }}</div>
        <div class="copy-chip">
          <code>{{ code }}</code>
          <el-button size="small" @click="copy(code)">{{ tt("common.copy") }}</el-button>
        </div>
      </div>
      <el-button link size="small" @click="copyAll">{{ tt("netdisk.copyAll", { code: usePassword ? ` & ${tt("netdisk.codeLabel")}` : "" }) }}</el-button>
    </template>

    <template #footer>
      <template v-if="step === 'form'">
        <el-button @click="$emit('update:visible', false)">{{ tt("common.cancel") }}</el-button>
        <el-button type="primary" :loading="creating" @click="submit">{{ creating ? tt("netdisk.creating2") : tt("netdisk.createLink") }}</el-button>
      </template>
      <template v-else>
        <el-button @click="close">{{ tt("common.close") }}</el-button>
        <el-button type="primary" @click="close">{{ tt("common.done") }}</el-button>
      </template>
    </template>
  </el-dialog>
</template>

<script>
import { ElMessage } from "element-plus";
import { api, copyText } from "@/api";
import { tt } from "@/i18n";

// no I, O, 0, 1 — legibility.
function randomCode(len = 4) {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  let out = "";
  for (let i = 0; i < len; i++) out += chars[Math.floor(Math.random() * chars.length)];
  return out;
}

export default {
  name: "ShareDialog",
  props: {
    visible: { type: Boolean, default: false },
    paths: { type: Array, default: () => [] },
    name: { type: String, default: "" },
  },
  data() {
    return {
      step: "form",
      expiry: 7, // days; 0 = permanent
      usePassword: true,
      code: randomCode(4),
      creating: false,
      created: null,
    };
  },
  computed: {
    link() {
      return this.created ? `${location.origin}/pan/${encodeURIComponent(this.created.token)}` : "";
    },
  },
  watch: {
    visible(v) {
      if (v) {
        this.step = "form";
        this.expiry = 7;
        this.usePassword = true;
        this.code = randomCode(4);
        this.creating = false;
        this.created = null;
      }
    },
  },
  methods: {
    tt,
    randomCode,
    onVisible(v) {
      this.$emit("update:visible", v);
    },
    async copy(text) {
      try {
        await copyText(text);
        ElMessage.success(tt("common.copied"));
      } catch {
        ElMessage.error(tt("common.copyFailed"));
      }
    },
    async copyAll() {
      const text = this.usePassword ? `${this.link}\n${tt("netdisk.extractionCode")}: ${this.code}` : this.link;
      await this.copy(text);
    },
    async submit() {
      if (this.creating) return;
      this.creating = true;
      const body = { paths: this.paths, name: this.name };
      if (this.expiry > 0) body.expiresDays = this.expiry;
      else body.permanent = true;
      if (this.usePassword && this.code.trim()) body.password = this.code.trim();
      try {
        this.created = await api("/api/netdisk/share", { method: "POST", body: JSON.stringify(body) });
        this.step = "link";
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.creating = false;
      }
    },
    close() {
      this.$emit("update:visible", false);
      this.$emit("done");
    },
  },
};
</script>

<style scoped>
.share-row { margin-bottom: 16px; }
.share-row-label { font-size: 13px; font-weight: 600; margin-bottom: 8px; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; margin-bottom: 8px; }
.share-code-field { display: flex; gap: 8px; align-items: center; }
.share-link-row { margin-bottom: 12px; }
.copy-chip { display: flex; align-items: center; gap: 10px; background: #f1f5f9; border: 1px solid var(--line); border-radius: 8px; padding: 6px 10px; }
.copy-chip code { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.share-name-line { margin-top: -8px; }
</style>
