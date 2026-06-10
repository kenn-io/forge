import { describe, expect, test } from "vite-plus/test";
import { linkify } from "./linkify";

describe("linkify", () => {
  test("plain text returns a single text segment", () => {
    const result = linkify("Hello, world!");
    expect(result).toEqual([{ kind: "text", value: "Hello, world!", href: "" }]);
  });

  test("empty string returns empty array", () => {
    expect(linkify("")).toEqual([]);
  });

  test("single absolute URL returns a single url segment", () => {
    const result = linkify("https://example.com");
    expect(result).toEqual([{ kind: "url", value: "https://example.com", href: "https://example.com" }]);
  });

  test("URL with trailing period strips the period", () => {
    const result = linkify("See https://example.com.");
    expect(result).toEqual([
      { kind: "text", value: "See ", href: "" },
      { kind: "url", value: "https://example.com", href: "https://example.com" },
      { kind: "text", value: ".", href: "" },
    ]);
  });

  test("URL with trailing comma strips the comma", () => {
    const result = linkify("Check https://example.com, then continue.");
    expect(result[1]).toEqual({ kind: "url", value: "https://example.com", href: "https://example.com" });
    expect(result[2]).toEqual({ kind: "text", value: ",", href: "" });
  });

  test("URL with multiple trailing punctuation chars strips all of them", () => {
    const result = linkify("(https://example.com).");
    expect(result.find((s) => s.kind === "url")).toEqual({
      kind: "url",
      value: "https://example.com",
      href: "https://example.com",
    });
    const trailing = result[result.length - 1]!;
    expect(trailing.kind).toBe("text");
    expect(trailing.value).toContain(".");
  });

  test("multiple URLs each get their own segment", () => {
    const result = linkify("Visit https://alpha.example.com and https://beta.example.com today.");
    expect(result.filter((s) => s.kind === "url")).toHaveLength(2);
    expect(result.find((s) => s.kind === "url" && s.value === "https://alpha.example.com")).toBeTruthy();
    expect(result.find((s) => s.kind === "url" && s.value === "https://beta.example.com")).toBeTruthy();
  });

  test("bare www. URL gets https:// href", () => {
    const result = linkify("Go to www.example.com now.");
    const urlSeg = result.find((s) => s.kind === "url");
    expect(urlSeg).toBeDefined();
    expect(urlSeg!.value).toBe("www.example.com");
    expect(urlSeg!.href).toBe("https://www.example.com");
  });

  test("mixed text and URL preserves text segments around the URL", () => {
    const result = linkify("Hello https://example.com world");
    expect(result).toEqual([
      { kind: "text", value: "Hello ", href: "" },
      { kind: "url", value: "https://example.com", href: "https://example.com" },
      { kind: "text", value: " world", href: "" },
    ]);
  });

  test("http:// URL is also recognized", () => {
    const result = linkify("http://example.com");
    expect(result).toEqual([{ kind: "url", value: "http://example.com", href: "http://example.com" }]);
  });

  test("URL with path and query string is preserved intact", () => {
    const result = linkify("See https://example.com/path?q=1&foo=bar");
    expect(result).toEqual([
      { kind: "text", value: "See ", href: "" },
      { kind: "url", value: "https://example.com/path?q=1&foo=bar", href: "https://example.com/path?q=1&foo=bar" },
    ]);
  });

  test("multiline text with a URL in the middle", () => {
    const text = "Line one\nSee https://example.com\nLine three";
    const result = linkify(text);
    const kinds = result.map((s) => s.kind);
    expect(kinds).toContain("url");
    const urlSeg = result.find((s) => s.kind === "url");
    expect(urlSeg!.value).toBe("https://example.com");
  });

  test("text-only adjacent to URL on the same line", () => {
    const text = "prefix https://alpha.example.com suffix";
    const result = linkify(text);
    expect(result[0]).toEqual({ kind: "text", value: "prefix ", href: "" });
    expect(result[1]).toEqual({
      kind: "url",
      value: "https://alpha.example.com",
      href: "https://alpha.example.com",
    });
    expect(result[2]).toEqual({ kind: "text", value: " suffix", href: "" });
  });

  test("URL with balanced parentheses is preserved intact", () => {
    const result = linkify("https://example.com/wiki/Rust_(programming_language)");
    expect(result).toEqual([
      {
        kind: "url",
        value: "https://example.com/wiki/Rust_(programming_language)",
        href: "https://example.com/wiki/Rust_(programming_language)",
      },
    ]);
  });

  test("URL with balanced parentheses inline preserves the URL", () => {
    const result = linkify("see https://example.com/wiki/Rust_(programming_language) - neat");
    const urlSeg = result.find((s) => s.kind === "url");
    expect(urlSeg).toEqual({
      kind: "url",
      value: "https://example.com/wiki/Rust_(programming_language)",
      href: "https://example.com/wiki/Rust_(programming_language)",
    });
    expect(result[0]).toEqual({ kind: "text", value: "see ", href: "" });
    const lastSeg = result[result.length - 1]!;
    expect(lastSeg.kind).toBe("text");
    expect(lastSeg.value).toContain("neat");
  });

  test("unbalanced trailing ')' is stripped as punctuation", () => {
    const result = linkify("foo (see https://example.com).");
    const urlSeg = result.find((s) => s.kind === "url");
    expect(urlSeg).toEqual({
      kind: "url",
      value: "https://example.com",
      href: "https://example.com",
    });
    const trailingSeg = result[result.length - 1]!;
    expect(trailingSeg.kind).toBe("text");
    expect(trailingSeg.value).toBe(").");
  });
});
