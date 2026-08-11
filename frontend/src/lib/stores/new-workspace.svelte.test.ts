import { beforeEach, describe, expect, it } from "vite-plus/test";

import {
  closeNewWorkspaceDialog,
  getNewWorkspaceSource,
  getNewWorkspaceSeedRepo,
  isNewWorkspaceDialogOpen,
  openNewWorkspaceDialog,
  resetNewWorkspaceDialogState,
} from "./new-workspace.svelte.js";

describe("new workspace dialog state", () => {
  beforeEach(() => resetNewWorkspaceDialogState());

  it("opens on repository work by default and clears the seed on close", () => {
    const repo = { provider: "github", platformHost: "github.com", owner: "acme", name: "widgets" };
    openNewWorkspaceDialog(repo);

    expect(isNewWorkspaceDialogOpen()).toBe(true);
    expect(getNewWorkspaceSource()).toBe("repository");
    expect(getNewWorkspaceSeedRepo()).toEqual(repo);

    closeNewWorkspaceDialog();
    expect(isNewWorkspaceDialogOpen()).toBe(false);
    expect(getNewWorkspaceSeedRepo()).toBeNull();
  });

  it("can open directly on Kata issue search without retaining a repository seed", () => {
    openNewWorkspaceDialog(undefined, "kata_issue");

    expect(isNewWorkspaceDialogOpen()).toBe(true);
    expect(getNewWorkspaceSource()).toBe("kata_issue");
    expect(getNewWorkspaceSeedRepo()).toBeNull();
  });
});
