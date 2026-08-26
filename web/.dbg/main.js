import { createApp, h } from "vue";
import ElementPlus, { ElMessageBox } from "element-plus";
import * as Icons from "@element-plus/icons-vue";
import "element-plus/dist/index.css";
import "../src/styles/index.css";

const app = createApp({ render: () => h("div") });
app.use(ElementPlus);
for (const [name, c] of Object.entries(Icons)) app.component(name, c);
app.mount("#app");
window.ElMessageBox = ElMessageBox;
