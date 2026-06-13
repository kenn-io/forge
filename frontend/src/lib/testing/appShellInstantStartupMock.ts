// Companion to appShellMocks.ts: replaces runAppStartup with an
// immediately-ready version so App mounts its views without waiting on the
// real settings race. Kept separate from the base preset because the
// startup-timeout suite exercises the REAL runAppStartup and must not
// import this.
import { vi } from "vite-plus/test";

vi.mock("../utils/appStartup.js", () => ({
  runAppStartup: ({ onReady }: { onReady: () => void }) => {
    queueMicrotask(onReady);
    return vi.fn();
  },
}));
