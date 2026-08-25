<template>
  <section class="shell" :class="{ 'sidebar-collapsed': collapsed }">
    <aside class="shell-aside" :class="{ collapsed }">
      <div class="brand">
        <span class="dot"></span>
        <span v-show="!collapsed" class="brand-text">{{ siteName || "MUDP" }}</span>
        <button class="sidebar-toggle" :title="collapsed ? tt('shell.expandSidebar') : tt('shell.collapseSidebar')" aria-label="Toggle sidebar" @click="toggleCollapse">
          <v-icon :name="collapsed ? 'expand' : 'collapse'" :size="16" />
        </button>
      </div>
      <nav class="shell-nav">
        <button
          v-for="item in menuItems"
          :key="item.name"
          class="nav-item"
          :class="{ active: $route.name === item.name }"
          :data-tab="item.key"
          :title="tt('nav.' + item.name)"
          @click="navigate(item)"
        >
          <span class="ico"><v-icon :name="item.key" /></span>
          <span v-show="!collapsed" class="nav-label">{{ tt("nav." + item.name) }}</span>
        </button>
      </nav>
      <div class="profile">
        <strong :title="userName">{{ userName }}</strong>
        <span>{{ roleLine }}</span>
        <button :title="tt('user.logout')" @click="logout">
          <v-icon name="logout" /><span v-show="!collapsed" class="nav-label">{{ tt("user.logout") }}</span>
        </button>
      </div>
    </aside>

    <section class="work">
      <header class="work-header">
        <button class="mobile-nav-toggle" :aria-label="tt('nav.toggleMenu')" @click="drawer = true">
          <v-icon name="menu" />
        </button>
        <div class="titles">
          <h1>{{ pageTitle }}</h1>
          <p>{{ pageSubtitle }}</p>
        </div>
        <div class="head-actions">
          <el-badge :value="jobsCount" :hidden="!jobsCount" class="head-badge">
            <el-button class="icon-btn" :title="jobsTitle" @click="jobsVisible = true">
              <v-icon name="jobs" />
            </el-button>
          </el-badge>
          <el-badge :value="bellCount" :hidden="!bellCount" class="head-badge">
            <el-button class="icon-btn" :title="tt('notif.title')" @click="bellVisible = true">
              <v-icon name="bell" />
            </el-button>
          </el-badge>
          <el-button class="ghost-btn" @click="refresh">{{ tt("action.refresh") }}</el-button>
        </div>
      </header>
      <div class="app-main">
        <router-view :key="$route.fullPath" />
      </div>
    </section>

    <el-drawer v-model="drawer" direction="ltr" size="220px" :with-header="false" class="mobile-nav-drawer">
      <div class="drawer-nav">
        <button
          v-for="item in menuItems"
          :key="item.name"
          class="nav-item"
          :class="{ active: $route.name === item.name }"
          :data-tab="item.key"
          @click="navigate(item)"
        >
          <span class="ico"><v-icon :name="item.key" /></span>
          <span class="nav-label">{{ tt("nav." + item.name) }}</span>
        </button>
      </div>
    </el-drawer>

    <jobs-panel v-model:visible="jobsVisible" />
    <notifications-panel v-model:visible="bellVisible" />
  </section>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { store, refreshAll, isAdmin } from "@/store";
import { tt } from "@/i18n";
import { activeJobCount } from "@/jobs";
import { refreshActiveRoute } from "@/refresh";
import VIcon from "@/components/VIcon.vue";
import JobsPanel from "@/layout/JobsPanel.vue";
import NotificationsPanel from "@/layout/NotificationsPanel.vue";

// Tab order from the old shell, keyed by nav id; the route name is the same
// and matches its i18n key (nav.<key> / subtitle.<key>).
const MENU = [
  { key: "dashboard", name: "dashboard" },
  { key: "netdisk", name: "netdisk" },
  { key: "containers", name: "containers" },
  { key: "mcp", name: "mcp" },
  { key: "processes", name: "processes" },
  { key: "usage", name: "usage" },
  { key: "images", name: "images" },
  { key: "volumes", name: "volumes" },
  { key: "networks", name: "networks" },
  { key: "forwards", name: "forwards", admin: true },
  { key: "stacks", name: "stacks" },
  { key: "hardware", name: "hardware" },
  { key: "users", name: "users", admin: true },
  { key: "audit", name: "audit", admin: true },
  { key: "security", name: "security", admin: true },
  { key: "errors", name: "errors", admin: true },
  { key: "disks", name: "disks", admin: true },
  { key: "database", name: "database", admin: true },
  { key: "settings", name: "settings" },
  { key: "help", name: "help" },
];

export default {
  name: "Layout",
  components: { VIcon, JobsPanel, NotificationsPanel },
  data() {
    return {
      s: store,
      drawer: false,
      jobsVisible: false,
      bellVisible: false,
    };
  },
  computed: {
    collapsed() {
      return this.s.sidebarCollapsed;
    },
    siteName() {
      return this.s.siteName;
    },
    menuItems() {
      const admin = isAdmin();
      return MENU.filter((item) => admin || !item.admin);
    },
    userName() {
      return this.s.me?.displayName || this.s.me?.username || "";
    },
    roleLine() {
      const me = this.s.me;
      return me?.role + (me?.groups?.length ? " - " + me.groups.join(", ") : "");
    },
    pageTitle() {
      return tt("nav." + (this.$route.name || ""));
    },
    pageSubtitle() {
      return tt("subtitle." + (this.$route.name || ""));
    },
    jobsCount() {
      const n = activeJobCount();
      return n > 99 ? "99+" : n;
    },
    jobsTitle() {
      const n = activeJobCount();
      return n > 0 ? tt("jobs.nJobs", { n }) : tt("jobs.title");
    },
    bellCount() {
      const n = this.s.unreadCount || 0;
      return n > 99 ? "99+" : n;
    },
  },
  methods: {
    tt,
    navigate(item) {
      this.drawer = false;
      if (this.$route.name !== item.name) {
        this.$router.push({ name: item.name });
        // Land on fresh data instead of up to one poll-interval of stale view.
        this.$nextTick(refreshActiveRoute);
      }
    },
    toggleCollapse() {
      store.sidebarCollapsed = !store.sidebarCollapsed;
      localStorage.setItem("mudp:sidebar", store.sidebarCollapsed ? "collapsed" : "expanded");
    },
    async logout() {
      await api("/api/logout", { method: "POST" }).catch(() => {});
      store.me = null;
      this.$router.push("/login");
    },
    async refresh() {
      try {
        await refreshAll();
        ElMessage.success(tt("toast.refreshed"));
      } catch (err) {
        ElMessage.error(err.message);
      }
    },
  },
};
</script>
