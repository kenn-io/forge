import { execFileSync } from "node:child_process";
import path from "node:path";
import { readFileSync, writeFileSync, readdirSync, unlinkSync } from "node:fs";
import { gzipSync } from "node:zlib";

const mobile = path.resolve(import.meta.dirname, "..");
execFileSync(
  process.execPath,
  [path.resolve(mobile, "../node_modules/vite-plus/bin/vp"), "build", "--config", path.join(mobile, "vite.config.mts")],
  { cwd: path.resolve(mobile, "../frontend"), stdio: "inherit" },
);

const assets = path.join(mobile, "assets");
if (readdirSync(assets).join() !== "index.html") throw new Error("The mobile frontend must be self-contained");
const html = path.join(assets, "index.html");
// Keep the HTML passed through the native WebView small.
writeFileSync(path.join(assets, "web.bundle"), gzipSync(readFileSync(html)).toString("base64"));
unlinkSync(html);
