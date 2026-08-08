import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY } from "../../context.js";
import StackStatus from "./StackStatus.svelte";

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

function renderStackStatus(initialStack: StackContext | null) {
  return render(StackStatus, {
    props: {
      ...baseProps,
      initialStack,
    },
    context: new Map<symbol, unknown>([
      [
        API_CLIENT_KEY,
        {
          GET: vi.fn(async () => ({ data: null })),
        },
      ],
      [STORES_KEY, {}],
      [NAVIGATE_KEY, vi.fn()],
    ]),
  });
}

describe("StackStatus", () => {
  afterEach(() => {
    cleanup();
  });

  it("replaces cached stack data with the latest detail stack", async () => {
    const rendered = renderStackStatus(stack());

    expect(
      screen.getByRole("button", {
        name: /Stacked: 2\/3, 1 downstack CI failure/i,
      }),
    ).toBeTruthy();

    await rendered.rerender({
      ...baseProps,
      initialStack: stack({
        size: 2,
        health: "healthy",
        members: [member({ number: 1, position: 1 }), member({ number: 2, position: 2 })],
      }),
    });

    expect(screen.getByRole("button", { name: /Stacked: 2\/2/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Stacked: 2\/3/i })).toBeNull();
    expect(screen.getByText(/2 PRs . current 2\/2/)).toBeTruthy();
  });

  it("clears cached stack data when the latest detail has no stack", async () => {
    const rendered = renderStackStatus(stack());

    expect(screen.getByRole("button", { name: /Stacked: 2\/3/i })).toBeTruthy();

    await rendered.rerender({
      ...baseProps,
      initialStack: null,
    });

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
