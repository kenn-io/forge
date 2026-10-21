import { afterEach, expect, it, vi } from "vite-plus/test";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";

let app: MountedBrowserApp | undefined;
afterEach(async () => {
  app?.unmount();
  app = undefined;
  await resetKeyboardModuleState();
});

it("posts a failed PR view to the daemon telemetry endpoint", async () => {
  const receipt = Promise.withResolvers<string>();
  const initialReceipt = new Response(
    new ReadableStream({
      async start(controller) {
        controller.enqueue(new TextEncoder().encode(await receipt.promise));
        controller.close();
      },
    }),
    { status: 202 },
  );
  app = await mountBrowserApp("/pulls/github/acme/widgets/42", {
    overrides: [
      (request) =>
        request.url.pathname.endsWith("/telemetry/events")
          ? JSON.parse(request.bodyText).event === "app_opened"
            ? initialReceipt
            : Response.json({ status: "queued" }, { status: 202 })
          : request.url.pathname === "/api/v1/pulls/github/acme/widgets/42"
            ? Response.json({ code: "forbidden", title: "PR unavailable", status: 403 }, { status: 403 })
            : undefined,
    ],
  });
  await vi.waitFor(() => expect(app?.target.textContent).toContain("PR unavailable"));
  expect(app.api.requests.filter((request) => request.bodyText.includes("error_shown"))).toHaveLength(0);
  receipt.resolve(JSON.stringify({ status: "queued" }));
  await vi.waitFor(() => {
    const outcomes = app?.api.requests.filter(
      (request) =>
        request.method === "POST" &&
        request.url.pathname === "/api/v1/telemetry/events" &&
        JSON.parse(request.bodyText).event === "error_shown",
    );
    expect(outcomes).toHaveLength(1);
    expect(JSON.parse(outcomes![0]!.bodyText)).toEqual({
      event: "error_shown",
      properties: { surface: "web", kind: "pr_viewed" },
    });
  });
});
