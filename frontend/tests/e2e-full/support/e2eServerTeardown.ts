import { cleanupE2ERunnerArtifacts, stopE2EServer } from "./e2eServer";

export default async function globalTeardown(): Promise<void> {
  try {
    await stopE2EServer();
  } finally {
    await cleanupE2ERunnerArtifacts();
  }
}
