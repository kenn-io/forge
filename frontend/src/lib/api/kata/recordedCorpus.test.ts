import { describe, expect, test } from "vite-plus/test";

import eventsFixture from "./fixtures/events.json";
import instanceFixture from "./fixtures/instance.json";
import issueDetailFixture from "./fixtures/issue-detail.json";
import issuesOpenFixture from "./fixtures/issues-open.json";
import projectsFixture from "./fixtures/projects.json";
import {
  normalizeKataEvents,
  normalizeKataInstance,
  normalizeKataProjectList,
  normalizeKataTaskDetail,
  normalizeKataTaskList,
} from "./taskNormalizers.js";

describe("recorded Kata JSON corpus", () => {
  test("normalizes daemon response shapes used by web and future native clients", () => {
    expect(normalizeKataInstance(instanceFixture)).toMatchObject({
      instance_uid: expect.stringMatching(/^[0-9A-HJKMNP-TV-Z]{26}$/),
      schema_version: expect.any(Number),
    });

    expect(normalizeKataProjectList(projectsFixture).projects).toEqual([
      expect.objectContaining({
        name: "Inbox",
        metadata: expect.objectContaining({ role: "inbox" }),
        revision: expect.any(Number),
      }),
    ]);

    expect(normalizeKataTaskList(issuesOpenFixture).groups[0]?.issues[0]).toMatchObject({
      title: "Recorded capture",
      metadata: { scheduled_on: "2026-05-20" },
      labels: ["mobile", "capture"],
    });

    expect(normalizeKataTaskDetail(issueDetailFixture)).toMatchObject({
      issue: expect.objectContaining({
        body: "Captured from a recorded daemon response.",
        metadata: expect.objectContaining({
          checklist: [{ id: "01HZX4Y5Z6A7B8C9D0E1F2G3C2", text: "Review on mobile", done: false }],
        }),
      }),
      comments: [],
      links: [],
    });

    expect(normalizeKataEvents(eventsFixture).events[0]).toMatchObject({
      type: "issue.created",
      origin_instance_uid: expect.stringMatching(/^[0-9A-HJKMNP-TV-Z]{26}$/),
      issue_uid: "01HZX4Y5Z6A7B8C9D0E1F2G3J1",
    });
  });
});
