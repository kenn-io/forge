import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Issue, PullRequest } from "../../api/types.js";
import { createMockApiFetch, mockIssueFixture, mockPullFixture } from "../../../test/mockApiFetch.js";
import { makeGeneratedClient } from "../../testing/generated-client.js";
import type { ListItemTarget } from "../../stores/item-actions.js";
import ItemActionsMenu from "./ItemActionsMenuTestHarness.svelte";

const pullOnGitHub = mockPullFixture(42) as unknown as PullRequest;
const pullOnMirror = mockPullFixture(84) as unknown as PullRequest;
const issue = mockIssueFixture(7) as unknown as Issue;

const mixed: ListItemTarget[] = [
  { kind: "pull", item: pullOnGitHub },
  { kind: "issue", item: issue },
  { kind: "pull", item: pullOnMirror },
];

describe("ItemActionsMenu with a mixed selection", () => {
  const originalFetch = globalThis.fetch;
  const setKanbanState = vi.fn();
  const setKanbanStateOnHost = vi.fn();

  beforeEach(() => {
    globalThis.fetch = createMockApiFetch().fetch;
    setKanbanState.mockReset().mockResolvedValue({});
    setKanbanStateOnHost.mockReset().mockResolvedValue({});
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
  });

  function renderMenu(targets: readonly ListItemTarget[]) {
    return render(ItemActionsMenu, {
      props: {
        client: makeGeneratedClient({ PullRequestsService: { setKanbanState, setKanbanStateOnHost } }),
        anchor: { x: 10, y: 10 },
        returnFocus: undefined,
        targets,
        selectionActive: true,
        onClearSelection: () => {},
        onChanged: () => {},
      },
    });
  }

  it("counts items rather than calling them all pull requests", () => {
    renderMenu(mixed);

    expect(screen.getByRole("menu", { name: "Actions for 3 items" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Star 3 items" })).toBeTruthy();
  });

  it("sets workflow status only on the pull requests in the selection", async () => {
    renderMenu(mixed);

    expect(screen.getByRole("group", { name: "Workflow status for 2 PRs" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("menuitemradio", { name: "Waiting" }));

    await waitFor(() => expect(setKanbanState.mock.calls.length + setKanbanStateOnHost.mock.calls.length).toBe(2));
    const numbers = [...setKanbanState.mock.calls, ...setKanbanStateOnHost.mock.calls].map((call) => call[0].number);
    expect(numbers.sort()).toEqual([42, 84]);
    expect(setKanbanState.mock.calls[0]?.[1]).toEqual({ status: "waiting" });
  });

  it("offers no workflow status for issues alone", () => {
    renderMenu([mixed[1]!]);

    expect(screen.queryByRole("menuitemradio", { name: "Waiting" })).toBeNull();
  });
});
