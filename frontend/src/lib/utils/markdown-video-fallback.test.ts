import { afterEach, describe, expect, it } from "vite-plus/test";
import { renderMarkdownSync, type RepoContext } from "./markdown.js";
import { initMarkdownVideoFallback } from "./markdown-video-fallback.js";

const githubRepo: RepoContext = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
};
const giteaRepo: RepoContext = {
  provider: "gitea",
  platformHost: "gitea.example.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
};
const attachment = "https://github.com/user-attachments/assets/a1";

let cleanup: (() => void) | undefined;

afterEach(() => {
  cleanup?.();
  cleanup = undefined;
  document.body.replaceChildren();
});

function mount(html: string): HTMLElement {
  const body = document.createElement("div");
  body.className = "markdown-body";
  body.innerHTML = html;
  document.body.append(body);
  cleanup = initMarkdownVideoFallback(document);
  return body;
}

describe("markdown video fallback", () => {
  it("replaces a player that cannot load with a link to the original attachment", () => {
    const body = mount(renderMarkdownSync(attachment, githubRepo, { mediaOutcomes: new Map([[attachment, "video"]]) }));

    body.querySelector("video")!.dispatchEvent(new Event("error"));

    expect(body.querySelector("video")).toBeNull();
    expect(body.textContent).toContain("Video could not be loaded.");
    expect(body.querySelector("a")?.getAttribute("href")).toBe(attachment);
  });

  it("links a direct attachment to its own address", () => {
    const body = mount(renderMarkdownSync('<video src="attachments/u1" controls></video>', giteaRepo));

    body.querySelector("video")!.dispatchEvent(new Event("error"));

    expect(body.querySelector("a")?.getAttribute("href")).toBe("https://gitea.example.com/acme/widgets/attachments/u1");
  });

  it("gives up on a player only after its last source fails", () => {
    const body = mount(
      renderMarkdownSync(
        '<video controls><source src="attachments/a.webm"><source src="attachments/b.mp4"></video>',
        giteaRepo,
      ),
    );
    const [first, last] = body.querySelectorAll("source");

    first!.dispatchEvent(new Event("error"));
    expect(body.querySelector("video")).not.toBeNull();

    last!.dispatchEvent(new Event("error"));
    expect(body.querySelector("video")).toBeNull();
    expect(body.querySelector("a")?.getAttribute("href")).toBe(
      "https://gitea.example.com/acme/widgets/attachments/b.mp4",
    );
  });

  it("leaves videos outside rendered markdown alone", () => {
    cleanup = initMarkdownVideoFallback(document);
    const video = document.createElement("video");
    document.body.append(video);

    video.dispatchEvent(new Event("error"));

    expect(video.isConnected).toBe(true);
  });
});
