<template>
  <section class="auth-wrap">
    <div class="auth-pane">
      <div class="login-brand">
        <div class="app-icon" aria-hidden="true">
          <svg viewBox="0 0 32 32">
            <path d="M8 12.8c0-1 .8-1.8 1.8-1.8h4.7l1.8 2h6.7c1 0 1.8.8 1.8 1.8v1H8v-3Z" fill="#fff"/>
            <path d="M8 15h16l-1.1 7.4c-.1.9-.9 1.6-1.8 1.6H10.9c-.9 0-1.7-.7-1.8-1.6L8 15Z" fill="#fff"/>
          </svg>
        </div>
        <div class="app-name">MUDP</div>
        <div class="app-tagline">{{ tt("setup.welcome") }}</div>
      </div>
      <form class="auth-card" @submit.prevent="submit">
        <h1>{{ tt("setup.title") }}</h1>
        <label class="field-label">{{ tt("setup.adminUsername") }}</label>
        <el-input v-model="form.adminUsername" name="adminUsername" placeholder="admin" autocomplete="username" />
        <label class="field-label">{{ tt("setup.adminPassword") }}</label>
        <el-input v-model="form.adminPassword" name="adminPassword" type="password" show-password :placeholder="tt('setup.adminPwPlaceholder')" autocomplete="new-password" />
        <label class="field-label">{{ tt("setup.siteName") }} <span class="hint">{{ tt("setup.siteNameOptional") }}</span></label>
        <el-input v-model="form.siteName" name="siteName" :placeholder="tt('setup.siteNamePlaceholder')" />
        <label class="field-label">{{ tt("setup.usersPath") }} <span class="hint">{{ tt("setup.siteNameOptional") }}</span></label>
        <el-input v-model="form.usersGroupNetdiskPath" name="usersGroupNetdiskPath" :placeholder="tt('setup.usersPathPlaceholder')" />
        <p class="hint">{{ tt("setup.usersPathHint") }}</p>
        <el-button type="primary" native-type="submit" :loading="busy" class="auth-submit">{{ tt("setup.complete") }}</el-button>
      </form>
    </div>
  </section>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { tt } from "@/i18n";

export default {
  name: "Setup",
  data() {
    return {
      form: {
        adminUsername: "admin",
        adminPassword: "",
        siteName: "",
        usersGroupNetdiskPath: "",
      },
      busy: false,
    };
  },
  methods: {
    tt,
    async submit() {
      if (this.busy) return;
      // Mirror the old form's required attributes: fail fast client-side
      // instead of round-tripping an empty admin credential to the server.
      if (!this.form.adminUsername.trim() || !this.form.adminPassword) {
        ElMessage.warning(tt("setup.credentialsRequired"));
        return;
      }
      this.busy = true;
      try {
        await api("/api/setup/init", { method: "POST", body: JSON.stringify(this.form) });
        ElMessage.success(tt("setup.completeToast"));
        this.$router.push("/login");
      } catch (err) {
        ElMessage.error(err.message);
      } finally {
        this.busy = false;
      }
    },
  },
};
</script>
