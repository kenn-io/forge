import { createRequire } from "node:module";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vite";

const frontendRequire = createRequire(new URL("../../frontend/package.json", import.meta.url));
const interFont = frontendRequire.resolve("@kenn-io/kit-ui/fonts/inter-var.woff2");
const jetbrainsMonoFont = frontendRequire.resolve("@kenn-io/kit-ui/fonts/jetbrains-mono-var.woff2");

// The build output is copied into internal/githubapp/ui/dist and
// embedded in the kenn-forge binary, which serves it from
// an ephemeral loopback port during the app creation flow.
export default defineConfig({
  plugins: [svelte()],
  base: "./",
  resolve: {
    alias: [
      { find: "@kenn-io/kit-ui/fonts/inter-var.woff2", replacement: interFont },
      { find: "@kenn-io/kit-ui/fonts/jetbrains-mono-var.woff2", replacement: jetbrainsMonoFont },
    ],
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // Inline bundled woff2 fonts into the stylesheet to satisfy the
    // first-paint check and avoid painting a fallback OS face.
    assetsInlineLimit: (file) => (file.endsWith(".woff2") ? true : undefined),
  },
});
