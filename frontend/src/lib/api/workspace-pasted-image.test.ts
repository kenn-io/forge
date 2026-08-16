import { Effect } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";

import { makeWorkspacePastedImageUploader } from "./workspace-pasted-image.js";

describe("workspace pasted image upload", () => {
  it("posts base64 JSON to the encoded fleet workspace target", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(
          JSON.stringify({
            path: ".kenn-forge/pasted-images/paste-abcdef0123456789abcdef0123456789.png",
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
    );
    const upload = makeWorkspacePastedImageUploader(fetchMock);

    const path = await Effect.runPromise(
      upload(
        { workspaceId: "ws/a", hostKey: "host one" },
        new File([new Uint8Array([0, 255, 1])], "shot.png", { type: "text/plain" }),
      ),
    );

    expect(path).toBe(".kenn-forge/pasted-images/paste-abcdef0123456789abcdef0123456789.png");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const request = fetchMock.mock.calls[0]![0] as Request;
    expect(request.url).toBe(`${window.location.origin}/api/v1/fleet/hosts/host%20one/workspaces/ws%2Fa/pasted-images`);
    expect(request.method).toBe("POST");
    expect(request.headers.get("Content-Type")).toBe("application/json");
    expect(request.headers.get("X-Kenn-Forge-Csrf")).toBe("1");
    await expect(request.json()).resolves.toEqual({ data: "AP8B" });
  });

  it("uses the local workspace route when no fleet host is present", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(
          JSON.stringify({
            path: ".kenn-forge/pasted-images/paste-0123456789abcdef0123456789abcdef.png",
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
    );
    const upload = makeWorkspacePastedImageUploader(fetchMock);

    await Effect.runPromise(upload({ workspaceId: "ws local" }, new File(["x"], "shot.png")));

    const request = fetchMock.mock.calls[0]![0] as Request;
    expect(request.url).toBe(`${window.location.origin}/api/v1/workspaces/ws%20local/pasted-images`);
  });

  it("returns typed failures for problem responses and invalid success payloads", async () => {
    const problemFetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ code: "validationError", detail: "unsupported image" }), {
          status: 400,
          headers: { "Content-Type": "application/problem+json" },
        }),
    );
    const invalidFetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ path: "$(touch /tmp/not-a-pasted-image)" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );

    await expect(
      Effect.runPromise(
        makeWorkspacePastedImageUploader(problemFetch)({ workspaceId: "ws-1" }, new File(["x"], "shot.png")),
      ),
    ).rejects.toMatchObject({ _tag: "WorkspacePastedImageUploadError", message: "unsupported image" });
    await expect(
      Effect.runPromise(
        makeWorkspacePastedImageUploader(invalidFetch)({ workspaceId: "ws-1" }, new File(["x"], "shot.png")),
      ),
    ).rejects.toMatchObject({ _tag: "WorkspacePastedImageUploadError" });
  });
});
