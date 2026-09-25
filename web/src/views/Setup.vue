<template>
  <section class="auth-wrap">
    <!-- Brand hero: macOS-style aurora panel on wide screens, stacked on top
         when narrow. The blob layer is pure decoration behind the glass. -->
    <div class="auth-hero">
      <div class="hero-bg" aria-hidden="true"><span class="g1"></span><span class="g2"></span><span class="g3"></span><span class="g4"></span></div>
      <div class="login-brand">
        <div class="app-icon" aria-hidden="true">
          <img src="/mudp.png" alt="" draggable="false" />
        </div>
        <div class="app-name">MUDP</div>
        <div class="app-tagline">{{ tt("setup.welcome") }}</div>
        <div class="hero-feats">
          <span v-for="i in 4" :key="i">{{ tt("login.feature" + i) }}</span>
        </div>
      </div>
    </div>
    <div class="auth-pane">
      <form class="auth-card" @submit.prevent="submit">
        <h1>{{ tt("setup.title") }}</h1>
        <div class="login-lang">
          <button
            v-for="l in langs"
            :key="l"
            type="button"
            class="lang-btn"
            :class="{ active: l === currentLang }"
            @click="switchLang(l)"
          >{{ l === "zh_CN" ? "中文" : "English" }}</button>
        </div>
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
import { store } from "@/store";
import { tt, setLanguage, errText } from "@/i18n";
import { getCurrentLanguage, SUPPORTED_LANGS } from "@/lib/i18n.js";

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
    currentLang: getCurrentLanguage(),
    langs: SUPPORTED_LANGS,
    };
  },
  methods: {
    tt,
    switchLang(lang) {
      localStorage.setItem("mudp_language", lang);
      setLanguage(lang);
      this.currentLang = getCurrentLanguage();
    },
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
        // Clear the flag the route guard reads: leaving it set bounces the
        // push("/login") below straight back to /setup, stranding the operator
        // on the wizard until a manual reload.
        store.setupNeeded = false;
        ElMessage.success(tt("setup.completeToast"));
        this.$router.push("/login");
      } catch (err) {
        ElMessage.error(errText(err));
      } finally {
        this.busy = false;
      }
    },
  },
};
</script>
