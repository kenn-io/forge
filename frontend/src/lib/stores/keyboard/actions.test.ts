import { describe, expect, it } from "vitest";

import { defaultActions, setStoreInstances } from "./actions.js";
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

  it("palette.open binds Cmd/Ctrl+K and Cmd/Ctrl+P", () => {
    const palette = defaultActions.find((a) => a.id === "palette.open");
    expect(palette).toBeDefined();
    expect(palette!.binding).toEqual([
      { key: "k", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true },
    ]);
  });

  it("cheatsheet.open binds ? with shift so the dispatcher matches the real keystroke", () => {
    // `?` is Shift+/ on a US keyboard. The dispatcher's matcher treats
    // omitted `shift` as `false`, so without an explicit `shift: true`
    // a real `?` press (event.shiftKey === true) would never fire the
    // action — Playwright's keyboard.press synthesizes the char and hides
    // this in e2e tests.
    const cheatsheet = defaultActions.find((a) => a.id === "cheatsheet.open");
    expect(cheatsheet).toBeDefined();
    expect(cheatsheet!.binding).toEqual({ key: "?", shift: true });
  });

  it("dispatches Edit labels from PR detail context", () => {
    const action = defaultActions.find((a) => a.id === "labels.edit");
    expect(action).toBeDefined();
    setStoreInstances(() => ({
      detail: {
        getDetail: () => ({ repo_owner: "octo", repo_name: "repo", repo, merge_request: { Number: 1 } }),
      },
    } as never));
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
    setStoreInstances(() => ({
      issues: {
        getIssueDetail: () => ({ repo_owner: "octo", repo_name: "repo", repo, issue: { Number: 1 } }),
      },
    } as never));
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
});
