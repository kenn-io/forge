import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import RepoPrivateIcon from "./RepoPrivateIcon.svelte";

describe("RepoPrivateIcon", () => {
  afterEach(() => {
    cleanup();
  });

  it("names the lock for a repository the provider reported as private", () => {
    render(RepoPrivateIcon, { props: { isPrivate: true } });

    expect(screen.getByRole("img", { name: "Private repository" })).toBeTruthy();
  });

  it.each([
    ["public", false],
    ["unknown", undefined],
  ])("renders nothing for a %s repository", (_label, isPrivate) => {
    const { container } = render(RepoPrivateIcon, { props: { isPrivate } });

    expect(container.querySelector(".repo-private-icon")).toBeNull();
  });
});
