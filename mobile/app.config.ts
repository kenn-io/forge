import type { ConfigContext, ExpoConfig } from "expo/config";
import { withAppBuildGradle } from "expo/config-plugins";

// Release builds set FORGE_MOBILE_VERSION to the Forge release, for example v0.4.0.
export default ({ config }: ConfigContext): ExpoConfig => {
  const release = process.env.FORGE_MOBILE_VERSION;
  if (!release) return withWebAssets(config as ExpoConfig);
  const version = release.replace(/^v/, "");
  return withWebAssets({
    ...(config as ExpoConfig),
    version,
    android: { ...config.android, versionCode: androidVersionCode(version) },
  });
};

// Package the web UI that scripts/build-web.mjs writes to assets/android as APK
// assets; the patched WebView client serves them under /__forge_app__/.
const webAssets = 'android.sourceSets.main.assets.srcDirs += file("../../assets/android")';

function withWebAssets(config: ExpoConfig): ExpoConfig {
  return withAppBuildGradle(config, (gradle) => {
    if (!gradle.modResults.contents.includes(webAssets)) gradle.modResults.contents += `\n${webAssets}\n`;
    return gradle;
  });
}

// Android installs an update only when its versionCode is higher, so each
// MAJOR.MINOR.PATCH release maps to MAJOR * 1,000,000 + MINOR * 1,000 + PATCH.
export function androidVersionCode(version: string): number {
  const match = /^(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?$/.exec(version);
  if (!match) throw new Error(`FORGE_MOBILE_VERSION must be MAJOR.MINOR.PATCH, got ${JSON.stringify(version)}`);
  const [major, minor, patch] = match.slice(1).map(Number);
  if (minor > 999 || patch > 999) throw new Error(`Minor and patch versions must be below 1000, got ${version}`);
  const code = major * 1_000_000 + minor * 1_000 + patch;
  if (code < 1) throw new Error(`Android versionCode must be positive, got ${version}`);
  return code;
}
