import { describe, expect, test } from "vite-plus/test";
import { addFilterToQuery } from "./searchQuery";

describe("addFilterToQuery", () => {
  test("empty starting query returns just the token", () => {
    expect(addFilterToQuery("", "from:alice@example.com")).toBe("from:alice@example.com");
  });

  test("existing free-text plus new token appends with single space", () => {
    expect(addFilterToQuery("project", "from:alice@example.com")).toBe("project from:alice@example.com");
  });

  test("duplicate token is a no-op", () => {
    expect(addFilterToQuery("project from:alice@example.com", "from:alice@example.com")).toBe(
      "project from:alice@example.com",
    );
  });

  test("two different tokens of same operator are both kept", () => {
    expect(addFilterToQuery("from:alice@example.com", "from:bob@example.com")).toBe(
      "from:alice@example.com from:bob@example.com",
    );
  });

  test("duplicate detection is case-insensitive on operator and value", () => {
    expect(addFilterToQuery("From:Alice@Example.com", "from:alice@example.com")).toBe("From:Alice@Example.com");
  });

  test("extra whitespace normalizes to single spaces", () => {
    expect(addFilterToQuery("  project   ", "from:alice@example.com")).toBe("project from:alice@example.com");
  });

  test("label token appends to empty query", () => {
    expect(addFilterToQuery("", "label:Inbox")).toBe("label:Inbox");
  });

  test("domain token appends to free-text", () => {
    expect(addFilterToQuery("project", "domain:example.com")).toBe("project domain:example.com");
  });
});
