// Internationalization (i18n) - Multi-language support for Chinese and English
// Manages language switching, storage, and translation

// Language constants
export const LANG_CHINESE = "zh_CN";
export const LANG_ENGLISH = "en_US";
export const SUPPORTED_LANGS = [LANG_CHINESE, LANG_ENGLISH];
export const DEFAULT_LANGUAGE = LANG_ENGLISH;

// Translation dictionary
const translations = {
  [LANG_CHINESE]: {
    // Navigation & Menu
    "nav.dashboard": "仪表板",
    "nav.containers": "容器",
    "nav.images": "镜像",
    "nav.volumes": "卷",
    "nav.networks": "网络",
    "nav.forwards": "端口转发",
    "nav.stacks": "堆栈",
    "nav.users": "用户",
    "nav.usage": "使用情况",
    "nav.help": "帮助",
    "nav.audit": "审计",
    "nav.settings": "设置",
    "nav.netdisk": "网盘",
    "nav.disks": "磁盘",
    "nav.hardware": "硬件",
    "nav.mcp": "MCP",
    "nav.usersGroups": "用户与组",
    "nav.activityLog": "活动日志",
    "nav.toggleMenu": "菜单",

    // Shell actions
    "action.searchContainers": "搜索容器",
    "action.newContainer": "+ 新建容器",
    "action.refresh": "刷新",

    // Section subtitles
    "subtitle.dashboard": "环境概览、资源统计与工作区状态一目了然。",
    "subtitle.netdisk": "管理个人文件、批量上传、断点续传和下载。",
    "subtitle.containers": "创建并管理容器，内置网页终端。",
    "subtitle.images": "发布并分享由 mudp 管理的镜像给用户组。",
    "subtitle.volumes": "工作区范围内的持久化卷。",
    "subtitle.networks": "用于服务互联的自定义网络。",
    "subtitle.forwards": "由 mudp 中转的主机端口：所属用户、容器与目标地址，可手动添加。",
    "subtitle.stacks": "部署和管理 docker-compose 项目，实时查看进度。",
    "subtitle.users": "管理用户、用户组、角色、端口前缀与飞书审批。",
    "subtitle.usage": "按用户和容器查看 CPU、内存、磁盘、GPU 与进程使用情况。",
    "subtitle.help": "快速用户/管理员指南、常见流程与排障建议。",
    "subtitle.audit": "平台近期管理操作记录。",
    "subtitle.disks": "主机磁盘概览、挂载辅助与数据库备份。",
    "subtitle.settings": "配置镜像仓库、飞书 SSO 与系统设置。",
    "subtitle.hardware": "实时 CPU、内存、温度与每块 GPU 监控。",
    "subtitle.mcp": "为容器生成令牌，让 AI 工具（Claude Code、Codex、Kimi）可连接工作。",

    // Toasts
    "toast.refreshed": "已刷新",

    // Settings
    "settings.title": "设置",
    "settings.language": "语言",
    "settings.defaultLanguage": "默认语言",
    "settings.currentLanguage": "当前语言",
    "settings.setDefault": "设为默认",
    "settings.saved": "设置已保存",
    "settings.languageChanged": "语言已切换",
    "settings.selectLanguage": "选择语言",
    "settings.admin": "管理员设置",

    // Languages
    "lang.chinese": "中文",
    "lang.english": "English",

    // Common
    "common.save": "保存",
    "common.cancel": "取消",
    "common.close": "关闭",
    "common.edit": "编辑",
    "common.delete": "删除",
    "common.add": "添加",
    "common.confirm": "确认",
    "common.loading": "加载中...",
    "common.error": "错误",
    "common.success": "成功",

    // User
    "user.profile": "个人资料",
    "user.language": "语言",
    "user.username": "用户名",
    "user.role": "角色",
    "user.logout": "登出",
    "user.preferences": "偏好设置",

    // Admin
    "admin.settings": "管理员设置",
    "admin.defaultLanguage": "系统默认语言",
    "admin.selectDefaultLanguage": "选择系统默认语言",
    "admin.applyToAllUsers": "应用到所有用户",
    "admin.newUsersWillUse": "新用户将使用此语言",
    "admin.userCanOverride": "用户可以在个人偏好中更改语言",
    "admin.saved": "管理员设置已保存",

    // Group
    "group.languages": "组语言",
    "group.defaultLanguage": "默认语言",
    "group.setLanguage": "设置语言",
    "group.groupLanguageSaved": "组语言已保存",

    // Login
    "login.title": "登录",
    "login.username": "用户名",
    "login.password": "密码",
    "login.signIn": "登录",
    "login.hint": "使用初始设置时创建的管理员账号登录，或通过 MUDP_ADMIN_USER / MUDP_ADMIN_PASSWORD 配置。",
    "login.or": "或",
    "login.feishu": "飞书登录",
    "login.brandSubtitle": "一个轻量自托管的容器控制台。从一个简洁面板管理 Docker 工作负载、GPU 访问和网页终端。",
    "login.feature1": "一键创建容器，内置网页终端",
    "login.feature2": "实时创建进度与网页终端",
    "login.feature3": "GPU 直通与按用户配额",
    "login.feature4": "飞书单点登录与管理员审批",
  },

  [LANG_ENGLISH]: {
    // Navigation & Menu
    "nav.dashboard": "Dashboard",
    "nav.containers": "Containers",
    "nav.images": "Images",
    "nav.volumes": "Volumes",
    "nav.networks": "Networks",
    "nav.forwards": "Port Forwarding",
    "nav.stacks": "Stacks",
    "nav.users": "Users",
    "nav.usage": "Usage",
    "nav.help": "Help",
    "nav.audit": "Audit",
    "nav.settings": "Settings",
    "nav.netdisk": "Netdisk",
    "nav.disks": "Disks",
    "nav.hardware": "Hardware",
    "nav.mcp": "MCP",
    "nav.usersGroups": "Users & Groups",
    "nav.activityLog": "Activity Log",
    "nav.toggleMenu": "Menu",

    // Shell actions
    "action.searchContainers": "Search containers",
    "action.newContainer": "+ New Container",
    "action.refresh": "Refresh",

    // Section subtitles
    "subtitle.dashboard": "Environment overview, resource counts, and your workspace at a glance.",
    "subtitle.netdisk": "Manage personal files, batch uploads, resumed uploads, and downloads.",
    "subtitle.containers": "Create and manage containers with a built-in web terminal.",
    "subtitle.images": "Publish and share mudp-managed images with user groups.",
    "subtitle.volumes": "Persistent volumes scoped to your workspace.",
    "subtitle.networks": "Custom networks for service-to-service connectivity.",
    "subtitle.forwards": "Host ports mudp relays: the user and container behind each, plus rules you add by hand.",
    "subtitle.stacks": "Deploy and manage docker-compose projects with live progress.",
    "subtitle.users": "Manage users, groups, roles, port prefixes, and Feishu approvals.",
    "subtitle.usage": "Per-user and per-container CPU, memory, disk, GPU, and process usage.",
    "subtitle.help": "Quick user and admin guides, common workflows, and troubleshooting tips.",
    "subtitle.audit": "Recent management actions across the platform.",
    "subtitle.disks": "Host disk overview, mount helpers, and database backups.",
    "subtitle.settings": "Configure registries, Feishu SSO, and system settings.",
    "subtitle.hardware": "Real-time CPU, memory, temperature, and per-GPU monitoring.",
    "subtitle.mcp": "Generate per-container tokens so AI tools (Claude Code, Codex, Kimi) can connect and work.",

    // Toasts
    "toast.refreshed": "Refreshed",

    // Settings
    "settings.title": "Settings",
    "settings.language": "Language",
    "settings.defaultLanguage": "Default Language",
    "settings.currentLanguage": "Current Language",
    "settings.setDefault": "Set as Default",
    "settings.saved": "Settings saved",
    "settings.languageChanged": "Language switched",
    "settings.selectLanguage": "Select Language",
    "settings.admin": "Administrator Settings",

    // Languages
    "lang.chinese": "中文",
    "lang.english": "English",

    // Common
    "common.save": "Save",
    "common.cancel": "Cancel",
    "common.close": "Close",
    "common.edit": "Edit",
    "common.delete": "Delete",
    "common.add": "Add",
    "common.confirm": "Confirm",
    "common.loading": "Loading...",
    "common.error": "Error",
    "common.success": "Success",

    // User
    "user.profile": "Profile",
    "user.language": "Language",
    "user.username": "Username",
    "user.role": "Role",
    "user.logout": "Logout",
    "user.preferences": "Preferences",

    // Admin
    "admin.settings": "Administrator Settings",
    "admin.defaultLanguage": "System Default Language",
    "admin.selectDefaultLanguage": "Select System Default Language",
    "admin.applyToAllUsers": "Apply to All Users",
    "admin.newUsersWillUse": "New users will use this language",
    "admin.userCanOverride": "Users can change language in their preferences",
    "admin.saved": "Administrator settings saved",

    // Group
    "group.languages": "Group Languages",
    "group.defaultLanguage": "Default Language",
    "group.setLanguage": "Set Language",
    "group.groupLanguageSaved": "Group language saved",

    // Login
    "login.title": "Sign in",
    "login.username": "Username",
    "login.password": "Password",
    "login.signIn": "Sign In",
    "login.hint": "Sign in with the administrator account created during initial setup, or configured via MUDP_ADMIN_USER / MUDP_ADMIN_PASSWORD.",
    "login.or": "or",
    "login.feishu": "Sign in with Feishu",
    "login.brandSubtitle": "A compact, self-hosted container console. Manage Docker workloads, GPU access and web terminal — all from one clean panel.",
    "login.feature1": "One-click containers with web terminal",
    "login.feature2": "Live creation progress and web terminal",
    "login.feature3": "GPU passthrough and per-user quotas",
    "login.feature4": "Feishu single sign-on with admin approval",
  },
};

// Current language state
let currentLanguage = DEFAULT_LANGUAGE;

// Initialize i18n with saved language preference or system default
export function initI18n(userLanguage, systemDefaultLanguage, groupLanguage = null) {
  const savedLang = localStorage.getItem("mudp_language");
  
  if (savedLang && SUPPORTED_LANGS.includes(savedLang)) {
    currentLanguage = savedLang;
  } else if (userLanguage && SUPPORTED_LANGS.includes(userLanguage)) {
    currentLanguage = userLanguage;
  } else if (groupLanguage && SUPPORTED_LANGS.includes(groupLanguage)) {
    currentLanguage = groupLanguage;
  } else if (systemDefaultLanguage && SUPPORTED_LANGS.includes(systemDefaultLanguage)) {
    currentLanguage = systemDefaultLanguage;
  } else {
    currentLanguage = DEFAULT_LANGUAGE;
  }
  
  applyLanguage();
  return currentLanguage;
}

// Get translation for a given key
export function t(key, defaultValue = null) {
  const langDict = translations[currentLanguage] || translations[DEFAULT_LANGUAGE];
  return langDict[key] || defaultValue || key;
}

// Switch to a different language
export async function switchLanguage(lang) {
  if (!SUPPORTED_LANGS.includes(lang)) {
    console.warn(`Unsupported language: ${lang}`);
    return false;
  }

  currentLanguage = lang;
  localStorage.setItem("mudp_language", lang);

  // Persist preference to server
  try {
    await fetch("/api/user/language", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": getCsrfToken(),
      },
      body: JSON.stringify({ language: lang }),
    });
  } catch (err) {
    console.error("Failed to save language preference:", err);
  }

  applyLanguage();
  return true;
}

// Get current language
export function getCurrentLanguage() {
  return currentLanguage;
}

// Get language name
export function getLanguageName(lang) {
  const langKey = lang === LANG_CHINESE ? "lang.chinese" : "lang.english";
  return t(langKey);
}

// Apply language to the DOM (for dynamically rendered content)
export function applyLanguage() {
  document.documentElement.lang = currentLanguage === LANG_CHINESE ? "zh-CN" : "en-US";
  document.documentElement.dir = "ltr";
  
  // Dispatch event for components to listen to language changes
  window.dispatchEvent(new CustomEvent("languagechange", { detail: { language: currentLanguage } }));
}

// Helper to get CSRF token from cookie
function getCsrfToken() {
  const match = document.cookie.match(/(?:^|;\s*)mudp_csrf=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : "";
}

// Batch translate multiple keys
export function translateBatch(keys) {
  const result = {};
  keys.forEach((key) => {
    result[key] = t(key);
  });
  return result;
}
