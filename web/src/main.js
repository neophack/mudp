import { createApp } from "vue";
import ElementPlus from "element-plus";
import * as ElementPlusIconsVue from "@element-plus/icons-vue";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import App from "./App.vue";
import { router } from "./router";
import { store, initTheme } from "./store";
import "./styles/index.css";

initTheme();

const app = createApp(App);
app.use(ElementPlus);
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
