import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { OwnedAppRuntime } from "../../app/runtime.js";
import type { GeneratedClient } from "../../api/generated-api.js";
import { NAVIGATE_KEY, STORES_KEY } from "../../context.js";
import { makeTestAppRuntime } from "../../testing/effect-layers.js";
import StackStatusTestHarness from "./StackStatusTestHarness.svelte";

interface StackMember {
  number: number;
  title: string;
  state: string;
  ci_status: string;
  review_decision: string;
  mergeable_state: string;
  position: number;
  is_draft: boolean;
  base_branch: string;
  blocked_by: number | null;
}

interface StackContext {
  stack_id: number;
  stack_name: string;
  position: number;
  size: number;
  health: string;
  members: StackMember[];
}

function member(overrides: Partial<StackMember> & Pick<StackMember, "number" | "position">): StackMember {
  return {
    number: overrides.number,
    title: `PR ${overrides.number}`,
    state: "open",
    ci_status: "success",
    review_decision: "APPROVED",
    mergeable_state: "",
    position: overrides.position,
    is_draft: false,
    base_branch: "main",
    blocked_by: null,
    ...overrides,
  };
}

function stack(overrides: Partial<StackContext> = {}): StackContext {
  return {
    stack_id: 1,
    stack_name: "feature-stack",
    position: 2,
    size: 3,
    health: "blocked",
    members: [
      member({ number: 1, position: 1, ci_status: "failure" }),
      member({ number: 2, position: 2, ci_status: "pending" }),
      member({ number: 3, position: 3 }),
    ],
    ...overrides,
  };
}

const baseProps = {
  owner: "acme",
  name: "widget",
  number: 2,
  provider: "github",
  platformHost: "github.com",
  repoPath: "acme/widget",
  expanded: true,
};

const runtimes = new Set<OwnedAppRuntime>();

function renderStackStatus(
  initialStack: StackContext | null,
  options: {
    readonly client?: GeneratedClient;
    readonly sync?: { readonly subscribeSyncComplete: (callback: () => void) => () => void };
  } = {},
) {
  const client = options.client ?? ({ GET: vi.fn(async () => ({ data: null })) } as unknown as GeneratedClient);
  const runtime = makeTestAppRuntime(client);
  runtimes.add(runtime);
  const rendered = render(StackStatusTestHarness, {
    props: {
      runtime,
      stackProps: {
        ...baseProps,
        initialStack,
      },
    },
    context: new Map<symbol, unknown>([
      [STORES_KEY, options.sync === undefined ? {} : { sync: options.sync }],
      [NAVIGATE_KEY, vi.fn()],
    ]),
  });
  return {
    ...rendered,
    rerenderStackStatus: (nextStack: StackContext | null) =>
      rendered.rerender({
        runtime,
        stackProps: {
          ...baseProps,
          initialStack: nextStack,
        },
      }),
  };
}

describe("StackStatus", () => {
  afterEach(async () => {
    cleanup();
    await Promise.all(Array.from(runtimes, (runtime) => Effect.runPromise(runtime.disposeEffect)));
    runtimes.clear();
  });

  it("keeps a later stack refresh authoritative when an older response finishes last", async () => {
    const first = Promise.withResolvers<{ data: StackContext; response: Response }>();
    const latest = stack({
      size: 2,
      health: "healthy",
      members: [member({ number: 1, position: 1 }), member({ number: 2, position: 2 })],
    });
    const get = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockResolvedValueOnce({ data: latest, response: new Response(null, { status: 200 }) });
    let refresh = () => undefined;
    renderStackStatus(stack(), {
      client: { GET: get } as unknown as GeneratedClient,
      sync: {
        subscribeSyncComplete: (callback) => {
          refresh = callback;
          return () => undefined;
        },
      },
    });

    refresh();
    await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
    refresh();
    await waitFor(() => expect(screen.getByRole("button", { name: /Stacked: 2\/2/i })).toBeTruthy());

    first.resolve({ data: stack(), response: new Response(null, { status: 200 }) });
    await Promise.resolve();

    expect(screen.getByRole("button", { name: /Stacked: 2\/2/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Stacked: 2\/3/i })).toBeNull();
  });

  it("replaces cached stack data with the latest detail stack", async () => {
    const rendered = renderStackStatus(stack());

    expect(
      screen.getByRole("button", {
        name: /Stacked: 2\/3, 1 downstack CI failure/i,
      }),
    ).toBeTruthy();

    await rendered.rerenderStackStatus(
      stack({
        size: 2,
        health: "healthy",
        members: [member({ number: 1, position: 1 }), member({ number: 2, position: 2 })],
      }),
    );

    expect(screen.getByRole("button", { name: /Stacked: 2\/2/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Stacked: 2\/3/i })).toBeNull();
    expect(screen.getByText(/2 PRs . current 2\/2/)).toBeTruthy();
  });

  it("clears cached stack data when the latest detail has no stack", async () => {
    const rendered = renderStackStatus(stack());

    expect(screen.getByRole("button", { name: /Stacked: 2\/3/i })).toBeTruthy();

    await rendered.rerenderStackStatus(null);

    expect(screen.queryByRole("button", { name: /Stacked:/i })).toBeNull();
  });

  it("surfaces downstack merge conflicts on the chip and stack row", () => {
    renderStackStatus(
      stack({
        members: [
          member({ number: 1, position: 1, mergeable_state: "dirty" }),
          member({ number: 2, position: 2, mergeable_state: "dirty" }),
          member({ number: 3, position: 3 }),
        ],
      }),
    );

    expect(
      screen.getByRole("button", {
        name: /Stacked: 2\/3, 1 downstack merge conflict/i,
      }),
    ).toBeTruthy();
    expect(screen.getAllByText("× Conflicts")).toHaveLength(2);
    expect(screen.getByText(/3 PRs . current 2\/3 . downstack conflict/)).toBeTruthy();
  });
});
