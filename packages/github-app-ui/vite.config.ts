import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vite";

// The build output is copied into internal/githubapp/ui/dist and
// embedded in the middleman-github-app binary, which serves it from
// an ephemeral loopback port during the app creation flow.
export default defineConfig({
  plugins: [svelte()],
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
