import { expect, test } from "@playwright/test";
import {
  createLiveKataHarness,
  configureMiddlemanKataCatalog,
  configureMiddlemanKataHome,
  type LiveKataHarness,
  type MiddlemanKataHome,
} from "./support/kataLiveHarness";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

test.describe("kata live daemon integration", () => {
  test.skip(process.env.MIDDLEMAN_LIVE_KATA_TESTS !== "1", "Set MIDDLEMAN_LIVE_KATA_TESTS=1 to run live Kata e2e.");

  test("proxies live daemon route capabilities through middleman", async () => {
    const harness = await createLiveKataHarness();
    const kataHome = await configureMiddlemanKataHome(harness.baseURL);
    const server = await startIsolatedE2EServer();

    try {
      const seeded = await harness.seedIssue({
        projectName: "Middleman Route Probes",
        issueTitle: "Probe middleman proxy routes",
        issueBody: "Created to verify live route passthrough.",
      });
      const recurrence = await harness.rawPost(`/api/v1/projects/${seeded.project.id}/recurrences`, {
        actor: "middleman-e2e",
        rrule: "FREQ=WEEKLY;COUNT=2",
        dtstart: "2026-06-10",
        timezone: "America/Chicago",
        template: {
          title: "Weekly proxy review",
          body: "Review proxied recurrence route.",
          labels: ["routine"],
          metadata: { checklist: [] },
        },
      });
      expect(recurrence.status, `Recurrence create failed: ${recurrence.text}`).toBe(201);

      const instance = await proxyJSON(server, "/api/v1/instance");
      expect(instance.status).toBe(200);
      expect(instance.body).toEqual(expect.objectContaining({ schema_version: expect.any(Number) }));

      const projects = await proxyJSON(server, "/api/v1/projects?include=stats");
      expect(projects.status).toBe(200);
      expect((projects.body as { projects?: Array<{ uid?: string }> }).projects).toEqual(
        expect.arrayContaining([expect.objectContaining({ uid: seeded.project.uid })]),
      );

      const issues = await proxyJSON(server, "/api/v1/issues?status=open");
      expect(issues.status).toBe(200);
      expect((issues.body as { issues?: Array<{ uid?: string }> }).issues).toEqual(
        expect.arrayContaining([expect.objectContaining({ uid: seeded.issue.uid })]),
      );

      const events = await proxyJSON(server, "/api/v1/events?after_id=0&limit=1000");
      expect(events.status).toBe(200);
      expect(events.body).toEqual(
        expect.objectContaining({ reset_required: false, next_after_id: expect.any(Number) }),
      );
      const eventCursor = (events.body as { next_after_id: number }).next_after_id;

      const recurrences = await proxyJSON(server, `/api/v1/projects/${seeded.project.id}/recurrences`);
      expect(recurrences.status).toBe(200);
      expect(
        (recurrences.body as { recurrences?: Array<{ rrule?: string; template_title?: string }> }).recurrences,
      ).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            rrule: "FREQ=WEEKLY;COUNT=2",
            template_title: "Weekly proxy review",
          }),
        ]),
      );

      const seen = waitForProxiedIssueCreated(server, eventCursor, (event) => event.project_uid === seeded.project.uid);
      await new Promise((resolve) => setTimeout(resolve, 100));
      const created = await proxyPostJSON<{
        issue: { uid: string; short_id: string; title: string };
        changed: boolean;
      }>(
        server,
        `/api/v1/projects/${seeded.project.id}/issues`,
        {
          actor: "middleman-e2e",
          title: "Observe proxied SSE issue",
          body: "Created while middleman has a proxied event stream open.",
          force_new: true,
        },
        { "Idempotency-Key": "01MIDDLEMANPROXYSSE000001" },
      );
      expect(created.status).toBe(200);
      expect(created.body).toEqual(expect.objectContaining({ changed: true }));
      expect(created.body.issue.uid).toEqual(expect.any(String));

      const event = await seen;
      expect(event).toEqual(
        expect.objectContaining({
          event_id: expect.any(Number),
          origin_instance_uid: expect.stringMatching(/^[0-9A-HJKMNP-TV-Z]{26}$/),
          type: "issue.created",
          issue_uid: created.body.issue.uid,
          project_uid: seeded.project.uid,
        }),
      );
      expect(event.event_id).toBeGreaterThan(eventCursor);
    } finally {
      await server.stop();
      await kataHome.stop();
      await harness.stop();
    }
  });

  test("switches between real daemons from the external catalog", async ({ page }) => {
    let home: LiveKataHarness | undefined;
    let work: LiveKataHarness | undefined;
    let kataHome: MiddlemanKataHome | undefined;
    let server: IsolatedE2EServer | undefined;

    try {
      home = await createLiveKataHarness();
      work = await createLiveKataHarness();
      kataHome = await configureMiddlemanKataCatalog(
        [
          { name: "home", url: home.baseURL },
          { name: "work", url: work.baseURL },
        ],
        "home",
      );
      server = await startIsolatedE2EServer();

      await home.seedIssue({
        projectName: "Home",
        issueTitle: "Rake the yard",
        issueBody: "Visible only from the home daemon.",
      });
      await work.seedIssue({
        projectName: "Work",
        issueTitle: "Ship the release",
        issueBody: "Visible only from the work daemon.",
      });

      await page.goto(`${server.info.base_url}/kata`);

      const taskList = page.locator(".kata-list");
      await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();
      await expect(page.getByText("Connected")).toBeVisible();
      await expect(page.getByTestId("daemon-chip")).toContainText("home");
      await page.getByRole("button", { name: "All Open" }).click();
      await expect(taskList.getByRole("button", { name: /Rake the yard/ })).toBeVisible();
      await expect(taskList.getByRole("button", { name: /Ship the release/ })).toHaveCount(0);

      await page.getByTestId("daemon-chip").click();
      await page.getByTestId("daemon-row-work").click();

      await expect(page.getByTestId("daemon-chip")).toContainText("work");
      await expect(taskList.getByRole("button", { name: /Ship the release/ })).toBeVisible();
      await expect(taskList.getByRole("button", { name: /Rake the yard/ })).toHaveCount(0);
    } finally {
      await server?.stop();
      await kataHome?.stop();
      await work?.stop();
      await home?.stop();
    }
  });

  test("reads and mutates a real external daemon through middleman", async ({ page }) => {
    const harness = await createLiveKataHarness();
    const kataHome = await configureMiddlemanKataHome(harness.baseURL);
    const server = await startIsolatedE2EServer();

    try {
      const seeded = await harness.seedIssue({
        projectName: "Middleman Live",
        issueTitle: "Verify live daemon proxy",
        issueBody: "Created by the opt-in middleman live daemon e2e.",
      });

      await page.goto(`${server.info.base_url}/kata`);

      await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();
      await expect(page.getByText("Connected")).toBeVisible();
      await expect(page.getByRole("button", { name: /Verify live daemon proxy/ })).toBeVisible();
      await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
        "Created by the opt-in middleman live daemon e2e.",
      );

      const mutation = await page.evaluate(
        async ({ projectID, shortID, revision }) => {
          const response = await fetch(
            `/api/v1/kata/proxy/api/v1/projects/${projectID}/issues/${encodeURIComponent(shortID)}/metadata`,
            {
              method: "PUT",
              headers: {
                "Content-Type": "application/json",
                "If-Match": `"rev-${revision}"`,
              },
              body: JSON.stringify({
                actor: "middleman",
                patch: { deadline_on: "2026-06-12" },
              }),
            },
          );
          const text = await response.text();
          return {
            status: response.status,
            etag: response.headers.get("etag"),
            body: text ? (JSON.parse(text) as unknown) : {},
          };
        },
        {
          projectID: seeded.project.id,
          shortID: seeded.issue.short_id,
          revision: seeded.issue.revision,
        },
      );

      expect(mutation).toMatchObject({
        status: 200,
        etag: `"rev-${seeded.issue.revision + 1}"`,
        body: {
          changed: true,
          issue: {
            uid: seeded.issue.uid,
            metadata: {
              deadline_on: "2026-06-12",
            },
          },
        },
      });

      const detail = await harness.getIssue(seeded.issue.uid);
      expect(detail.issue.metadata).toMatchObject({ deadline_on: "2026-06-12" });
    } finally {
      await server.stop();
      await kataHome.stop();
      await harness.stop();
    }
  });

  test("uses catalog token for authenticated daemon proxy mutations", async ({ page }) => {
    const token = "middleman-live-kata-token";
    const harness = await createLiveKataHarness({ authToken: token });
    const kataHome = await configureMiddlemanKataHome(harness.baseURL, token);
    const server = await startIsolatedE2EServer();
    const authHeaders = { Authorization: `Bearer ${token}` };

    try {
      const ping = await harness.rawGet("/api/v1/ping");
      expect(ping.status, `Unauthenticated ping failed: ${ping.text}`).toBe(200);

      const unauthenticated = await harness.rawGet("/api/v1/instance");
      expect(
        [401, 403],
        `Unauthenticated /api/v1/instance should require auth; got ${unauthenticated.status}: ${unauthenticated.text}`,
      ).toContain(unauthenticated.status);

      const project = await harness.post<{ project: { id: number; uid: string; name: string }; created: boolean }>(
        "/api/v1/projects",
        {
          name: "Middleman Auth",
          alias: {
            identity: `local://${harness.workspaceRoot}/auth`,
            kind: "local",
            root_path: `${harness.workspaceRoot}/auth`,
          },
        },
        authHeaders,
      );
      const created = await harness.post<{
        issue: { uid: string; short_id: string; revision: number; title: string };
        changed: boolean;
      }>(
        `/api/v1/projects/${project.project.id}/issues`,
        {
          actor: "middleman-e2e",
          title: "Verify authenticated daemon proxy",
          body: "Created to verify catalog-token proxying.",
          force_new: true,
        },
        {
          ...authHeaders,
          "Idempotency-Key": "01MIDDLEMANLIVEAUTH000001",
        },
      );

      await page.goto(`${server.info.base_url}/kata`);
      await expect(page.getByText("Connected")).toBeVisible();
      await expect(page.getByRole("button", { name: /Verify authenticated daemon proxy/ })).toBeVisible();

      const mutation = await page.evaluate(
        async ({ projectID, shortID, revision }) => {
          async function patch(ifMatch: string, patch: Record<string, unknown>) {
            const response = await fetch(
              `/api/v1/kata/proxy/api/v1/projects/${projectID}/issues/${encodeURIComponent(shortID)}/metadata`,
              {
                method: "PUT",
                headers: {
                  "Content-Type": "application/json",
                  "If-Match": ifMatch,
                },
                body: JSON.stringify({
                  actor: "middleman",
                  patch,
                }),
              },
            );
            const text = await response.text();
            return {
              status: response.status,
              etag: response.headers.get("etag"),
              body: text ? (JSON.parse(text) as unknown) : {},
            };
          }

          const first = await patch(`"rev-${revision}"`, { scheduled_on: "2026-06-13" });
          const stale = await patch(`"rev-${revision}"`, { deadline_on: "2026-06-14" });
          return { first, stale };
        },
        {
          projectID: project.project.id,
          shortID: created.issue.short_id,
          revision: created.issue.revision,
        },
      );

      expect(mutation.first).toMatchObject({
        status: 200,
        etag: `"rev-${created.issue.revision + 1}"`,
        body: {
          changed: true,
          issue: {
            uid: created.issue.uid,
            metadata: {
              scheduled_on: "2026-06-13",
            },
          },
        },
      });
      expect(mutation.stale).toMatchObject({
        status: 412,
        body: {
          error: {
            code: "revision_conflict",
          },
        },
      });

      const detail = await harness.getIssue(created.issue.uid, authHeaders);
      expect(detail.issue.metadata).toMatchObject({ scheduled_on: "2026-06-13" });
    } finally {
      await server.stop();
      await kataHome.stop();
      await harness.stop();
    }
  });
});

interface ProxiedKataEvent {
  event_id: number;
  issue_uid?: string;
  origin_instance_uid?: string;
  project_uid?: string;
  type: string;
}

async function proxyJSON<T = unknown>(server: IsolatedE2EServer, route: string): Promise<{ body: T; status: number }> {
  const response = await fetch(new URL(`/api/v1/kata/proxy${route}`, server.info.base_url));
  const text = await response.text();
  return {
    body: text ? (JSON.parse(text) as T) : ({} as T),
    status: response.status,
  };
}

async function proxyPostJSON<T>(
  server: IsolatedE2EServer,
  route: string,
  body: unknown,
  headers: Record<string, string> = {},
): Promise<{ body: T; status: number }> {
  const response = await fetch(new URL(`/api/v1/kata/proxy${route}`, server.info.base_url), {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...headers,
    },
    body: JSON.stringify(body),
  });
  const text = await response.text();
  return {
    body: text ? (JSON.parse(text) as T) : ({} as T),
    status: response.status,
  };
}

function waitForProxiedIssueCreated(
  server: IsolatedE2EServer,
  lastEventID: number,
  matches: (event: ProxiedKataEvent) => boolean,
): Promise<ProxiedKataEvent> {
  const controller = new AbortController();
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      controller.abort();
      reject(new Error("timed out waiting for proxied issue.created over SSE"));
    }, 5_000);

    function settle(event: ProxiedKataEvent): void {
      clearTimeout(timeout);
      controller.abort();
      resolve(event);
    }

    void (async () => {
      const response = await fetch(new URL("/api/v1/kata/proxy/api/v1/events/stream", server.info.base_url), {
        headers: {
          Accept: "text/event-stream",
          "Last-Event-ID": String(lastEventID),
        },
        signal: controller.signal,
      });
      if (response.status !== 200) {
        throw new Error(`proxied event stream returned ${response.status}`);
      }
      const contentType = response.headers.get("content-type") ?? "";
      if (!contentType.includes("text/event-stream")) {
        throw new Error(`proxied event stream returned ${contentType || "no content type"}`);
      }

      for await (const frame of readSSEFrames(response.body)) {
        if (frame.event === "issue.created") {
          const event = JSON.parse(frame.data) as ProxiedKataEvent;
          if (matches(event)) settle(event);
        }
      }
    })().catch((error: unknown) => {
      clearTimeout(timeout);
      if (!controller.signal.aborted) reject(error);
    });
  });
}

async function* readSSEFrames(
  body: ReadableStream<Uint8Array> | null,
): AsyncGenerator<{ data: string; event: string }> {
  if (!body) throw new Error("proxied event stream response had no body");

  const decoder = new TextDecoder();
  const reader = body.getReader();
  let buffer = "";

  try {
    while (true) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value, { stream: !done });
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        const frame = parseSSEFrame(buffer.slice(0, boundary));
        buffer = buffer.slice(boundary + 2);
        if (frame) yield frame;
        boundary = buffer.indexOf("\n\n");
      }
      if (done) break;
    }
  } finally {
    reader.releaseLock();
  }
}

function parseSSEFrame(raw: string): { data: string; event: string } | undefined {
  const data: string[] = [];
  let event = "";
  for (const line of raw.split("\n")) {
    if (line.startsWith("event:")) event = line.slice("event:".length).trim();
    if (line.startsWith("data:")) data.push(line.slice("data:".length).trimStart());
  }
  if (data.length === 0) return undefined;
  return {
    data: data.join("\n"),
    event,
  };
}
