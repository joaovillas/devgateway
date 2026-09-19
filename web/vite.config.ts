import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The gateway's admin port in development. The vite dev server forwards
// /api to it, so the panel runs on :5173 talking to the real process,
// without CORS.
const admin = process.env.GATEWAY_ADMIN_URL ?? "http://localhost:8081";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: admin,
        changeOrigin: false,
        // SSE: no buffering and no timeout, so /api/events arrives event by event.
        timeout: 0,
        proxyTimeout: 0,
      },
    },
  },
  build: {
    outDir: "dist",
    // The build replaces the versioned placeholder; `npm run clean` restores it.
    emptyOutDir: true,
    // Fonts always as files: no giant data: URI in the CSS.
    assetsInlineLimit: 0,
    sourcemap: false,
  },
});
