import { execFileSync } from "node:child_process";
import path from "node:path";
import { readFileSync, readdirSync, statSync } from "node:fs";

const mobile = path.resolve(import.meta.dirname, "..");
execFileSync(
  process.execPath,
  [path.resolve(mobile, "../node_modules/vite-plus/bin/vp"), "build", "--config", path.join(mobile, "vite.config.mts")],
  { cwd: path.resolve(mobile, "../frontend"), stdio: "inherit" },
);

// The WebView writes index.html into the page, and the APK serves everything
// else as files. Android WebView renders an empty page once the HTML handed to
// it reaches about 15 MB, so fail the build if code or fonts get inlined again.
const web = path.join(mobile, "assets/android/forge-web");
const index = readFileSync(path.join(web, "index.html"), "utf8");
const indexLimit = 64 * 1024;
if (index.length > indexLimit) {
  throw new Error(`index.html is ${index.length} bytes; keep it under ${indexLimit} so the page loads its assets as files`);
}
const inlined = readdirSync(web, { recursive: true })
  .filter((file) => /\.(css|js|html)$/.test(file) && statSync(path.join(web, file)).isFile())
  .filter((file) => /data:(font|image)\/[a-z0-9.+-]+;base64,/.test(readFileSync(path.join(web, file), "utf8")));
if (inlined.length) throw new Error(`Fonts or images are inlined in ${inlined.join(", ")}; ship them as files`);
