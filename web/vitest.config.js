import { defineConfig } from "vitest/config";
import path from "node:path";

export default defineConfig({
  resolve: {
    alias: {
      // Mirror vite.config.js so unit tests can import src modules whose own
      // imports use the "@/…" form.
      "@/lib": path.resolve(__dirname, "lib"),
      "@": path.resolve(__dirname, "src"),
    },
  },
  test: {
    environment: "node",
    include: ["tests/unit/**/*.test.js"],
    globals: false,
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html"],
      include: ["lib/**/*.js", "src/**/*.js", "share.js"],
    },
  },
});
