import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { MessageLinkRef } from "../../messages/types";
import IssueMessageLinks from "./IssueMessageLinks.svelte";

function fakeLink(overrides: Partial<MessageLinkRef> = {}): MessageLinkRef {
  return {
    message_id: 1001,
    conversation_id: 1001,
    subject: "Project sync",
    from: "alice@example.com",
    sent_at: "2026-05-15T09:00:00Z",
    added_at: "2026-05-18T00:00:00Z",
    ...overrides,
  };
}

afterEach(cleanup);

describe("IssueMessageLinks", () => {
  it("renders nothing for an empty link list", () => {
    render(IssueMessageLinks, {
      props: {
        links: [],
        onUnlink: vi.fn(),
      },
    });

    expect(screen.queryByRole("region", { name: "Linked messages" })).toBeNull();
    expect(screen.queryByText(/^Messages$/)).toBeNull();
  });

  it("renders one pill with subject, sender, and absolute date", () => {
    render(IssueMessageLinks, {
      props: {
        links: [
          fakeLink({
            subject: "Quarterly review",
            from: "bob@example.com",
            sent_at: "2026-01-15T09:00:00Z",
          }),
        ],
        onOpenMessage: vi.fn(),
        onUnlink: vi.fn(),
      },
    });

    expect(screen.getByRole("region", { name: "Linked messages" })).toBeTruthy();
    expect(screen.getByText("Quarterly review")).toBeTruthy();
    expect(screen.getByText("bob@example.com")).toBeTruthy();
    expect(screen.getByText(/2026/)).toBeTruthy();
  });

  it("renders a fallback label for linked messages without a subject", () => {
    render(IssueMessageLinks, {
      props: {
        links: [fakeLink({ subject: "" })],
        onOpenMessage: vi.fn(),
        onUnlink: vi.fn(),
      },
    });

    expect(screen.getByTitle("Open alice@example.com - (no subject)")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Unlink (no subject)" })).toBeTruthy();
  });

  it("formats date-only sent_at values as the named local calendar day", () => {
    render(IssueMessageLinks, {
      props: {
        links: [
          fakeLink({
            subject: "Date only update",
            sent_at: "2026-05-15",
          }),
        ],
        onOpenMessage: vi.fn(),
        onUnlink: vi.fn(),
      },
    });

    expect(screen.getByText("May 15, 2026")).toBeTruthy();
  });

  it("disables the open action when no Messages navigation callback is provided", () => {
    render(IssueMessageLinks, {
      props: {
        links: [fakeLink()],
        onUnlink: vi.fn(),
      },
    });

    const open = screen.getByTitle("Messages mode is not enabled.");
    expect(open).toHaveProperty("disabled", true);
  });

  it("clicking the open action emits the selected link", async () => {
    const link = fakeLink();
    const onOpenMessage = vi.fn();
    render(IssueMessageLinks, {
      props: {
        links: [link],
        onOpenMessage,
        onUnlink: vi.fn(),
      },
    });

    const open = screen.getByTitle("Open alice@example.com - Project sync");
    expect(open).toHaveProperty("disabled", false);
    await fireEvent.click(open);

    expect(onOpenMessage).toHaveBeenCalledTimes(1);
    expect(onOpenMessage).toHaveBeenCalledWith(link);
  });

  it("clicking unlink emits the selected link", async () => {
    const link = fakeLink({ subject: "Standup recap" });
    const onUnlink = vi.fn();
    render(IssueMessageLinks, {
      props: {
        links: [link],
        onOpenMessage: vi.fn(),
        onUnlink,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Unlink Standup recap" }));

    expect(onUnlink).toHaveBeenCalledTimes(1);
    expect(onUnlink).toHaveBeenCalledWith(link);
  });

  it("busyIds covering the message disables both buttons", () => {
    const link = fakeLink();
    render(IssueMessageLinks, {
      props: {
        links: [link],
        busyIds: new Set([link.message_id]),
        onOpenMessage: vi.fn(),
        onUnlink: vi.fn(),
      },
    });

    const open = screen.getByTitle("Open alice@example.com - Project sync");
    const unlink = screen.getByRole("button", { name: "Unlink Project sync" });
    expect(open).toHaveProperty("disabled", true);
    expect(unlink).toHaveProperty("disabled", true);
  });
});
