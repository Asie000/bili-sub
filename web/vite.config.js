import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// The build lands directly in the directory the Go binary embeds, so
// `npm run build && go build` needs no copy step.
//
// The output is committed to the repository on purpose: whoever only wants to
// run the tool clones the repo and runs `go build` without installing Node.
// Node is needed only to change the interface.
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: "../internal/server/web",
    emptyOutDir: true,
    assetsDir: "assets",
  },
  server: {
    // `npm run dev` proxies to the Go server, so the page talks to the real API
    // while Vite serves hot-reloaded modules.
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
