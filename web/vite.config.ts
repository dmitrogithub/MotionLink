import { defineConfig } from "vite";

export default defineConfig({
  // Relative asset paths let the same build be served by the host, a relay
  // behind a sub-path, or opened from the desktop application.
  base: "./",
  server: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
  },
  build: {
    target: "es2020",
    assetsInlineLimit: 0,
  },
});
