import type { TypeaheadOption } from "@kenn-io/kit-ui";
import type { WorkspaceListItem } from "./workspace-list-schema.js";

export function workspaceAuthorOptions(workspaces: readonly WorkspaceListItem[]): TypeaheadOption[] {
  const authors = new Map<string, string>();
  for (const { item_author: author } of workspaces) {
    if (author && !authors.has(author.toLowerCase())) authors.set(author.toLowerCase(), author);
  }
  return [...authors.values()].sort((a, b) => a.localeCompare(b)).map((author) => ({ name: author, label: author }));
}

export function workspaceMatchesAuthor(workspace: WorkspaceListItem, author: string): boolean {
  return !author || workspace.item_author?.toLowerCase() === author.toLowerCase();
}
