import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  root: new URL(".", import.meta.url).pathname,
  plugins: [vue()],
  resolve: {
    alias: {
      "@software-factory/core/result-summary": new URL("../../core/dist/result-summary.js", import.meta.url).pathname,
    },
  },
  build: {
    outDir: "../dist/web",
    emptyOutDir: true,
  },
});
