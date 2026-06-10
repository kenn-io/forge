import { describe, expect, test } from "vite-plus/test";
import { messagesHref, messagesSearch, messageIdFromRoute, parseMessagesRoute } from "./route";

describe("MessagesRoute", () => {
  test("parses q and message", () => {
    expect(parseMessagesRoute("?q=project&message=12345")).toEqual({
      mode: "messages",
      q: "project",
      message: "12345",
    });
  });
  test("missing params yield nulls", () => {
    expect(parseMessagesRoute("")).toEqual({ mode: "messages", q: null, message: null });
  });
  test("message is preserved as a string (no numeric coercion)", () => {
    expect(parseMessagesRoute("?message=00042")).toEqual({ mode: "messages", q: null, message: "00042" });
  });
  test("messagesHref round-trips parseMessagesRoute", () => {
    const route = { mode: "messages", q: "label:Inbox", message: "42" } as const;
    const href = messagesHref(route);
    expect(href).toContain("/messages");
    expect(href).toContain("q=label%3AInbox");
    expect(href).toContain("message=42");
    expect(parseMessagesRoute(href.replace(/^\/messages/, ""))).toEqual(route);
  });
  test("messagesHref omits null params", () => {
    expect(messagesHref({ mode: "messages", q: null, message: null })).toBe("/messages");
  });

  test("parseMessagesRoute with view=linked sets view field", () => {
    expect(parseMessagesRoute("?view=linked").view).toBe("linked");
  });

  test("messagesSearch of a route with view:linked contains view=linked", () => {
    const result = messagesSearch({ mode: "messages", q: null, message: null, view: "linked" });
    expect(result).toContain("view=linked");
  });

  test("q + message + view round-trips through parse -> search -> parse", () => {
    const original = { mode: "messages" as const, q: "test", message: "42", view: "linked" as const };
    const qs = messagesSearch(original);
    const parsed = parseMessagesRoute(qs);
    expect(parsed).toEqual(original);
  });

  test("parseMessagesRoute with view=bogus sets view to undefined", () => {
    expect(parseMessagesRoute("?view=bogus").view).toBeUndefined();
  });
});

describe("messageIdFromRoute", () => {
  test("null returns null", () => {
    expect(messageIdFromRoute(null)).toBeNull();
  });
  test("empty string returns null", () => {
    expect(messageIdFromRoute("")).toBeNull();
  });
  test("non-numeric string returns null", () => {
    expect(messageIdFromRoute("not-a-number")).toBeNull();
  });
  test("valid positive integer string returns the number", () => {
    expect(messageIdFromRoute("42")).toBe(42);
  });
});
