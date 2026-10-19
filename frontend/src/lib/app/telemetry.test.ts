import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vite-plus/test";
import { makeAppRuntime } from "./runtime.js";
import { orvalRequest } from "../api/runtime.js";
import { getRoute, replaceUrl } from "../stores/router.svelte.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { startRuntimeTelemetry, reportPRViewed, reportWorkspaceOpened, visibleScreen } from "./telemetry.js";

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

describe("browser telemetry", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  function setup(reply: () => Promise<Response> = async () => Response.json({ status: "queued" }, { status: 202 })) {
    const sent: Array<{ event: string; properties: Record<string, string> }> = [];
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      sent.push(await new Request(input, init).json());
      return reply();
    });
    const runtime = makeAppRuntime(makeGeneratedClient());
    const options = {
      route: "/api/v1/telemetry/events",
      surface: "web",
      storageKey: "test.browser.telemetry",
      post: (route: string, event: unknown, options?: { keepalive?: boolean }) =>
        orvalRequest(route, {
          method: "POST",
          body: JSON.stringify(event),
          keepalive: options?.keepalive ?? false,
        }),
    };
    const reporter = startRuntimeTelemetry(runtime, options);
    const quiet = vi.fn();
    reporter.onQuietChange(quiet);
    onTestFinished(async () => {
      reporter.stop();
      await Effect.runPromise(runtime.disposeEffect);
    });
    return { sent, runtime, reporter, quiet };
  }

  it("sends nothing after a disabled receipt, including pending core actions", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const receipt = Promise.withResolvers<Response>();
    const { sent, runtime, reporter, quiet } = setup(() => receipt.promise);
    reportPRViewed(runtime, "gh");
    reportWorkspaceOpened(runtime, "adhoc");
    reporter.reportScreen("docs");
    receipt.resolve(Response.json({ status: "disabled" }, { status: 202 }));
    await vi.advanceTimersByTimeAsync(0);
    expect(quiet).toHaveBeenLastCalledWith(true);
    reportPRViewed(runtime, "gitlab");
    reporter.reportScreen("settings");
    reporter.reportVisit(
      { event: "session_ended", properties: { surface: "web", duration_bucket: "under_1m" } },
      { keepalive: true },
    );
    window.dispatchEvent(new Event("focus"));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toEqual([{ event: "app_opened", properties: { surface: "web" } }]);
  });

  it.each([false, true])("admits pending core actions only on their UTC day, rollover: %s", async (rollover) => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-10-17T23:59:00Z"));
    const receipt = Promise.withResolvers<Response>();
    const { sent, runtime, quiet } = setup(() => receipt.promise.then((response) => response.clone()));
    const actions = () => sent.filter((event) => event.event === "pr_viewed" || event.event === "workspace_opened");
    for (let i = 0; i < 10; i++) {
      reportPRViewed(runtime, "gh");
      reportPRViewed(runtime, "gl");
      reportWorkspaceOpened(runtime, "pull_request");
      reportWorkspaceOpened(runtime, "adhoc");
    }
    reportPRViewed(runtime, "unknown");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(actions()).toEqual([]);
    if (rollover) vi.setSystemTime(new Date("2026-10-18T00:00:01Z"));
    receipt.resolve(Response.json({ status: "queued" }, { status: 202 }));
    await vi.waitFor(() => expect(quiet).toHaveBeenLastCalledWith(false));
    await new Promise((resolve) => setTimeout(resolve, 0));
    if (rollover) {
      expect(actions()).toEqual([]);
      return;
    }
    const expected = [
      { event: "pr_viewed", properties: { provider: "github", surface: "web" } },
      { event: "pr_viewed", properties: { provider: "gitlab", surface: "web" } },
      { event: "workspace_opened", properties: { item_type: "pull_request", surface: "web" } },
      { event: "workspace_opened", properties: { item_type: "adhoc", surface: "web" } },
    ];
    expect(actions()).toEqual(expected);
    reportPRViewed(runtime, "github");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(actions()).toEqual(expected);
    vi.setSystemTime(new Date("2026-10-18T00:00:01Z"));
    reportWorkspaceOpened(runtime, "adhoc");
    await vi.waitFor(() => expect(actions()).toHaveLength(5));
    expect(actions()[4]).toEqual(expected[3]);
  });

  it("readmits a core action after failed delivery", async () => {
    let status = 202;
    const { sent, runtime, quiet } = setup(async () =>
      status === 202
        ? Response.json({ status: "queued" }, { status })
        : Response.json(
            { title: "Bad Request", status: 400 },
            { status: 400, headers: { "Content-Type": "application/problem+json" } },
          ),
    );
    await vi.waitFor(() => expect(quiet).toHaveBeenLastCalledWith(false));
    status = 400;
    reportPRViewed(runtime, "github");
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    status = 202;
    reportPRViewed(runtime, "github");
    await vi.waitFor(() => expect(sent).toHaveLength(3));
  });
});
