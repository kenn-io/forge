import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import test from "node:test";

import { frontendApiClient } from "../frontend/scripts/generate-api-client.mjs";

const require = createRequire(new URL("../frontend/package.json", import.meta.url));
const { build, createServer } = await import(pathToFileURL(require.resolve("vite")));

test("Vite builds and serves a missing API client, then regenerates changed constraints", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "forge-vite-api-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await writeFile(
    join(root, "package.json"),
    JSON.stringify({ name: "example-frontend", private: true, type: "module" }),
  );
  await mkdir(join(root, "openapi"));
  await mkdir(join(root, "src/lib/api"), { recursive: true });
  const spec = {
    openapi: "3.0.3",
    info: { title: "Example API", version: "1" },
    paths: {
      "/settings": {
        get: {
          operationId: "get-settings",
          tags: ["settings"],
          responses: {
            200: {
              description: "Settings",
              content: { "application/json": { schema: { $ref: "#/components/schemas/Settings" } } },
            },
          },
        },
      },
    },
    components: { schemas: { Settings: { type: "object", properties: { limit: { type: "integer", minimum: 10 } } } } },
  };
  const specPath = join(root, "openapi/openapi.yaml");
  await writeFile(specPath, JSON.stringify(spec));
  await writeFile(
    join(root, "src/lib/api/runtime.ts"),
    "export async function orvalFetch<T>(url: string, options: RequestInit): Promise<T> { return (await fetch(url, options)).json(); }",
  );
  await writeFile(join(root, "index.html"), '<script type="module" src="/src/main.ts"></script>');
  await writeFile(
    join(root, "src/main.ts"),
    'import { schemaConstraints } from "./lib/api/generated/schema-constraints.ts"; import { SettingsService } from "./lib/api/generated/index.ts"; console.log(schemaConstraints.Settings.limit.minimum, SettingsService.getSettings);',
  );
  await build({ root, configFile: false, plugins: [frontendApiClient()], logLevel: "silent" });
  const constraintsPath = join(root, "src/lib/api/generated/schema-constraints.ts");
  const built = await import(pathToFileURL(constraintsPath));
  assert.equal(built.schemaConstraints.Settings.limit.minimum, 10);

  await rm(join(root, "src/lib/api/generated"), { recursive: true });
  const server = await createServer({
    root,
    configFile: false,
    plugins: [frontendApiClient()],
    logLevel: "silent",
    server: { host: "127.0.0.1", port: 0 },
  });
  t.after(() => server.close());
  await server.listen();
  const response = await fetch(new URL("/src/main.ts", server.resolvedUrls.local[0]));
  assert.equal(response.status, 200);
  assert.ok(
    (await server.transformRequest("/src/lib/api/generated/schema-constraints.ts")).code.includes("minimum: 10"),
  );

  spec.components.schemas.Settings.properties.limit.minimum = 20;
  await writeFile(specPath, JSON.stringify(spec));
  await t.waitFor(async () => assert.match(await readFile(constraintsPath, "utf8"), /minimum: 20/), { timeout: 10000 });
  const changed = await import(`${pathToFileURL(constraintsPath)}?changed`);
  assert.equal(changed.schemaConstraints.Settings.limit.minimum, 20);
});
