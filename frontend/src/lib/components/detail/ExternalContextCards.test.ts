import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Exit } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { ExternalContextWorkflow } from "../../stores/external-context-workflow.svelte.js";
import ExternalContextCards from "./ExternalContextCards.svelte";

const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));
vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtimeCapture.current }));

const props = {
  ref: { provider: "github", owner: "example", name: "project", repoPath: "example/project" },
  repositoryKey: { kind: "id", id: 123 },
  number: 42,
  headSha: "a".repeat(40),
};
const card = {
  status: "warning",
  summary: "A slower result",
  markdown: "| Test | Change |\n| --- | --- |\n| Parse input | +8% |\n\n- [ ] Review results",
  result_head_sha: "b".repeat(40),
  actions: [
    { id: "run", label: "Run checks" },
    { id: "publish", label: "Publish", disabled_reason: "Run first" },
  ],
};

describe("external PR context", () => {
  beforeEach(() => {
    runtimeCapture.current = makeAppRuntime();
  });

  afterEach(async () => {
    cleanup();
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("renders applicable results, their older-commit label and read-only Markdown", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = request.url;
        if (url.endsWith("/external-context/sources")) {
          return Response.json({
            sources: [
              { id: "checks", name: "Quality checks" },
              { id: "other", name: "Other context" },
            ],
          });
        }
        if (url.includes("/external-context/checks")) return Response.json({ card });
        if (url.includes("/external-context/other")) return Response.json({ card: null });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );
    render(ExternalContextCards, { props });

    expect(await screen.findByText("A slower result")).toBeTruthy();
    expect(screen.getByText("Quality checks")).toBeTruthy();
    expect(screen.getByText("Results are for an older commit")).toBeTruthy();
    await fireEvent.click(screen.getByText("Details"));
    expect(screen.getByRole("table").textContent).toContain("Parse input");
    expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Publish" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Run first")).toBeTruthy();
    await waitFor(() => expect(screen.queryByText("Other context")).toBeNull());
  });

  it("keeps a failed source separate from a successful card", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = request.url;
        if (url.endsWith("/external-context/sources"))
          return Response.json({
            sources: [
              { id: "checks", name: "Quality checks" },
              { id: "report", name: "Report" },
            ],
          });
        if (url.includes("/external-context/checks")) return Response.json({ card });
        return Response.json(
          {
            type: "about:blank",
            status: 504,
            title: "Source timed out",
            detail: "External context timed out.",
            code: "upstreamError",
          },
          { status: 504 },
        );
      }),
    );
    render(ExternalContextCards, { props });
    expect(await screen.findByText("A slower result")).toBeTruthy();
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "External context timed out.");
    expect(screen.getByRole("button", { name: "Refresh Report" })).toBeTruthy();
  });

  it("pins one action to the displayed commit on a host route and keeps its outcome across navigation", async () => {
    const action = Promise.withResolvers<Response>();
    const laterRead = Promise.withResolvers<Response>();
    const posts: { url: string; body: unknown; signal: AbortSignal | null | undefined }[] = [];
    let originalReads = 0;
    const fetch = vi.fn(async (request: Request) => {
      const url = request.url;
      if (url.endsWith("/external-context/sources"))
        return Response.json({ sources: [{ id: "checks", name: "Quality checks" }] });
      if (request.method === "POST") {
        posts.push({ url: new URL(url).pathname, body: await request.json(), signal: request.signal });
        return action.promise;
      }
      if (url.includes("/43/")) return Response.json({ card: { ...card, summary: "Another PR", actions: [] } });
      originalReads++;
      return originalReads === 1 ? Response.json({ card }) : laterRead.promise;
    });
    vi.stubGlobal("fetch", fetch);
    const hostProps = {
      ...props,
      ref: {
        provider: "gitlab",
        platformHost: "code.example.test",
        owner: "team/tools",
        name: "project",
        repoPath: "team/tools/project",
      },
    };
    const view = render(ExternalContextCards, { props: hostProps });
    await fireEvent.click(await screen.findByRole("button", { name: "Run checks" }));
    const pending = screen.getByRole("button", { name: "Submitting…" });
    expect((pending as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(pending);
    expect(posts).toHaveLength(1);
    expect(posts[0]?.url).toBe(
      "/api/v1/host/code.example.test/pulls/gitlab/team%2Ftools/project/42/external-context/checks/actions/run",
    );
    expect(posts[0]?.body).toEqual({ platform_repo_id: 123, head_sha: "a".repeat(40) });
    await view.rerender({ ...hostProps, number: 43 });
    expect(await screen.findByText("Another PR")).toBeTruthy();
    expect(posts[0]?.signal?.aborted).toBe(false);
    action.resolve(Response.json({ card: { ...card, status: "pending", summary: "Run queued" } }));
    const snapshot = await runtimeCapture.current!.runCommand(ExternalContextWorkflow, {
      operation: "observe action outcome",
      safeContext: {},
      onFailure: () => {},
    }).exit;
    if (!Exit.isSuccess(snapshot)) throw new Error("Workflow unavailable");
    await waitFor(() => expect(snapshot.value.state(hostProps, "checks").pendingAction).toBeNull());
    expect(screen.queryByText("Run queued")).toBeNull();
    expect(screen.getByText("Another PR")).toBeTruthy();
    await view.rerender(hostProps);
    expect(await screen.findByText("Run queued")).toBeTruthy();
    expect(posts).toHaveLength(1);
  });

  it("keeps acknowledged results when an older read settles or a later refresh fails", async () => {
    const staleRead = Promise.withResolvers<Response>();
    let reads = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = request.url;
        if (url.endsWith("/external-context/sources"))
          return Response.json({ sources: [{ id: "checks", name: "Quality checks" }] });
        if (request.method === "POST") return Response.json({ card: { ...card, summary: "Run queued" } });
        reads++;
        if (reads === 1) return Response.json({ card });
        if (reads === 2) return staleRead.promise;
        return Response.json({ code: "upstreamError", detail: "Refresh timed out." }, { status: 504 });
      }),
    );
    render(ExternalContextCards, { props });
    await screen.findByText("A slower result");
    const read = runtimeCapture.current!.runCommand(
      Effect.gen(function* () {
        yield* (yield* ExternalContextWorkflow).read(props, "checks", true);
      }),
      { operation: "refresh external context", safeContext: {}, onFailure: () => {} },
    );
    await waitFor(() => expect(reads).toBe(2));
    await fireEvent.click(screen.getByRole("button", { name: "Run checks" }));
    expect(await screen.findByText("Run queued")).toBeTruthy();
    staleRead.resolve(Response.json({ card: { ...card, summary: "Obsolete result" } }));
    await read.exit;
    expect(screen.queryByText("Obsolete result")).toBeNull();
    expect(screen.getByText("Run queued")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Refresh Quality checks" }));
    expect((await screen.findByRole("alert")).textContent).toBe("Refresh timed out.");
    expect(screen.getByText("Run queued")).toBeTruthy();
  });

  it("shows a failed action with Refresh and never retries the submission", async () => {
    const posts = vi.fn();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = request.url;
        if (url.endsWith("/external-context/sources"))
          return Response.json({ sources: [{ id: "checks", name: "Quality checks" }] });
        if (request.method === "POST") {
          posts();
          throw new TypeError("Connection closed");
        }
        return Response.json({ card });
      }),
    );
    render(ExternalContextCards, { props });
    await fireEvent.click(await screen.findByRole("button", { name: "Run checks" }));
    expect((await screen.findByRole("alert")).textContent).toContain(
      "The action may have been submitted. Refresh to check its status.",
    );
    expect((screen.getByRole("button", { name: "Run checks" }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(screen.getByRole("button", { name: "Refresh Quality checks" }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect((screen.getByRole("button", { name: "Run checks" }) as HTMLButtonElement).disabled).toBe(false);
    expect(posts).toHaveBeenCalledTimes(1);
  });

  it("collects text for an action that asks for input and keeps it until the action succeeds", async () => {
    const bodies: unknown[] = [];
    const noteCard = {
      ...card,
      actions: [{ id: "constructor", label: "Add note", input: { placeholder: "Leave a note" } }],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = request.url;
        if (url.endsWith("/external-context/sources"))
          return Response.json({ sources: [{ id: "checks", name: "Quality checks" }] });
        if (request.method === "POST") {
          bodies.push(await request.json());
          if (bodies.length === 1)
            return Response.json({ code: "upstreamError", detail: "Adapter rejected it." }, { status: 502 });
          return Response.json({ card: { ...noteCard, summary: "Note saved" } });
        }
        return Response.json({ card: noteCard });
      }),
    );
    const view = render(ExternalContextCards, { props });
    const textbox = () => screen.getByRole("textbox", { name: "Add note text" }) as HTMLTextAreaElement;
    const button = () => screen.getByRole("button", { name: "Add note" }) as HTMLButtonElement;
    await screen.findByRole("textbox", { name: "Add note text" });
    expect(textbox().placeholder).toBe("Leave a note");
    expect(button().disabled).toBe(true);
    await fireEvent.input(textbox(), { target: { value: "  " } });
    expect(button().disabled).toBe(true);
    await fireEvent.input(textbox(), { target: { value: "Ship after review" } });
    const newHead = { ...props, headSha: "c".repeat(40) };
    await view.rerender(newHead);
    await waitFor(() => expect(textbox().value).toBe("Ship after review"));
    await fireEvent.click(button());
    expect((await screen.findByRole("alert")).textContent).toBe("Adapter rejected it.");
    expect(textbox().value).toBe("Ship after review");
    await fireEvent.click(screen.getByRole("button", { name: "Refresh Quality checks" }));
    await waitFor(() => expect(button().disabled).toBe(false));
    await fireEvent.click(button());
    expect(await screen.findByText("Note saved")).toBeTruthy();
    expect(textbox().value).toBe("");
    expect(bodies).toEqual([
      { platform_repo_id: 123, head_sha: "c".repeat(40), input: "Ship after review" },
      { platform_repo_id: 123, head_sha: "c".repeat(40), input: "Ship after review" },
    ]);
  });

  it("reloads sources after configuration invalidation and reads again for a new head", async () => {
    let configured = false;
    let reads = 0;
    const emptySources = Response.json({ sources: [] });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = request.url;
        if (url.endsWith("/external-context/sources"))
          return configured ? Response.json({ sources: [{ id: "checks", name: "Quality checks" }] }) : emptySources;
        reads++;
        return Response.json({ card: { ...card, result_head_sha: "a".repeat(40) } });
      }),
    );
    const view = render(ExternalContextCards, { props });
    await waitFor(() => expect(emptySources.bodyUsed).toBe(true));
    const runtime = runtimeCapture.current!;
    await runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* ExternalContextWorkflow;
        configured = true;
        yield* workflow.invalidate;
      }),
      { operation: "test config reload", safeContext: {}, onFailure: () => {} },
    ).exit;
    expect(await screen.findByText("A slower result")).toBeTruthy();
    expect(screen.queryByText("Results are for an older commit")).toBeNull();
    await view.rerender({ ...props, headSha: "c".repeat(40) });
    expect(await screen.findByText("Results are for an older commit")).toBeTruthy();
    expect(reads).toBe(2);
  });

  it("pauses requested polling while hidden and cancels reads on unmount", async () => {
    let visible = true;
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => (visible ? "visible" : "hidden"));
    const pendingRead = Promise.withResolvers<Response>();
    const readSignals: (AbortSignal | null | undefined)[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const url = request.url;
        if (url.endsWith("/external-context/sources"))
          return Response.json({ sources: [{ id: "checks", name: "Quality checks" }] });
        readSignals.push(request.signal);
        return readSignals.length === 1
          ? Response.json({ card: { ...card, refresh_after_seconds: 1 } })
          : pendingRead.promise;
      }),
    );
    vi.useFakeTimers();
    const view = render(ExternalContextCards, { props });
    await vi.waitFor(() => expect(screen.getByText("A slower result")).toBeTruthy());
    await vi.advanceTimersByTimeAsync(4_000);
    expect(readSignals).toHaveLength(1);
    visible = false;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(10_000);
    expect(readSignals).toHaveLength(1);
    visible = true;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.waitFor(() => expect(readSignals).toHaveLength(2));
    view.unmount();
    expect(readSignals[1]?.aborted).toBe(true);
  });

  it("polls each source on its own interval", async () => {
    const reads = { fast: 0, slow: 0 };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.endsWith("/external-context/sources"))
          return Response.json({
            sources: [
              { id: "fast", name: "Fast checks" },
              { id: "slow", name: "Slow checks" },
            ],
          });
        const source = request.url.includes("/external-context/fast") ? "fast" : "slow";
        reads[source]++;
        return Response.json({
          card: { ...card, summary: `${source} result`, refresh_after_seconds: source === "fast" ? 5 : 10 },
        });
      }),
    );
    vi.useFakeTimers();
    render(ExternalContextCards, { props });
    await vi.waitFor(() => expect(screen.getByText("slow result")).toBeTruthy());
    await vi.advanceTimersByTimeAsync(5_000);
    expect(reads.fast).toBe(2);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(reads.slow).toBe(2);
  });
});
