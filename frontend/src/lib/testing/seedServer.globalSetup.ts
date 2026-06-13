// Spawns one seeded e2e Go server (cmd/e2e-server) for the whole Vitest run
// and provides its base URL to every worker via inject("seedBaseUrl"). Tests
// fetch real responses from it through ./seedClient.ts, so component tests
// assert against the exact shapes the app sees in production instead of
// checked-in JSON that drifts from the server.
import { startIsolatedE2EServerWithOptions } from "../../../tests/e2e-full/support/e2eServer";

export default async function setup({ provide }: { provide: (key: string, value: unknown) => void }) {
  // ensureEmbeddedFrontend() (run during the spawn) would rebuild/copy the
  // frontend dist; this test server only serves API endpoints, so skip it.
  // Set the flag ONLY around the spawn and restore it before workers fork:
  // e2eServerOwnership.test.ts exercises ensureEmbeddedFrontend's real build
  // logic and must not inherit a globally-set ready flag.
  const previousReady = process.env.PLAYWRIGHT_E2E_FRONTEND_READY;
  process.env.PLAYWRIGHT_E2E_FRONTEND_READY = "1";
  let server;
  try {
    server = await startIsolatedE2EServerWithOptions({ freshProcess: true });
  } finally {
    if (previousReady === undefined) {
      delete process.env.PLAYWRIGHT_E2E_FRONTEND_READY;
    } else {
      process.env.PLAYWRIGHT_E2E_FRONTEND_READY = previousReady;
    }
  }

  provide("seedBaseUrl", server.info.base_url);

  return async () => {
    await server.stop();
  };
}
