// @vitest-environment node

import { mkdirSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { defaultDevApiUrl, resolveDevApiUrl } from "./apiProxyTarget";

describe("resolveDevApiUrl", () => {
  const tempDirs: string[] = [];

  afterEach(() => {
    tempDirs.length = 0;
  });

  it("prefers KENN_FORGE_API_URL when present", () => {
    expect(
      resolveDevApiUrl({
        HOME: "/ignored",
        KENN_FORGE_API_URL: "http://127.0.0.1:9123/custom",
      }),
    ).toBe("http://127.0.0.1:9123/custom");
  });

  it("reads host, port, and base path from KENN_FORGE_HOME config", () => {
    const forgeHome = makeTempDir();
    writeConfig(
      forgeHome,
      `
host = "127.0.0.1"
port = 9123
base_path = "/kenn-forge/"
`,
    );

    expect(
      resolveDevApiUrl({
        HOME: "/ignored",
        KENN_FORGE_HOME: forgeHome,
      }),
    ).toBe("http://127.0.0.1:9123/kenn-forge");
  });

  it("prefers explicit KENN_FORGE_CONFIG over KENN_FORGE_HOME and HOME defaults", () => {
    const home = makeTempDir();
    const forgeHome = makeTempDir();
    const explicitConfigPath = path.join(makeTempDir(), "custom.toml");

    writeConfig(
      path.join(home, ".kenn", "forge"),
      `
port = 9234
`,
    );
    writeConfig(
      forgeHome,
      `
port = 9345
`,
    );
    writeConfigFile(
      explicitConfigPath,
      `
port = 9456
`,
    );

    const env = {
      HOME: home,
      KENN_FORGE_HOME: forgeHome,
      KENN_FORGE_CONFIG: explicitConfigPath,
    };

    expect(resolveDevApiUrl(env)).toBe("http://127.0.0.1:9456");
  });

  it("falls back to the default config path under HOME", () => {
    const home = makeTempDir();
    writeConfig(
      path.join(home, ".kenn", "forge"),
      `
port = 9234
`,
    );

    expect(
      resolveDevApiUrl({
        HOME: home,
      }),
    ).toBe("http://127.0.0.1:9234");
  });

  it("parses full TOML syntax used by backend config", () => {
    const forgeHome = makeTempDir();
    writeConfig(
      forgeHome,
      `
host = '::1'
port = 9_456
base_path = '/kenn-forge/'
`,
    );

    expect(
      resolveDevApiUrl({
        HOME: "/ignored",
        KENN_FORGE_HOME: forgeHome,
      }),
    ).toBe("http://[::1]:9456/kenn-forge");
  });

  it("falls back to the default URL when config cannot be read", () => {
    expect(
      resolveDevApiUrl({
        HOME: "/missing-home",
      }),
    ).toBe(defaultDevApiUrl);
  });

  it("formats IPv6 loopback hosts correctly", () => {
    const forgeHome = makeTempDir();
    writeConfig(
      forgeHome,
      `
host = "::1"
port = 9345
`,
    );

    expect(
      resolveDevApiUrl({
        HOME: "/ignored",
        KENN_FORGE_HOME: forgeHome,
      }),
    ).toBe("http://[::1]:9345");
  });

  function makeTempDir(): string {
    const dir = path.join(
      os.tmpdir(),
      `kenn-forge-api-proxy-target-${Date.now()}-${Math.random().toString(16).slice(2)}`,
    );
    mkdirSync(dir, { recursive: true });
    tempDirs.push(dir);
    return dir;
  }

  function writeConfig(baseDir: string, content: string): void {
    writeConfigFile(path.join(baseDir, "config.toml"), content);
  }

  function writeConfigFile(filePath: string, content: string): void {
    mkdirSync(path.dirname(filePath), { recursive: true });
    writeFileSync(filePath, content.trimStart(), "utf8");
  }
});
