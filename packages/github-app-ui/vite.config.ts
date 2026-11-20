import { realpathSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig, searchForWorkspaceRoot } from "vite";

const frontendRequire = createRequire(new URL("../../frontend/package.json", import.meta.url));
const interFont = frontendRequire.resolve("@kenn-io/kit-ui/fonts/inter-var.woff2");
const jetbrainsMonoFont = frontendRequire.resolve("@kenn-io/kit-ui/fonts/jetbrains-mono-var.woff2");
const workspaceRoot = searchForWorkspaceRoot(process.cwd());
// Bun links @kenn-io/kit-ui from its global store outside the checkout, so the
// dev server must allow serving both resolved font paths alongside the workspace.
const devAllowedDirs = [
  ...new Set([workspaceRoot, path.dirname(realpathSync(interFont)), path.dirname(realpathSync(jetbrainsMonoFont))]),
];

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
  server: {
    fs: { allow: devAllowedDirs },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // Inline bundled woff2 fonts into the stylesheet to satisfy the
    // first-paint check and avoid painting a fallback OS face.
    assetsInlineLimit: (file) => (file.endsWith(".woff2") ? true : undefined),
  },
});
