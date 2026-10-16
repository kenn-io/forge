import { waitFor } from "@testing-library/svelte";
import { makeAppRuntime } from "./runtime.js";
import { Effect, Exit } from "effect";
import { describe, expect, it } from "vite-plus/test";
import { makeGeneratedApiLayer } from "../api/generated-api.js";
import { getRoute, replaceUrl } from "../stores/router.svelte.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import {
  createScreenViewReporter,
  createCoreActionVisitReporter,
  type CoreAction,
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
    await run("docs");
    await run("docs");
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

describe("core action visits", () => {
  it.each([
    ["successful", false],
    ["failed", true],
  ])("counts each visit once after a %s report", async (_name, failure) => {
    const sent: unknown[] = [];
    const runtime = makeAppRuntime(
      makeGeneratedClient({
        SystemService: {
          captureTelemetryEvent: async (body) => {
            sent.push(body);
            if (failure) throw new Error("daemon unavailable");
            return { status: "queued" };
          },
        },
      }),
    );
    const report = createCoreActionVisitReporter(runtime);
    const action: CoreAction = { event: "pr_viewed", properties: { provider: "gitlab" } };
    report("pr-a");
    expect(sent).toEqual([]);
    report("pr-a", action);
    await waitFor(() => expect(sent).toHaveLength(1));
    report("pr-a");
    report("pr-a", action);
    report("pr-b", action);
    await waitFor(() => expect(sent).toHaveLength(2));
    report(null);
    report("pr-b", action);
    await waitFor(() => expect(sent).toHaveLength(3));
    expect(sent).toEqual([
      { event: "pr_viewed", properties: { provider: "gitlab", surface: "web" } },
      { event: "pr_viewed", properties: { provider: "gitlab", surface: "web" } },
      { event: "pr_viewed", properties: { provider: "gitlab", surface: "web" } },
    ]);
    await Effect.runPromise(runtime.disposeEffect);
  });
});
