/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url"

import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  // In dev the SPA runs on Vite's server and the API lives on the panel. Without
  // this proxy every /api call would be answered by Vite with index.html.
  // PB_DEV_PANEL overrides the target, e.g. https://10.10.11.106:8443.
  server: {
    proxy: {
      "/api": {
        target: process.env.PB_DEV_PANEL ?? "https://127.0.0.1:18443",
        changeOrigin: false,
        secure: false,
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
})
