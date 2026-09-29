import preact from "@preact/preset-vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [preact()],
  build: { outDir: "../web/dist", emptyOutDir: true, assetsInlineLimit: 0 },
  server: {
    // `npm run dev` proxies the API to a local `hoist` binary (or HOIST_URL).
    // Keep the Host header: Hoist refuses writes whose Origin doesn't match it.
    proxy: {
      "/api": { target: process.env.HOIST_URL ?? "http://localhost:8080", changeOrigin: false },
    },
  },
  test: { environment: "jsdom" },
});
