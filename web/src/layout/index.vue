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
        <template v-for="group in menuGroups" :key="group.key">
          <div v-show="!collapsed" class="nav-group-label">{{ tt("nav." + group.key) }}</div>
          <button
            v-for="item in group.items"
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
        </template>
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
          <el-button class="icon-btn" :title="themeTitle" @click="toggleTheme">
            <v-icon :name="s.isDark ? 'sun' : 'moon'" :size="16" />
          </el-button>
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
        <router-view v-slot="{ Component }">
          <transition name="page" mode="out-in">
            <component :is="Component" :key="$route.fullPath" />
          </transition>
        </router-view>
      </div>
    </section>

    <el-drawer v-model="drawer" direction="ltr" size="280px" :with-header="false" class="mobile-nav-drawer">
      <div class="drawer-shell">
        <div class="drawer-head">
          <span class="drawer-avatar" aria-hidden="true">{{ userInitial }}</span>
          <div class="drawer-user">
            <strong :title="userName">{{ userName }}</strong>
            <span>{{ roleLine }}</span>
          </div>
        </div>
        <nav class="drawer-nav">
          <template v-for="group in menuGroups" :key="group.key">
            <div class="nav-group-label">{{ tt("nav." + group.key) }}</div>
            <button
              v-for="item in group.items"
              :key="item.name"
              class="nav-item"
              :class="{ active: $route.name === item.name }"
              :data-tab="item.key"
              @click="navigate(item)"
            >
              <span class="ico"><v-icon :name="item.key" /></span>
              <span class="nav-label">{{ tt("nav." + item.name) }}</span>
            </button>
          </template>
        </nav>
        <div class="drawer-foot">
          <button class="drawer-logout" :title="tt('user.logout')" @click="logout">
            <v-icon name="logout" /><span>{{ tt("user.logout") }}</span>
          </button>
        </div>
      </div>
    </el-drawer>

    <jobs-panel v-model:visible="jobsVisible" />
    <notifications-panel v-model:visible="bellVisible" />
  </section>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { store, refreshAll, isAdmin, setTheme } from "@/store";
import { tt, errText } from "@/i18n";
import { activeJobCount } from "@/jobs";
import { refreshActiveRoute } from "@/refresh";
import VIcon from "@/components/VIcon.vue";
import JobsPanel from "@/layout/JobsPanel.vue";
import NotificationsPanel from "@/layout/NotificationsPanel.vue";

// Tab order from the old shell, keyed by nav id; the route name is the same
// and matches its i18n key (nav.<key> / subtitle.<key>).
// Sidebar sections: everyday workspace, shared resources, then system-level
// pages. Purely presentational — keys drive the small group captions.
const MENU_GROUPS = ["groupWorkspace", "groupResources", "groupSystem"];
const MENU = [
  { key: "dashboard", name: "dashboard", group: "groupWorkspace" },
  { key: "netdisk", name: "netdisk", group: "groupWorkspace" },
  { key: "containers", name: "containers", group: "groupWorkspace" },
  { key: "mcp", name: "mcp", group: "groupWorkspace" },
  { key: "processes", name: "processes", group: "groupWorkspace" },
  { key: "usage", name: "usage", group: "groupWorkspace" },
  { key: "images", name: "images", group: "groupResources" },
  { key: "volumes", name: "volumes", group: "groupResources" },
  { key: "networks", name: "networks", group: "groupResources" },
  { key: "forwards", name: "forwards", admin: true, group: "groupResources" },
  { key: "stacks", name: "stacks", group: "groupResources" },
  { key: "hardware", name: "hardware", group: "groupResources" },
  { key: "users", name: "users", admin: true, group: "groupSystem" },
  { key: "audit", name: "audit", admin: true, group: "groupSystem" },
  { key: "security", name: "security", admin: true, group: "groupSystem" },
  { key: "errors", name: "errors", admin: true, group: "groupSystem" },
  { key: "disks", name: "disks", admin: true, group: "groupSystem" },
  { key: "database", name: "database", admin: true, group: "groupSystem" },
  { key: "settings", name: "settings", group: "groupSystem" },
  { key: "help", name: "help", group: "groupSystem" },
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
    menuGroups() {
      return MENU_GROUPS.map((group) => ({
        key: group,
        items: this.menuItems.filter((item) => item.group === group),
      })).filter((g) => g.items.length);
    },
    userName() {
      return this.s.me?.displayName || this.s.me?.username || "";
    },
    userInitial() {
      const name = this.userName.trim();
      return name ? name[0].toUpperCase() : "?";
    },
    roleLine() {
      const me = this.s.me;
      return me?.role + (me?.group ? " - " + me.group : "");
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
    themeTitle() {
      return tt("shell.themeToggle", { next: tt("theme." + (this.s.isDark ? "light" : "dark")) });
    },
  },
  methods: {
    tt,
    toggleTheme() {
      setTheme(this.s.isDark ? "light" : "dark");
    },
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
        ElMessage.error(errText(err));
      }
    },
  },
};
</script>
