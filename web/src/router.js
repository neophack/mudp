import { createRouter, createWebHistory } from "vue-router";
import { store, isAdmin } from "@/store";
import { boot } from "@/boot";

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: "/login", name: "login", component: () => import("@/views/Login.vue"), meta: { public: true, key: "login" } },
    { path: "/setup", name: "setup", component: () => import("@/views/Setup.vue"), meta: { public: true, key: "setup" } },
    { path: "/pending", name: "pending", component: () => import("@/views/Pending.vue"), meta: { public: true, key: "pending" } },
    {
      path: "/",
      component: () => import("@/layout/index.vue"),
      children: [
        { path: "", redirect: { name: "dashboard" } },
        { path: "dashboard", name: "dashboard", component: () => import("@/views/Dashboard.vue"), meta: { key: "dashboard" } },
        { path: "netdisk", name: "netdisk", component: () => import("@/views/Netdisk.vue"), meta: { key: "netdisk" } },
        { path: "containers", name: "containers", component: () => import("@/views/Containers.vue"), meta: { key: "containers" } },
        { path: "mcp", name: "mcp", component: () => import("@/views/Mcp.vue"), meta: { key: "mcp" } },
        { path: "processes", name: "processes", component: () => import("@/views/Processes.vue"), meta: { key: "processes" } },
        { path: "usage", name: "usage", component: () => import("@/views/Usage.vue"), meta: { key: "usage" } },
        { path: "images", name: "images", component: () => import("@/views/Images.vue"), meta: { key: "images" } },
        { path: "volumes", name: "volumes", component: () => import("@/views/Volumes.vue"), meta: { key: "volumes" } },
        { path: "networks", name: "networks", component: () => import("@/views/Networks.vue"), meta: { key: "networks" } },
        { path: "forwards", name: "forwards", component: () => import("@/views/Forwards.vue"), meta: { key: "forwards", admin: true } },
        { path: "stacks", name: "stacks", component: () => import("@/views/Stacks.vue"), meta: { key: "stacks" } },
        { path: "hardware", name: "hardware", component: () => import("@/views/Hardware.vue"), meta: { key: "hardware" } },
        { path: "users", name: "users", component: () => import("@/views/Users.vue"), meta: { key: "users", admin: true } },
        { path: "audit", name: "audit", component: () => import("@/views/Audit.vue"), meta: { key: "audit", admin: true } },
        { path: "security", name: "security", component: () => import("@/views/Security.vue"), meta: { key: "security", admin: true } },
        { path: "errors", name: "errors", component: () => import("@/views/Errors.vue"), meta: { key: "errors", admin: true } },
        { path: "disks", name: "disks", component: () => import("@/views/Disks.vue"), meta: { key: "disks", admin: true } },
        { path: "database", name: "database", component: () => import("@/views/Database.vue"), meta: { key: "database", admin: true } },
        { path: "settings", name: "settings", component: () => import("@/views/Settings.vue"), meta: { key: "settings" } },
        { path: "help", name: "help", component: () => import("@/views/Help.vue"), meta: { key: "help" } },
      ],
    },
    { path: "/:pathMatch(.*)*", redirect: { name: "dashboard" } },
  ],
});

let bootPromise = null;

router.beforeEach(async (to, from, next) => {
  if (!store.booted) {
    if (!bootPromise) bootPromise = boot();
    const dest = await bootPromise;
    store.booted = true;
    if (dest === "setup") return next("/setup");
    if (dest === "login") return next("/login");
    if (dest === "pending") return next("/pending");
  }
  // Reflect the current route name into the store for the refresh loop and
  // page-level toolbars.
  store.route = to.name;
  if (to.meta.public) {
    if (store.setupNeeded && to.name !== "setup") return next("/setup");
    if (!store.setupNeeded && to.name === "setup") return next(store.me ? "/dashboard" : "/login");
    if (store.me && to.name === "login") return next("/dashboard");
    if (store.me?.pending && to.name !== "pending") return next("/pending");
    if (!store.me && to.name === "pending") return next("/login");
  } else {
    if (store.setupNeeded) return next("/setup");
    if (!store.me) return next("/login");
    if (store.me.pending) return next("/pending");
    if (to.meta.admin && !isAdmin()) return next("/dashboard");
  }
  next();
});
