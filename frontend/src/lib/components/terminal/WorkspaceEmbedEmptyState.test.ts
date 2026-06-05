import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import WorkspaceEmbedEmptyState from "./WorkspaceEmbedEmptyState.svelte";

describe("WorkspaceEmbedEmptyState", () => {
  afterEach(() => cleanup());

  it("renders the noSelection message", () => {
    render(WorkspaceEmbedEmptyState, {
      props: { reason: "noSelection" },
    });
    expect(screen.getByText("Select a workspace from the sidebar")).toBeTruthy();
  });

  it("renders the noRepo message", () => {
    render(WorkspaceEmbedEmptyState, {
      props: { reason: "noRepo" },
    });
    expect(screen.getByText("Select a repository to see workspaces")).toBeTruthy();
  });

  it("renders the noWorkspace message", () => {
    render(WorkspaceEmbedEmptyState, {
      props: { reason: "noWorkspace" },
    });
    expect(screen.getByText("No workspace for this item yet")).toBeTruthy();
  });
});
