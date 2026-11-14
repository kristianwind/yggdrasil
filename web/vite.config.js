import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  plugins: [svelte()],
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        // Not changeOrigin: the panel compares Origin with Host, port included, so
        // the Host it sees has to be the one the browser is on.
        changeOrigin: false,
        ws: true,
      },
    },
  },
});
