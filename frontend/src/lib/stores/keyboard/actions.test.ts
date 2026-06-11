import { afterEach, describe, expect, it } from "vite-plus/test";

import { defaultActions, setStoreInstances } from "./actions.js";
import { isSidebarCollapsed, setSidebarCollapsed } from "../sidebar.svelte.js";
import {
  OPEN_LABEL_PICKER_EVENT,
  type OpenLabelPickerDetail,
} from "../../../../../packages/ui/src/components/detail/labelPickerCommand.js";
import type { Context } from "./types.js";

function ctx(page: Context["page"], overrides: Partial<Context> = {}): Context {
  return {
    page,
    route: { page } as never,
    selectedPR: null,
    selectedIssue: null,
    isDiffView: false,
    detailTab: "conversation",
    sidebarTargetAvailable: true,
    ...overrides,
  };
}

const repo = {
  provider: "github",
  platform_host: "github.com",
  owner: "octo",
  name: "repo",
  repo_path: "octo/repo",
  capabilities: { read_labels: true, label_mutation: true },
};

const selected = {
  provider: "github",
  platformHost: "github.com",
  owner: "octo",
  name: "repo",
  repoPath: "octo/repo",
  number: 1,
};

describe("defaultActions", () => {
  afterEach(() => {
    setSidebarCollapsed(false);
  });

  it("includes the migrated globals", () => {
    const ids = defaultActions.map((a) => a.id);
    expect(ids).toEqual(
      expect.arrayContaining([
        "labels.edit",
        "go.next",
        "go.prev",
        "tab.toggle",
        "escape.list",
        "nav.pulls.list",
        "nav.pulls.board",
        "sidebar.toggle",
        "palette.open",
        "cheatsheet.open",
        "sync.repos",
        "theme.toggle",
        "nav.settings",
        "nav.repos",
        "nav.reviews",
        "nav.workspaces",
        "nav.design-system",
      ]),
    );
  });

  it("palette.open binds Cmd/Ctrl+K, Cmd/Ctrl+P, and Cmd/Ctrl+Shift+P", () => {
    const palette = defaultActions.find((a) => a.id === "palette.open");
    expect(palette).toBeDefined();
    expect(palette!.binding).toEqual([
      { key: "k", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true, shift: true },
    ]);
  });

  it("cheatsheet.open binds shifted slash variants so the dispatcher matches the real keystroke", () => {
    // Browsers disagree on whether Shift+/ arrives as `?` or `/`.
    // The dispatcher's matcher treats omitted `shift` as `false`, so both
    // variants need an explicit `shift: true`.
    const cheatsheet = defaultActions.find((a) => a.id === "cheatsheet.open");
    expect(cheatsheet).toBeDefined();
    expect(cheatsheet!.binding).toEqual([
      { key: "?", shift: true },
      { key: "/", shift: true },
    ]);
  });

  it("dispatches Edit labels from PR detail context", () => {
    const action = defaultActions.find((a) => a.id === "labels.edit");
    expect(action).toBeDefined();
    setStoreInstances(
      () =>
        ({
          detail: {
            getDetail: () => ({
              repo_owner: "octo",
              repo_name: "repo",
              repo,
              merge_request: { Number: 1 },
            }),
          },
        }) as never,
    );
    const events: OpenLabelPickerDetail[] = [];
    const listener = (event: Event) => events.push((event as CustomEvent<OpenLabelPickerDetail>).detail);
    window.addEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    try {
      const context = ctx("pulls", { selectedPR: selected });
      expect(action!.when(context)).toBe(true);
      action!.handler(context);
    } finally {
      window.removeEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    }

    expect(events).toEqual([{ itemType: "pull", ...selected }]);
  });

  it("dispatches Edit labels from issue detail context", () => {
    const action = defaultActions.find((a) => a.id === "labels.edit");
    expect(action).toBeDefined();
    setStoreInstances(
      () =>
        ({
          issues: {
            getIssueDetail: () => ({
              repo_owner: "octo",
              repo_name: "repo",
              repo,
              issue: { Number: 1 },
            }),
          },
        }) as never,
    );
    const events: OpenLabelPickerDetail[] = [];
    const listener = (event: Event) => events.push((event as CustomEvent<OpenLabelPickerDetail>).detail);
    window.addEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    try {
      const context = ctx("issues", { selectedIssue: selected });
      expect(action!.when(context)).toBe(true);
      action!.handler(context);
    } finally {
      window.removeEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    }

    expect(events).toEqual([{ itemType: "issue", ...selected }]);
  });

  it("cheatsheet.open does not fire on the reviews page (roborev owns ?)", () => {
    // Roborev's ReviewsView has its own window-level `?` handler that
    // opens a help modal. If middleman's cheatsheet also fires on `?`,
    // both modals open and the cheatsheet's filter input steals focus,
    // causing roborev's Escape handler to short-circuit on its
    // tag === "INPUT" guard. Gate the action by page to avoid that.
    const cheatsheet = defaultActions.find((a) => a.id === "cheatsheet.open");
    expect(cheatsheet).toBeDefined();
    expect(cheatsheet!.when(ctx("reviews"))).toBe(false);
    expect(cheatsheet!.when(ctx("pulls"))).toBe(true);
    expect(cheatsheet!.when(ctx("issues"))).toBe(true);
  });

  it("sidebar.toggle reserves the chord everywhere but only toggles pages with a sidebar target", () => {
    const action = defaultActions.find((a) => a.id === "sidebar.toggle");
    expect(action).toBeDefined();

    const visible = action!.visible ?? action!.when;

    expect(action!.when(ctx("activity"))).toBe(true);
    expect(visible(ctx("activity"))).toBe(false);
    setSidebarCollapsed(false);
    action!.handler(ctx("activity"));
    expect(isSidebarCollapsed()).toBe(false);

    expect(action!.when(ctx("repos"))).toBe(true);
    expect(visible(ctx("repos"))).toBe(false);
    expect(
      action!.when(
        ctx("pulls", {
          route: { page: "pulls", view: "list" } as never,
        }),
      ),
    ).toBe(true);
    expect(
      visible(
        ctx("pulls", {
          route: { page: "pulls", view: "list" } as never,
        }),
      ),
    ).toBe(true);
    action!.handler(
      ctx("pulls", {
        route: { page: "pulls", view: "list" } as never,
      }),
    );
    expect(isSidebarCollapsed()).toBe(true);
    setSidebarCollapsed(false);
    const compactPulls = ctx("pulls", {
      route: { page: "pulls", view: "list" } as never,
      sidebarTargetAvailable: false,
    });
    expect(action!.when(compactPulls)).toBe(true);
    expect(visible(compactPulls)).toBe(false);
    action!.handler(compactPulls);
    expect(isSidebarCollapsed()).toBe(false);
    expect(
      action!.when(
        ctx("pulls", {
          route: { page: "pulls", view: "board" } as never,
        }),
      ),
    ).toBe(true);
    expect(
      visible(
        ctx("pulls", {
          route: { page: "pulls", view: "board" } as never,
        }),
      ),
    ).toBe(false);
    expect(action!.when(ctx("issues"))).toBe(true);
    expect(visible(ctx("issues"))).toBe(true);
    expect(action!.when(ctx("workspaces"))).toBe(true);
    expect(visible(ctx("workspaces"))).toBe(true);
    expect(
      action!.when(
        ctx("terminal", {
          route: { page: "terminal", workspaceId: "1" } as never,
        }),
      ),
    ).toBe(true);
    expect(
      visible(
        ctx("terminal", {
          route: { page: "terminal", workspaceId: "1" } as never,
        }),
      ),
    ).toBe(true);
  });

  it("does not enable pull request number navigation on Kata", () => {
    const list = defaultActions.find((a) => a.id === "nav.pulls.list");
    const board = defaultActions.find((a) => a.id === "nav.pulls.board");

    expect(list).toBeDefined();
    expect(board).toBeDefined();
    expect(list!.when(ctx("kata"))).toBe(false);
    expect(board!.when(ctx("kata"))).toBe(false);
    expect(list!.when(ctx("pulls"))).toBe(true);
    expect(board!.when(ctx("pulls"))).toBe(true);
  });
});
