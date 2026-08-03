import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

export default defineConfig({
  base: "/api/v1/calls/",
  plugins: [react()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    host: true,
    port: 5173,
    allowedHosts: ['crmlocal.duology.com.br', '.duology.com.br', 'localhost', '127.0.0.1'],
    proxy: {
      "/api": {
        target: "http://localhost:3001",
        changeOrigin: true,
        ws: false,
      },
    },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
