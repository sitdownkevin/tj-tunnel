import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The bundle is embedded into the Go binary, so assets must be relative to
// the document root that the server exposes.
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    sourcemap: false,
    target: "es2020",
    rollupOptions: {
      output: {
        // Content-hashed names so the immutable cache in the server stays
        // correct across upgrades.
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
      },
    },
  },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", ws: true },
    },
  },
});
