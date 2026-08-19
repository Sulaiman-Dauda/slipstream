import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // The panel serves font-src 'self', so a font inlined as a data: URI is
    // refused by the browser and the page silently falls back to a system
    // face. One woff2 subset is small enough to fall under Vite's default
    // 4 kB inline limit, so keep fonts out of it rather than widening the
    // policy to allow data: URIs.
    assetsInlineLimit: (file: string) =>
      file.endsWith(".woff2") || file.endsWith(".woff") ? false : undefined,
  },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:9080", changeOrigin: true },
    },
  },
});
