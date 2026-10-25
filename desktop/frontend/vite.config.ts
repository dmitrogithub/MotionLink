import { defineConfig } from "vite";

export default defineConfig({
  server: {
    // The UI lives in ../../web/src and is shared with the browser client.
    fs: { allow: ["..", "../../web"] },
  },
});
