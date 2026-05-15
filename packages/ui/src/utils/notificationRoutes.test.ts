import { describe, expect, it } from "vitest";
import type { NotificationItem } from "../api/types.js";
import { notificationDestination } from "./notificationRoutes.js";

function notification(overrides: Partial<NotificationItem>): NotificationItem {
  return {
    id: 1,
    platform_host: "github.com",
    platform_thread_id: "thread-1",
    provider: "github",
    repo_path: "acme/widget",
    repo_owner: "acme",
    repo_name: "widget",
    subject_type: "PullRequest",
    subject_title: "Review requested",
    subject_url: "",
    subject_latest_comment_url: "",
    web_url: "https://github.com/acme/widget/pull/1",
    item_number: 1,
    item_type: "pr",
    item_author: "octocat",
    reason: "review_requested",
    unread: true,
    participating: true,
    github_updated_at: "2026-05-01T10:00:00Z",
    github_last_read_at: "",
    done_at: "",
    done_reason: "",
    github_read_queued_at: "",
    github_read_synced_at: "",
    github_read_error: "",
    github_read_attempts: 0,
    github_read_last_attempt_at: "",
    github_read_next_attempt_at: "",
    ...overrides,
  };
}

describe("notificationDestination", () => {
  it("routes GitHub PR notifications through provider-shaped detail URLs", () => {
    expect(notificationDestination(notification({}))).toEqual({
      kind: "internal",
      path: "/pulls/github/acme/widget/1",
    });
  });

  it("routes enterprise PR notifications through provider-shaped detail URLs", () => {
    expect(notificationDestination(notification({
      platform_host: "ghe.example.com",
      web_url: "https://ghe.example.com/acme/widget/pull/1",
    }))).toEqual({
      kind: "internal",
      path: "/host/ghe.example.com/pulls/github/acme/widget/1",
    });
  });

  it("routes enterprise issue notifications through provider-shaped detail URLs", () => {
    expect(notificationDestination(notification({
      platform_host: "ghe.example.com",
      web_url: "https://ghe.example.com/acme/widget/issues/2",
      item_number: 2,
      item_type: "issue",
    }))).toEqual({
      kind: "internal",
      path: "/host/ghe.example.com/issues/github/acme/widget/2",
    });
  });

  it("does not guess provider when notification provider is blank", () => {
    expect(notificationDestination(notification({
      provider: "",
      web_url: "https://github.com/acme/widget/pull/1",
    }))).toEqual({
      kind: "external",
      url: "https://github.com/acme/widget/pull/1",
    });
  });
});
