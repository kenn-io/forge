import { Effect } from "effect";
import { describe, expect, test } from "vite-plus/test";

import { createKataTaskAPI } from "./taskClient.js";

type LiveKataHarnessModule = typeof import("../../../../tests/e2e-full/support/kataLiveHarness");
type LiveKataHarness = Awaited<ReturnType<LiveKataHarnessModule["createLiveKataHarness"]>>;
type ForgeKataHome = Awaited<ReturnType<LiveKataHarnessModule["configureForgeKataHome"]>>;
type ForgeServer = { info: { base_url: string }; stop: () => Promise<void> };

describe.skipIf(process.env.KENN_FORGE_LIVE_KATA_TESTS !== "1")("createKataTaskAPI live daemon integration", () => {
  test("runs core task mutations through the kenn-forge proxy", async () => {
    let harness: LiveKataHarness | undefined;
    let kataHome: ForgeKataHome | undefined;
    let server: ForgeServer | undefined;
    try {
      const { configureForgeKataHome, createLiveKataHarness } =
        await import("../../../../tests/e2e-full/support/kataLiveHarness");
      const { startIsolatedE2EServer } = await import("../../../../tests/e2e-full/support/e2eServer");
      harness = await createLiveKataHarness();
      kataHome = await configureForgeKataHome(harness.baseURL);
      server = await startIsolatedE2EServer();

      const seeded = await harness.seedIssue({
        projectName: "Kenn Forge Client Mutations",
        issueTitle: "Seed client mutation project",
        issueBody: "Created so the client can reuse the project identity.",
      });
      const api = createKataTaskAPI({
        fetchImpl: createForgeFetch(server),
      });
      const options = { daemonId: "live" };

      await expect(
        Effect.runPromise(
          api.createIssue(
            seeded.project.id,
            "kenn-forge-e2e",
            {
              title: "Exercise client mutations",
              body: "Original body",
              force_new: true,
            },
            options,
            "01KENN_FORGECLIENTMUT000001",
          ),
        ),
      ).resolves.toEqual({
        changed: true,
      });

      const peer = await harness.seedIssue({
        projectName: "Kenn Forge Client Mutations",
        issueTitle: "Related client peer",
        issueBody: "Related through the mutation-only client.",
      });
      const target = { project_id: seeded.project.id, ref: seeded.issue.short_id };

      await expect(
        Effect.runPromise(api.addComment(target, "kenn-forge-e2e", "Client mutation comment", options)),
      ).resolves.toEqual({ changed: true });
      await expect(Effect.runPromise(api.addLabel(target, "kenn-forge-e2e", "ui", options))).resolves.toEqual({
        changed: true,
      });
      await expect(Effect.runPromise(api.removeLabel(target, "kenn-forge-e2e", "ui", options))).resolves.toEqual({
        changed: true,
      });
      await expect(
        Effect.runPromise(
          api.editIssue(
            target,
            "kenn-forge-e2e",
            {
              title: "Exercise client mutations updated",
              body: "Updated body",
              links_delta: { add_related: [peer.issue.short_id] },
            },
            options,
          ),
        ),
      ).resolves.toEqual({ changed: true });
      await expect(
        Effect.runPromise(
          api.closeIssue(
            target,
            "kenn-forge-e2e",
            {
              reason: "done",
              message: "Finished through the kenn-forge client mutation coverage.",
              source: "ui",
            },
            options,
          ),
        ),
      ).resolves.toEqual({ changed: true });
      await expect(Effect.runPromise(api.reopenIssue(target, "kenn-forge-e2e", options))).resolves.toEqual({
        changed: true,
      });

      const detail = await harness.getIssue(seeded.issue.uid);
      expect(detail.issue).toMatchObject({
        uid: seeded.issue.uid,
        title: "Exercise client mutations updated",
        body: "Updated body",
        status: "open",
      });
      expect((detail.comments ?? []).map((comment) => (comment as { body?: string }).body)).toContain(
        "Client mutation comment",
      );
      expect((detail.labels ?? []).map((label) => (label as { label?: string }).label)).not.toContain("ui");
      expect(detail.links ?? []).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            type: "related",
            to: expect.objectContaining({ short_id: peer.issue.short_id }),
          }),
        ]),
      );
    } finally {
      await server?.stop();
      await kataHome?.stop();
      await harness?.stop();
    }
  });
});

function createForgeFetch(server: ForgeServer): typeof fetch {
  return (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    return fetch(new URL(raw, server.info.base_url), init);
  };
}
