import type { SpawnSyncReturns } from "node:child_process";

export function isolatedGitEnv(env?: NodeJS.ProcessEnv): NodeJS.ProcessEnv;

export function createGitTestRepository(
  lifecycle: { after: (cleanup: () => void) => void },
  options?: { env?: NodeJS.ProcessEnv; initialBranch?: string; prefix?: string },
): {
  env: NodeJS.ProcessEnv;
  git: (...args: string[]) => SpawnSyncReturns<string>;
  root: string;
  scratch: string;
};
