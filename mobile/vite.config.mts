import path from "node:path";
import { viteSingleFile } from "vite-plugin-singlefile";
import frontend from "../frontend/vite.config";

export default {
  ...frontend,
  publicDir: false,
  plugins: [
    ...frontend.plugins.flat().filter((plugin) => plugin.name !== "kenn-forge-precompress-assets"),
    {
      name: "forge-mobile-assets",
      enforce: "pre",
      transformIndexHtml(html: string) {
        return html.replace(/<link[^>]*rel="icon"[^>]*>/, '<link rel="icon" href="data:,">');
      },
      transform(code: string, id: string) {
        if (!id.endsWith("/pierre-worker-pool.ts")) return;
        return (
          'import InlineDiffWorker from "./pierre-diff-worker-entry.js?worker&inline";\n' +
          code.replace(
            /new Worker\(new URL\("\.\/pierre-diff-worker-entry\.js", import\.meta\.url\), \{\s*type: "module",\s*\}\)/,
            "new InlineDiffWorker()",
          )
        );
      },
    },
    viteSingleFile(),
  ],
  build: {
    ...frontend.build,
    outDir: path.resolve(import.meta.dirname, "assets"),
  },
};
