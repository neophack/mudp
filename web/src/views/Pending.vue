<template>
  <section class="pending-wrap">
    <div class="pending-card card">
      <div class="pending-icon"></div>
      <h1>{{ tt("pending.title") }}</h1>
      <p>
        {{ tt("pending.greeting", { name: name }) }}<br />
        {{ tt("pending.hint") }}
      </p>
      <el-button class="ghost-btn" @click="logout">{{ tt("user.logout") }}</el-button>
    </div>
  </section>
</template>

<script>
import { api } from "@/api";
import { store, displayName } from "@/store";
import { tt } from "@/i18n";

export default {
  name: "Pending",
  computed: {
    name() {
      return displayName(store.me);
    },
  },
  methods: {
    tt,
    async logout() {
      await api("/api/logout", { method: "POST" }).catch(() => {});
      store.me = null;
      this.$router.push("/login");
    },
  },
};
</script>
