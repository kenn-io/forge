import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vite-plus/test";
import { makeAppRuntime } from "./runtime.js";
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
    const reporter = startRuntimeTelemetry(runtime, { storageKey: "test.browser.telemetry" });
    const quiet = vi.fn();
    reporter.onQuietChange(quiet);
    onTestFinished(async () => {
      reporter.stop();
      await Effect.runPromise(runtime.disposeEffect);
    });
    return { sent, runtime, reporter, quiet };
  }

  it("sends nothing after an enabled gate receives a disabled receipt", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let status = "queued";
    const { sent, runtime, reporter, quiet } = setup(async () => Response.json({ status }, { status: 202 }));
    await vi.waitFor(() => expect(quiet).toHaveBeenLastCalledWith(false));
    status = "disabled";
    reporter.reportScreen("docs");
    await vi.waitFor(() => expect(quiet).toHaveBeenLastCalledWith(true));
    expect(sent).toHaveLength(2);
    reportPRViewed(runtime, "gitlab");
    reportWorkspaceOpened(runtime, "adhoc");
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(2);
  });

  it.each(["same day", "expired day", "recovering after opt-out"])(
    "canonicalizes and deduplicates pending actions, expires old days, and readmits next day: %s",
    async (mode) => {
      vi.useFakeTimers({ toFake: ["Date"] });
      const recovering = mode === "recovering after opt-out";
      vi.setSystemTime(new Date(recovering ? "2026-10-16T23:59:00Z" : "2026-10-17T23:59:00Z"));
      const receipt = Promise.withResolvers<Response>();
      let checking = !recovering;
      const { sent, runtime, quiet } = setup(() =>
        checking
          ? receipt.promise.then((response) => response.clone())
          : Promise.resolve(Response.json({ status: "disabled" }, { status: 202 })),
      );
      if (recovering) {
        await vi.waitFor(() => expect(sent).toHaveLength(1));
        await new Promise((resolve) => setTimeout(resolve, 0));
        checking = true;
        vi.setSystemTime(new Date("2026-10-17T23:59:00Z"));
        window.dispatchEvent(new Event("focus"));
        await vi.waitFor(() => expect(sent).toHaveLength(2));
      }
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
      if (mode === "expired day") vi.setSystemTime(new Date("2026-10-18T00:00:01Z"));
      receipt.resolve(Response.json({ status: "queued" }, { status: 202 }));
      await vi.waitFor(() => expect(quiet).toHaveBeenLastCalledWith(false));
      await new Promise((resolve) => setTimeout(resolve, 0));
      if (mode === "expired day") {
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
    },
  );

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
