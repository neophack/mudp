import { createApp } from "vue";
import * as ElementPlusIconsVue from "@element-plus/icons-vue";
// Element Plus components are resolved per-template by unplugin-vue-components
// (see vite.config.js), so there is no app-wide install. These style entries
// cover what templates can't: ElMessage and ElMessageBox are imported directly
// by views, and the loading style backs the v-loading directive usage.
import "element-plus/es/components/message/style/css";
import "element-plus/es/components/message-box/style/css";
import "element-plus/es/components/loading/style/css";
// Dark palette variables ("dark" class on <html>, driven by store.js).
import "element-plus/theme-chalk/dark/css-vars.css";
import App from "./App.vue";
import { router } from "./router";
import { store, initTheme } from "./store";
import "./styles/index.css";

initTheme();

const app = createApp(App);
// Element Plus icon props are string component names ("Search", "VideoPlay"…),
// resolved through globally registered icon components.
for (const [name, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(name, component);
}
app.use(router);

// Live phone-width flag: list views collapse their action columns into a
// bottom action sheet as soon as (and while) the viewport is phone-sized.
const mq = window.matchMedia("(max-width: 768px)");
store.isMobile = mq.matches;
mq.addEventListener("change", (e) => { store.isMobile = e.matches; });

app.mount("#app");
