import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  API_CLIENT_KEY,
  STORES_KEY,
} from "../../../../packages/ui/src/context.js";
import CommentBox from "../../../../packages/ui/src/components/detail/CommentBox.svelte";
import IssueCommentBox from "../../../../packages/ui/src/components/detail/IssueCommentBox.svelte";
import {
  clearCommentSubmitError,
  finishCommentSubmit,
  getCommentDraft,
  getCommentSubmitError,
  isCommentSubmitPending,
  setCommentDraft,
} from "../../../../packages/ui/src/components/detail/comment-drafts.svelte.js";
import CommentBoxContextHarness from "./CommentBoxContextHarness.svelte";

interface AutocompleteResponse {
  users: string[];
  references: Array<{
    kind: string;
    number: number;
    title: string;
    state: string;
  }>;
}

function mockAutocompleteClient(
  response: AutocompleteResponse = { users: [], references: [] },
) {
  return {
    GET: async (path: string) => {
      if (path === "/repo/{provider}/{owner}/{name}/comment-autocomplete") {
        return { data: response };
      }
      return { data: undefined, error: { title: "not mocked" } };
    },
  };
}

function getCommentEditor(): HTMLElement {
  const editor = document.querySelector(".comment-editor-input");
  if (!(editor instanceof HTMLElement)) {
    throw new Error("comment editor not found");
  }
  return editor;
}

function getCommentEditorText(): string {
  return getCommentEditor().textContent ?? "";
}

function isCommentEditorDisabled(): boolean {
  return getCommentEditor().getAttribute("contenteditable") === "false";
}

async function waitForCommentButtonEnabled(name = "Comment"): Promise<void> {
  await waitFor(() => {
    expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(false);
  });
}

function deferred(): {
  promise: Promise<void>;
  resolve: () => void;
} {
  let resolve = () => {};
  const promise = new Promise<void>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function deferredByNumber(numbers: number[]): Map<number, ReturnType<typeof deferred>> {
  return new Map(numbers.map((number) => [number, deferred()]));
}

function renderPullCommentBox(owner = "octo", name = "repo", number = 1) {
  return render(CommentBox, {
    props: { provider: "github", platformHost: "github.com", owner, name, repoPath: `${owner}/${name}`, number },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, mockAutocompleteClient()],
      [STORES_KEY, {
        detail: {
          submitComment: async () => {},
          getDetailError: () => null,
        },
      }],
    ]),
  });
}

function renderIssueCommentBox(owner = "octo", name = "repo", number = 1) {
  return render(IssueCommentBox, {
    props: { provider: "github", platformHost: "github.com", owner, name, repoPath: `${owner}/${name}`, number },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, mockAutocompleteClient()],
      [STORES_KEY, {
        issues: {
          submitIssueComment: async () => {},
          getIssueDetailError: () => null,
        },
      }],
    ]),
  });
}

describe("comment draft persistence", () => {
  afterEach(() => {
    setCommentDraft("pull", "octo", "repo", 1, "");
    setCommentDraft("pull", "octo", "repo", 2, "");
    setCommentDraft("issue", "octo", "repo", 1, "");
    setCommentDraft("issue", "octo", "repo", 2, "");
    setCommentDraft("pull", "octo", "repo", 1, "", "github.com");
    setCommentDraft("pull", "octo", "repo", 1, "", "ghe.example.com");
    setCommentDraft("issue", "octo", "repo", 1, "", "github.com");
    setCommentDraft("issue", "octo", "repo", 1, "", "ghe.example.com");
    setCommentDraft("pull", "group", "project", 1, "", "gitlab.example.com");
    setCommentDraft("issue", "group", "project", 1, "", "gitlab.example.com");
    clearCommentSubmitError("pull", "octo", "repo", 1);
    clearCommentSubmitError("pull", "octo", "repo", 2);
    clearCommentSubmitError("issue", "octo", "repo", 1);
    clearCommentSubmitError("issue", "octo", "repo", 2);
    clearCommentSubmitError("pull", "octo", "repo", 1, "github.com");
    clearCommentSubmitError("pull", "octo", "repo", 1, "ghe.example.com");
    clearCommentSubmitError("issue", "octo", "repo", 1, "github.com");
    clearCommentSubmitError("issue", "octo", "repo", 1, "ghe.example.com");
    finishCommentSubmit("pull", "octo", "repo", 1);
    finishCommentSubmit("pull", "octo", "repo", 2);
    finishCommentSubmit("issue", "octo", "repo", 1);
    finishCommentSubmit("issue", "octo", "repo", 2);
    finishCommentSubmit("pull", "octo", "repo", 1, "github.com");
    finishCommentSubmit("pull", "octo", "repo", 1, "ghe.example.com");
    finishCommentSubmit("issue", "octo", "repo", 1, "github.com");
    finishCommentSubmit("issue", "octo", "repo", 1, "ghe.example.com");
    finishCommentSubmit("pull", "group", "project", 1, "gitlab.example.com");
    finishCommentSubmit("issue", "group", "project", 1, "gitlab.example.com");
    cleanup();
  });

  it("keeps the pull request comment draft when the box remounts", async () => {
    const firstRender = renderPullCommentBox("octo", "repo", 1);

    setCommentDraft("pull", "octo", "repo", 1, "draft review note");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("draft review note");
    });

    firstRender.unmount();
    renderPullCommentBox("octo", "repo", 1);

    await waitFor(() => {
      expect(getCommentEditorText()).toBe("draft review note");
    });
  });

  it("keeps the issue comment draft when the box remounts", async () => {
    const firstRender = renderIssueCommentBox("octo", "repo", 2);

    setCommentDraft("issue", "octo", "repo", 2, "draft issue note");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("draft issue note");
    });

    firstRender.unmount();
    renderIssueCommentBox("octo", "repo", 2);

    await waitFor(() => {
      expect(getCommentEditorText()).toBe("draft issue note");
    });
  });

  it.each([
    ["pull request", renderPullCommentBox],
    ["issue", renderIssueCommentBox],
  ] as const)("renders the %s comment button inside the editor", async (_kind, renderBox) => {
    renderBox();

    const button = screen.getByRole("button", { name: "Comment" });
    await waitFor(() => {
      expect(document.querySelector(".comment-editor-input")).toBeInstanceOf(HTMLElement);
    });

    const shell = button.closest(".comment-editor-shell");
    expect(shell).not.toBeNull();
    expect(shell?.querySelector(".comment-editor-input")).toBe(getCommentEditor());
  });

  it.each(["pull", "issue"] as const)(
    "keeps %s comment drafts isolated by platform host",
    async (kind) => {
      const { rerender } = render(CommentBoxContextHarness, {
        props: {
          kind,
          owner: "octo",
          name: "repo",
          number: 1,
          platformHost: "github.com",
        },
      });

      setCommentDraft(kind, "octo", "repo", 1, "github draft", "github.com");
      await waitFor(() => {
        expect(getCommentEditorText()).toBe("github draft");
      });

      await rerender({
        kind,
        owner: "octo",
        name: "repo",
        number: 1,
        platformHost: "ghe.example.com",
      });

      setCommentDraft(kind, "octo", "repo", 1, "ghe draft", "ghe.example.com");
      await waitFor(() => {
        expect(getCommentEditorText()).toBe("ghe draft");
      });

      await rerender({
        kind,
        owner: "octo",
        name: "repo",
        number: 1,
        platformHost: "github.com",
      });

      await waitFor(() => {
        expect(getCommentEditorText()).toBe("github draft");
      });
      expect(getCommentDraft(kind, "octo", "repo", 1, "github.com")).toBe(
        "github draft",
      );
      expect(getCommentDraft(kind, "octo", "repo", 1, "ghe.example.com")).toBe(
        "ghe draft",
      );
    },
  );

  it.each(["pull", "issue"] as const)(
    "reads a legacy %s comment draft before a host-specific draft exists",
    async (kind) => {
      setCommentDraft(kind, "octo", "repo", 1, "legacy draft");

      render(CommentBoxContextHarness, {
        props: {
          kind,
          owner: "octo",
          name: "repo",
          number: 1,
          platformHost: "ghe.example.com",
        },
      });

      await waitFor(() => {
        expect(getCommentEditorText()).toBe("legacy draft");
      });
    },
  );

  it("does not clear the newly selected pull request draft when an earlier submit resolves", async () => {
    const submit = deferred();
    const { rerender } = render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "old pull draft");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    setCommentDraft("pull", "octo", "repo", 2, "new pull draft");
    await rerender({
      kind: "pull",
      owner: "octo",
      name: "repo",
      number: 2,
      submitComment: async () => submit.promise,
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(false);
    });
    expect(isCommentSubmitPending("pull", "octo", "repo", 2, "github.com")).toBe(false);
    expect(
      (screen.getByRole("button", { name: "Comment" }) as HTMLButtonElement).disabled,
    ).toBe(false);

    submit.resolve();

    await waitFor(() => {
      expect(getCommentDraft("pull", "octo", "repo", 1, "github.com")).toBe("");
    });

    await waitFor(() => {
      expect(getCommentEditorText()).toBe("new pull draft");
    });
    expect(getCommentDraft("pull", "octo", "repo", 2, "github.com")).toBe("new pull draft");
  });

  it("does not clear the newly selected issue draft when an earlier submit resolves", async () => {
    const submit = deferred();
    const { rerender } = render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
      },
    });

    setCommentDraft("issue", "octo", "repo", 1, "old issue draft");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    setCommentDraft("issue", "octo", "repo", 2, "new issue draft");
    await rerender({
      kind: "issue",
      owner: "octo",
      name: "repo",
      number: 2,
      submitComment: async () => submit.promise,
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(false);
    });
    expect(isCommentSubmitPending("issue", "octo", "repo", 2, "github.com")).toBe(false);
    expect(
      (screen.getByRole("button", { name: "Comment" }) as HTMLButtonElement).disabled,
    ).toBe(false);

    submit.resolve();

    await waitFor(() => {
      expect(getCommentDraft("issue", "octo", "repo", 1, "github.com")).toBe("");
    });

    await waitFor(() => {
      expect(getCommentEditorText()).toBe("new issue draft");
    });
    expect(getCommentDraft("issue", "octo", "repo", 2, "github.com")).toBe("new issue draft");
  });

  it("keeps the original pull request disabled when returning to it before its submit resolves", async () => {
    const submits = deferredByNumber([1, 2]);
    const submitComment = async (_owner: string, _name: string, number: number) => {
      await submits.get(number)?.promise;
    };
    const { rerender } = render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment,
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "old pull draft");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    setCommentDraft("pull", "octo", "repo", 2, "new pull draft");
    await rerender({ kind: "pull", owner: "octo", name: "repo", number: 2, submitComment });
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    await rerender({ kind: "pull", owner: "octo", name: "repo", number: 1, submitComment });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(true);
    });
    expect(isCommentSubmitPending("pull", "octo", "repo", 1, "github.com")).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Posting…" }) as HTMLButtonElement).disabled,
    ).toBe(true);

    submits.get(2)?.resolve();
    await waitFor(() => {
      expect(getCommentDraft("pull", "octo", "repo", 2, "github.com")).toBe("");
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(true);
    });

    submits.get(1)?.resolve();
    await waitFor(() => {
      expect(getCommentDraft("pull", "octo", "repo", 1, "github.com")).toBe("");
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(false);
    });
    expect(isCommentSubmitPending("pull", "octo", "repo", 1, "github.com")).toBe(false);
  });

  it("keeps the original issue disabled when returning to it before its submit resolves", async () => {
    const submits = deferredByNumber([1, 2]);
    const submitComment = async (_owner: string, _name: string, number: number) => {
      await submits.get(number)?.promise;
    };
    const { rerender } = render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment,
      },
    });

    setCommentDraft("issue", "octo", "repo", 1, "old issue draft");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    setCommentDraft("issue", "octo", "repo", 2, "new issue draft");
    await rerender({ kind: "issue", owner: "octo", name: "repo", number: 2, submitComment });
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    await rerender({ kind: "issue", owner: "octo", name: "repo", number: 1, submitComment });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(true);
    });
    expect(isCommentSubmitPending("issue", "octo", "repo", 1, "github.com")).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Posting…" }) as HTMLButtonElement).disabled,
    ).toBe(true);

    submits.get(2)?.resolve();
    await waitFor(() => {
      expect(getCommentDraft("issue", "octo", "repo", 2, "github.com")).toBe("");
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(true);
    });

    submits.get(1)?.resolve();
    await waitFor(() => {
      expect(getCommentDraft("issue", "octo", "repo", 1, "github.com")).toBe("");
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(false);
    });
    expect(isCommentSubmitPending("issue", "octo", "repo", 1, "github.com")).toBe(false);
  });

  it("keeps a pull request pending submit disabled across remounts", async () => {
    const submit = deferred();
    const firstRender = render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "draft review note");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));
    expect(isCommentSubmitPending("pull", "octo", "repo", 1, "github.com")).toBe(true);

    firstRender.unmount();
    render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
      },
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(true);
    });

    submit.resolve();
    await waitFor(() => {
      expect(isCommentSubmitPending("pull", "octo", "repo", 1, "github.com")).toBe(false);
    });
  });

  it("keeps an issue pending submit disabled across remounts", async () => {
    const submit = deferred();
    const firstRender = render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
      },
    });

    setCommentDraft("issue", "octo", "repo", 1, "draft issue note");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));
    expect(isCommentSubmitPending("issue", "octo", "repo", 1, "github.com")).toBe(true);

    firstRender.unmount();
    render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
      },
    });

    await waitFor(() => {
      expect(isCommentEditorDisabled()).toBe(true);
    });

    submit.resolve();
    await waitFor(() => {
      expect(isCommentSubmitPending("issue", "octo", "repo", 1, "github.com")).toBe(false);
    });
  });

  it("keeps a pull request submit error visible across remounts", async () => {
    const submit = deferred();
    const firstRender = render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
        getError: () => "pull submit failed",
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "draft review note");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    submit.resolve();
    await waitFor(() => {
      expect(getCommentSubmitError("pull", "octo", "repo", 1, "github.com")).toBe("pull submit failed");
    });

    firstRender.unmount();
    render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => Promise.resolve(),
        getError: () => "pull submit failed",
      },
    });

    expect(screen.getByText("pull submit failed")).toBeTruthy();
  });

  it("keeps an issue submit error visible across remounts", async () => {
    const submit = deferred();
    const firstRender = render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => submit.promise,
        getError: () => "issue submit failed",
      },
    });

    setCommentDraft("issue", "octo", "repo", 1, "draft issue note");
    await waitForCommentButtonEnabled();
    await fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    submit.resolve();
    await waitFor(() => {
      expect(getCommentSubmitError("issue", "octo", "repo", 1, "github.com")).toBe("issue submit failed");
    });

    firstRender.unmount();
    render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        owner: "octo",
        name: "repo",
        number: 1,
        submitComment: async () => Promise.resolve(),
        getError: () => "issue submit failed",
      },
    });

    expect(screen.getByText("issue submit failed")).toBeTruthy();
  });

  it("shows username autocomplete suggestions and inserts the selected mention", async () => {
    render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        autocompleteResponse: {
          users: ["alice", "albert"],
          references: [],
        },
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "@al");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("@al");
    });

    await fireEvent.focus(getCommentEditor());

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /@alice/i })).toBeTruthy();
    });

    await fireEvent.keyDown(getCommentEditor(), { key: "Enter" });

    await waitFor(() => {
      expect(getCommentDraft("pull", "octo", "repo", 1, "github.com")).toBe("@alice ");
    });
  });

  it("shows issue and pull request reference suggestions and inserts the selected item", async () => {
    render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        autocompleteResponse: {
          users: [],
          references: [
            { kind: "pull", number: 12, title: "Polish mentions", state: "open" },
            { kind: "issue", number: 17, title: "Mention bug", state: "open" },
          ],
        },
      },
    });

    setCommentDraft("issue", "octo", "repo", 1, "#1");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("#1");
    });

    await fireEvent.focus(getCommentEditor());

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /#12/i })).toBeTruthy();
    });

    await fireEvent.keyDown(getCommentEditor(), { key: "Enter" });

    await waitFor(() => {
      expect(getCommentDraft("issue", "octo", "repo", 1, "github.com")).toBe("#12 ");
    });
  });

  it("uses GitLab issue and merge request reference prefixes for autocomplete", async () => {
    const autocompleteQueries: Array<Record<string, unknown> | undefined> = [];

    render(CommentBoxContextHarness, {
      props: {
        kind: "issue",
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "project",
        repoPath: "group/project",
        autocompleteResponse: {
          users: [],
          references: [
            { kind: "pull", number: 12, title: "Polish mentions", state: "open" },
            { kind: "issue", number: 17, title: "Mention bug", state: "open" },
          ],
        },
        onAutocompleteQuery: (query: Record<string, unknown> | undefined) => {
          autocompleteQueries.push(query);
        },
      },
    });

    setCommentDraft("issue", "group", "project", 1, "#1", "gitlab.example.com");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("#1");
    });

    await fireEvent.focus(getCommentEditor());

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /#17/i })).toBeTruthy();
    });
    expect(screen.queryByRole("option", { name: /#12/i })).toBeNull();
    expect(autocompleteQueries.at(-1)).toMatchObject({
      query: { trigger: "#" },
    });

    await fireEvent.keyDown(getCommentEditor(), { key: "Enter" });

    await waitFor(() => {
      expect(getCommentDraft("issue", "group", "project", 1, "gitlab.example.com")).toBe("#17 ");
    });

    setCommentDraft("issue", "group", "project", 1, "!1", "gitlab.example.com");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("!1");
    });

    await fireEvent.focus(getCommentEditor());

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /!12/i })).toBeTruthy();
    });
    expect(screen.queryByRole("option", { name: /!17/i })).toBeNull();
    expect(autocompleteQueries.at(-1)).toMatchObject({
      query: { trigger: "!" },
    });

    await fireEvent.keyDown(getCommentEditor(), { key: "Enter" });

    await waitFor(() => {
      expect(getCommentDraft("issue", "group", "project", 1, "gitlab.example.com")).toBe("!12 ");
    });
  });

  it.each(["pull", "issue"] as const)(
    "passes the platform host to %s comment autocomplete",
    async (kind) => {
      const autocompleteQueries: Array<Record<string, unknown> | undefined> = [];

      render(CommentBoxContextHarness, {
        props: {
          kind,
          platformHost: "ghe.example.com",
          autocompleteResponse: {
            users: ["alice"],
            references: [],
          },
          onAutocompleteQuery: (query: Record<string, unknown> | undefined) => {
            autocompleteQueries.push(query);
          },
        },
      });

      setCommentDraft(kind, "octo", "repo", 1, "@al");
      await waitFor(() => {
        expect(getCommentEditorText()).toBe("@al");
      });

      await fireEvent.focus(getCommentEditor());

      await waitFor(() => {
        expect(screen.getByRole("option", { name: /@alice/i })).toBeTruthy();
      });

      expect(autocompleteQueries.at(-1)).toMatchObject({
        path: {
          platform_host: "ghe.example.com",
        },
      });
    },
  );

  it("does not accept an autocomplete suggestion while IME composition is active", async () => {
    render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        autocompleteResponse: {
          users: ["alice", "albert"],
          references: [],
        },
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "@al");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("@al");
    });

    await fireEvent.focus(getCommentEditor());

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /@alice/i })).toBeTruthy();
    });

    await fireEvent.compositionStart(getCommentEditor());

    await waitFor(() => {
      expect(screen.queryByRole("option", { name: /@alice/i })).toBeNull();
    });

    await fireEvent.keyDown(getCommentEditor(), {
      key: "Enter",
      isComposing: true,
    });

    await waitFor(() => {
      expect(getCommentDraft("pull", "octo", "repo", 1, "github.com")).toBe("@al");
    });
  });

  it("submits with Cmd+Enter even when autocomplete is open", async () => {
    const submitComment = async () => Promise.resolve();
    const submitSpy = vi.fn(submitComment);

    render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        submitComment: submitSpy,
        autocompleteResponse: {
          users: ["alice", "albert"],
          references: [],
        },
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "@al");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("@al");
    });

    await fireEvent.focus(getCommentEditor());
    await waitFor(() => {
      expect(screen.getByRole("option", { name: /@alice/i })).toBeTruthy();
    });

    await fireEvent.keyDown(getCommentEditor(), { key: "Enter", metaKey: true });

    await waitFor(() => {
      expect(submitSpy).toHaveBeenCalledWith("octo", "repo", 1, "@al");
    });
  });

  it("persists the first typed change after syncing the editor from props", async () => {
    render(CommentBoxContextHarness, {
      props: { kind: "pull" },
    });

    setCommentDraft("pull", "octo", "repo", 1, "draft review note");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("draft review note");
    });

    const editor = getCommentEditor();
    await fireEvent.focus(editor);
    await fireEvent.input(editor, {
      inputType: "insertText",
      data: "!",
      target: { textContent: "draft review note!" },
    });

    await waitFor(() => {
      expect(getCommentDraft("pull", "octo", "repo", 1, "github.com")).toBe("draft review note!");
    });
  });

  it("submits from the editor with Cmd+Enter", async () => {
    const submitComment = async () => Promise.resolve();
    const submitSpy = vi.fn(submitComment);

    render(CommentBoxContextHarness, {
      props: {
        kind: "pull",
        submitComment: submitSpy,
      },
    });

    setCommentDraft("pull", "octo", "repo", 1, "hello @alice");
    await waitFor(() => {
      expect(getCommentEditorText()).toBe("hello @alice");
    });

    await fireEvent.focus(getCommentEditor());
    await fireEvent.keyDown(getCommentEditor(), { key: "Enter", metaKey: true });

    await waitFor(() => {
      expect(submitSpy).toHaveBeenCalledWith("octo", "repo", 1, "hello @alice");
    });
  });
});
