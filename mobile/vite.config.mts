import path from "node:path";
import frontend from "../frontend/vite.config";

// The Android app packages these files in the APK and serves them under
// /__forge_app__/ on the connected server's origin (see embedded-page.ts).
export default {
  ...frontend,
  base: "/__forge_app__/",
  publicDir: false,
  plugins: [
    ...frontend.plugins.flat().filter((plugin) => plugin.name !== "kenn-forge-precompress-assets"),
    {
      name: "forge-mobile-assets",
      enforce: "pre",
      transformIndexHtml(html: string) {
        return html.replace(/<link[^>]*rel="icon"[^>]*>/, '<link rel="icon" href="data:,">');
      },
    },
  ],
  build: {
    ...frontend.build,
    // Local files load fast, so keep fonts and images out of the stylesheet.
    assetsInlineLimit: 0,
    outDir: path.resolve(import.meta.dirname, "assets/android/forge-web"),
  },
};
