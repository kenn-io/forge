import { Effect, Exit } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "./runtime.js";
import { makeGeneratedApiLayer } from "../api/generated-api.js";
import { getRoute, replaceUrl } from "../stores/router.svelte.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import {
  createScreenViewReporter,
  reportPRViewed,
  reportWorkspaceOpened,
  type ScreenName,
  visibleScreen,
} from "./telemetry.js";

describe("visible screen names", () => {
  it.each([
    ["/", "activity"],
    ["/actions", "actions"],
    ["/repos", "repos"],
    ["/repo/browser?provider=github&repo_path=owner/repo", "repo-browser"],
    ["/pulls", "pulls"],
    ["/issues", "issues"],
    ["/docs?folder=notes&doc=README.md", "docs"],
    ["/workspaces", "workspaces"],
    ["/terminal/workspace-a", "terminal"],
    ["/settings", "settings"],
    ["/project-intake", "project-intake"],
    ["/design-system", "design-system"],
    ["/m/activity", "activity"],
    ["/m/pulls", "pulls"],
    ["/m/issues", "issues"],
    ["/m/workspaces", "workspaces"],
    ["/m/workspaces/local/workspace-a", "terminal"],
    ["/m/workspaces/local/workspace-a/item", "workspace-item"],
    ["/focus/mrs", "pulls"],
    ["/focus/issues", "issues"],
    ["/focus/pulls/github/owner/repo/1", "pulls"],
    ["/focus/issues/github/owner/repo/1", "issues"],
  ])("maps %s to %s", (path, screen) => {
    replaceUrl(path);
    expect(visibleScreen(getRoute(), true, false)).toBe(screen);
  });

  it("waits for startup and gives onboarding precedence", () => {
    expect(visibleScreen({ page: "activity" }, false, true)).toBeNull();
    expect(visibleScreen({ page: "activity" }, true, true)).toBe("onboarding");
  });
});

describe("screen view reporter", () => {
  function setup() {
    const sent: string[] = [];
    const clock = { now: new Date("2026-01-02T23:59:00Z"), fail: false };
    const layer = makeGeneratedApiLayer(
      makeGeneratedClient({
        SystemService: {
          captureTelemetryEvent: async (body) => {
            sent.push(String(body.properties?.["screen"]));
            if (clock.fail) throw new Error("daemon unavailable");
            return { status: "queued" };
          },
        },
      }),
    );
    const report = createScreenViewReporter(() => clock.now);
    const run = (screen: ScreenName) => Effect.runPromiseExit(report(screen).pipe(Effect.provide(layer)));
    return { sent, clock, run };
  }

  it("sends each screen once per UTC day", async () => {
    const { sent, clock, run } = setup();
    await Promise.all([run("docs"), run("docs")]);
    await run("pulls");
    expect(sent).toEqual(["docs", "pulls"]);
    clock.now = new Date("2026-01-03T00:00:01Z");
    await run("docs");
    expect(sent).toEqual(["docs", "pulls", "docs"]);
  });

  it("sends again after a failed report", async () => {
    const { sent, clock, run } = setup();
    clock.fail = true;
    expect(Exit.isFailure(await run("docs"))).toBe(true);
    clock.fail = false;
    expect(Exit.isSuccess(await run("docs"))).toBe(true);
    await run("docs");
    expect(sent).toEqual(["docs", "docs"]);
  });
});

it("counts core actions per value and runtime daily, retries failures, and coalesces concurrent sends", async () => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-01-02T23:59:00Z"));
  const capture = vi.fn(async (_body: unknown) => ({ status: "queued" }));
  const runtime = makeAppRuntime(makeGeneratedClient({ SystemService: { captureTelemetryEvent: capture } }));
  const drain = () => new Promise<void>((resolve) => setTimeout(resolve, 0));
  try {
    reportPRViewed(runtime, "unknown");
    await drain();
    expect(capture).not.toHaveBeenCalled();
    capture.mockRejectedValueOnce(new Error("unavailable"));
    reportPRViewed(runtime, "gh");
    await drain();
    reportPRViewed(runtime, "github");
    reportPRViewed(runtime, "github");
    await drain();
    expect(capture).toHaveBeenCalledTimes(2);
    reportPRViewed(runtime, "gl");
    reportWorkspaceOpened(runtime, "pull_request");
    reportWorkspaceOpened(runtime, "pull_request");
    reportWorkspaceOpened(runtime, "adhoc");
    await drain();
    expect(capture.mock.calls.map(([body]) => body)).toEqual([
      { event: "pr_viewed", properties: { provider: "github", surface: "web" } },
      { event: "pr_viewed", properties: { provider: "github", surface: "web" } },
      { event: "pr_viewed", properties: { provider: "gitlab", surface: "web" } },
      { event: "workspace_opened", properties: { item_type: "pull_request", surface: "web" } },
      { event: "workspace_opened", properties: { item_type: "adhoc", surface: "web" } },
    ]);
    vi.setSystemTime(new Date("2026-01-03T00:00:01Z"));
    reportPRViewed(runtime, "github");
    reportWorkspaceOpened(runtime, "adhoc");
    await drain();
    expect(capture).toHaveBeenCalledTimes(7);
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    vi.useRealTimers();
  }
});
