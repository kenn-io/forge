import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";

const mobile = path.resolve(import.meta.dirname, "..");
const android = path.join(mobile, "android");
const gradlew = path.join(android, "gradlew");
const apk = path.join(android, "app/build/outputs/apk/release/app-release.apk");

const args = process.argv.slice(2);
const dryRun = args.includes("--dry-run");
const unknown = args.filter((arg) => arg !== "--dry-run");
if (unknown.length) throw new Error(`Unknown arguments: ${unknown.join(" ")}. Usage: release-android.mjs [--dry-run]`);

function run(command, commandArgs, cwd, env = process.env) {
  console.log(`$ ${[command, ...commandArgs].join(" ")}  (in ${cwd})`);
  if (!dryRun) execFileSync(command, commandArgs, { cwd, env, stdio: "inherit" });
}

// The APK embeds assets/web.bundle, so rebuild it from the current frontend first.
run(process.execPath, [path.join(mobile, "scripts/build-web.mjs")], mobile);

if (!existsSync(gradlew)) {
  // CI=1 keeps Expo from prompting; dependencies are already installed with Bun.
  run(
    process.execPath,
    [path.join(mobile, "node_modules/expo/bin/cli"), "prebuild", "--platform", "android", "--no-install"],
    mobile,
    { ...process.env, CI: "1" },
  );
}

// React Native's bundle task tracks JS/TS inputs, not assets/web.bundle. Force it
// in its own invocation so assembleRelease packages the fresh bundle instead of a
// cached one.
run(gradlew, [":app:createBundleReleaseJsAndAssets", "--rerun-tasks"], android);
run(gradlew, [":app:assembleRelease"], android);

if (!dryRun && !existsSync(apk)) throw new Error(`Gradle finished without producing ${apk}`);
console.log(`Release APK: ${apk}`);
