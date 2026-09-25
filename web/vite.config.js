import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import Components from "unplugin-vue-components/vite";
import { ElementPlusResolver } from "unplugin-vue-components/resolvers";
import path from "node:path";

// Dev workflow: `npm run dev` starts Vite on :5173 and proxies every
// non-asset request to the Go server on :9000 (`go run ./cmd/mudp`), so the
// SPA gets live reload while sessions/CSRF/Docker data come from the real
// backend. The production build outputs to dist/, which go:embed picks up.
const UPSTREAM = process.env.MUDP_UPSTREAM || "http://127.0.0.1:9000";

const proxy = {};
for (const p of ["/api", "/lib", "/share.js"]) {
  proxy[p] = { target: UPSTREAM, ws: true };
}

export default defineConfig({
  plugins: [
    vue(),
    // Element Plus components (and their styles, plus the v-loading-style
    // directives) are resolved per template usage, so the app ships only what
    // it renders instead of the full library. Function APIs that the plugin
    // cannot see — ElMessage/ElMessageBox called from <script> — keep their
    // explicit imports and get their styles pinned in main.js.
    Components({ resolvers: [ElementPlusResolver()] }),
  ],
  resolve: {
    alias: {
      // The framework-independent helpers under web/lib are shared with the
      // standalone share page, so they live outside src/. Must precede "@" so
      // "@/lib/…" resolves there instead of src/lib.
      "@/lib": path.resolve(__dirname, "lib"),
      "@": path.resolve(__dirname, "src"),
    },
  },
  server: { port: 5173, proxy },
  build: { outDir: "dist" },
});
