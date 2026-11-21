import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The browser talks only to this server; /api goes to dock-api (APP: 8082 locally, the api container in compose).
const api = process.env.DOCK_API_URL ?? "http://localhost:8082";

export default defineConfig({
  plugins: [react()],
  server: {
    host: process.env.VITE_HOST ?? "localhost",
    port: 5173,
    strictPort: true,
    proxy: { "/api": { target: api, changeOrigin: true } },
  },
  preview: {
    host: process.env.VITE_HOST ?? "localhost",
    port: 5173,
    strictPort: true,
    proxy: { "/api": { target: api, changeOrigin: true } },
  },
});
