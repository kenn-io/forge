import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
import IssuePickerDialog from "./IssuePickerDialog.svelte";

describe("IssuePickerDialog (browser)", () => {
  it("keeps the selected task enabled after Typeahead closes", async () => {
    const onPick = vi.fn();
    const searchReferences: KataTaskReferenceSearch = vi.fn(async () => ({
      server_instance_id: "server-a",
      daemon_id: "home",
      generation: 7,
      invalidation_epoch: 2,
      references: [
        {
          uid: "issue-q3",
          short_id: "kat-7",
          qualified_id: "Kata#kat-7",
          reference: "kat-7",
          title: "Email Susan re: Q3",
          project_id: 7,
          project_uid: "project-kata",
          project_name: "Kata",
        },
      ],
      fetched_at: "2026-07-15T12:00:00Z",
    }));

    render(IssuePickerDialog, {
      props: {
        open: true,
        searchReferences,
        onClose: vi.fn(),
        onPick,
      },
    });

    const dialog = page.getByRole("dialog", { name: "Link to task" });
    await dialog.getByRole("button", { name: "Title or qualified ID..." }).click();
    await dialog.getByRole("combobox", { name: "Title or qualified ID..." }).fill("q3");

    const option = dialog.getByRole("option", { name: /kat-7.*Email Susan re: Q3/ });
    await expect.element(option).toBeVisible();
    await option.click();

    const link = dialog.getByRole("button", { name: "Link", exact: true });
    await expect.element(link).toBeEnabled();
    await link.click();

    expect(onPick).toHaveBeenCalledWith({
      uid: "issue-q3",
      short_id: "kat-7",
      qualified_id: "Kata#kat-7",
      reference: "kat-7",
      title: "Email Susan re: Q3",
      project_id: 7,
      project_uid: "project-kata",
      project_name: "Kata",
    });
  });
});
