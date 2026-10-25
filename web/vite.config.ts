/// <reference types="vitest" />
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Dev server proxies the same paths nginx proxies in the container (see nginx.conf),
// so the browser always talks to one origin and needs no CORS.
const gateway = process.env.GATEWAY_PUBLIC_URL ?? "http://localhost:8080";
const forecast = process.env.FORECAST_PUBLIC_URL ?? "http://localhost:8090";
const simGrn = process.env.SIM_GRN_PUBLIC_URL ?? "http://localhost:8102";
const simGst = process.env.SIM_GST_PUBLIC_URL ?? "http://localhost:8103";
const simUdyam = process.env.SIM_UDYAM_PUBLIC_URL ?? "http://localhost:8104";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    proxy: {
      "/api": { target: gateway, changeOrigin: true },
      "/forecast": { target: forecast, changeOrigin: true },
      "/sim/grn": { target: simGrn, changeOrigin: true },
      "/sim/qc": { target: simGrn, changeOrigin: true },
      "/sim/invoice": { target: simGst, changeOrigin: true },
      "/udyam": { target: simUdyam, changeOrigin: true },
    },
  },
  build: { outDir: "dist", sourcemap: false, chunkSizeWarningLimit: 1200 },
  test: { environment: "jsdom", globals: true, setupFiles: ["src/test/setup.ts"] },
});
