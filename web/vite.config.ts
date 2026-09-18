import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// A porta de administração do gateway em desenvolvimento. O vite dev server
// encaminha /api para ela, então o painel roda em :5173 falando com o
// processo real, sem CORS.
const admin = process.env.GATEWAY_ADMIN_URL ?? "http://localhost:8081";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: admin,
        changeOrigin: false,
        // SSE: sem buffer nem timeout, para /api/events chegar evento a evento.
        timeout: 0,
        proxyTimeout: 0,
      },
    },
  },
  build: {
    outDir: "dist",
    // O build substitui o placeholder versionado; `npm run clean` o restaura.
    emptyOutDir: true,
    // Fontes sempre como arquivo: nada de data: URI gigante no CSS.
    assetsInlineLimit: 0,
    sourcemap: false,
  },
});
