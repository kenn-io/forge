import { describe, expect, it, vi } from "vite-plus/test";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { PullRequest } from "../../../../../packages/ui/src/api/types.js";
import KanbanCard from "../../../../../packages/ui/src/components/kanban/KanbanCard.svelte";

const pull = {
  Number: 17,
  Title: "Preserve nested drag behavior",
  Author: "alice",
  LastActivityAt: "2026-07-15T12:00:00Z",
  repo: {
    provider: "gitlab",
    platform_host: "gitlab.example.com",
    owner: "group/subgroup",
    name: "project",
    repo_path: "group/subgroup/project",
  },
} as PullRequest;

describe("KanbanCard (browser)", () => {
  it("keeps the nested card draggable and exposes the drag cursor", () => {
    const { container } = render(KanbanCard, { props: { pr: pull, onclick: vi.fn() } });
    const card = container.querySelector<HTMLElement>(".kanban-card");
    expect(card).not.toBeNull();
    expect(getComputedStyle(card!).cursor).toBe("grab");

    const transfer = new DataTransfer();
    card!.dispatchEvent(new DragEvent("dragstart", { bubbles: true, dataTransfer: transfer }));

    expect(JSON.parse(transfer.getData("text/plain"))).toEqual({
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group/subgroup",
      name: "project",
      repoPath: "group/subgroup/project",
      number: 17,
    });
  });
});
