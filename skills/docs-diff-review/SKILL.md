---
name: docs-diff-review
description: Render kenn-forge documentation changes for human review as aligned before-and-after blocks. Use when docs are rewritten, screenshots or navigation change, or a reviewer asks to see what changed on the rendered pages rather than reading a source diff.
---

# Docs diff review

Build both versions of the docs, assemble the review bundle, inspect it, and
open it for the user.

## Trust boundary

Use this skill only for documentation the user authored or explicitly approved
for local execution. The canonical docs build executes code from the checkout
before the viewer opens, so the viewer treats HTML produced by that same
checkout as trusted input. Review an untrusted branch as source, or build it in
a separate disposable environment instead of using this workflow.

Keep the base checkout and review bundle in owner-private temporary
directories. Bind every preview server explicitly to `127.0.0.1`, and keep the
bundle local. This skill is a maintainer tool, not a hosted review service or an
HTML sandbox.

## Build the two sites

1. Choose the comparison base. Use the merge base with `origin/main` unless the
   user names another ref.
2. Stop only preview servers created during the current task before running the
   canonical build. If another process owns port 4178, inspect it and ask before
   stopping it.
3. Run `make docs-build` in the working tree. This is the after site.
4. Export the base revision to a private temporary directory. Symlink the
   working tree's `node_modules` into it, then run `make docs-build` there. This
   is the before site. Keep application state inside the repository's isolated
   screenshot harness.

Both builds must pass before assembling the review. A source-only comparison is
not a substitute because screenshots, Markdown extensions, navigation, and
theme behavior affect what readers see.

## Assemble and review

Create a fresh temporary output path, then run:

```sh
node skills/docs-diff-review/scripts/assemble-review.mjs \
  --before-site <base-checkout>/site \
  --after-site site \
  --output <temporary-root>/review \
  --base <comparison-ref>
```

The script copies both rendered sites and writes a manifest of changed
user-facing pages. Its bundled viewer aligns headings, paragraphs, lists,
tables, code, and screenshots. It hides unchanged blocks by default.

The aligned blocks use the review viewer's own styles. When a stylesheet,
template, or navigation file changes, use **Open before** and **Open final
page** to inspect the complete rendered pages as well.

Serve the review directory on a free loopback port with an explicit loopback
binding, for example:

```sh
python3 -m http.server <port> --bind 127.0.0.1
```

Open the review URL and the final rendered site in the user's browser. Before
calling it ready, inspect at least one changed page in a real browser and
confirm that the page list, aligned rows, screenshots, and "Open final page"
link work.

Leave the preview running while the user reviews it. Report the two local URLs,
the comparison ref, and both build results.
