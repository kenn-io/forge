# Relicense Middleman to Elastic License 2.0 (dual-licensed)

- **Date:** 2026-06-06
- **Status:** Approved (design)
- **Author:** Wes McKinney

## Goal

Relicense Middleman from MIT to a dual-license model:

- **Default:** Elastic License 2.0 (ELv2), a source-available license.
- **Commercial alternative:** a commercial license offered by Kenn Software LLC
  for uses ELv2 does not permit (notably, offering Middleman as a hosted or
  managed service).

## Current state

- `LICENSE` — MIT License, `Copyright (c) 2026 Wes McKinney and middleman
  contributors`.
- `README.md` (§License, ~line 389) — body is just `MIT`.
- Go module path is `go.kenn.io/middleman`.
- No `license` field in any `package.json` (root, `frontend/`, `packages/ui/`);
  all are `private`.
- No per-file license headers anywhere.
- No in-app, CLI, or API surface displays the license. `MIT` appears only in
  `LICENSE` and `README.md` (verified via `rg`).

This makes the relicense contained: it touches root legal files, the README
section, and `package.json` metadata. No source-code behavior changes.

## Decisions (from brainstorming)

| Decision | Choice |
|----------|--------|
| Licensor / copyright holder (legal) | **Kenn Software LLC** |
| Name used in README prose | **Kenn Software**, linked to https://kenn.io |
| Commercial license contact | **info@kenn.io** |
| Per-file SPDX/license headers | **No** — license info stays centralized |
| File layout | `LICENSE` (verbatim ELv2) + new `NOTICE` + README update |
| `package.json` `license` field | Add `"license": "Elastic-2.0"` (SPDX id) |

## Design (Approach A)

### 1. `LICENSE`

Replace the MIT text with the **verbatim** Elastic License 2.0 text as published
by Elastic.

- Source of truth:
  `https://raw.githubusercontent.com/elastic/elasticsearch/main/licenses/ELASTIC-LICENSE-2.0.txt`
- Cross-reference: https://www.elastic.co/licensing/elastic-license
- SPDX identifier: `Elastic-2.0`
- Expected content: 93 lines, `sha256
  48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2`

The legal text is **not** modified. ELv2 defines "the licensor" generically
("the entity offering these terms"); the licensor is named in `NOTICE` and the
README, not by editing the license body. Implementation must re-fetch and verify
the sha256 before writing, so the file is byte-for-byte the canonical text.

### 2. `NOTICE` (new file)

Names Kenn Software LLC as the ELv2 licensor and states the commercial option.
Exact content:

```
Middleman
Copyright (c) 2026 Kenn Software LLC

This software is made available under the Elastic License 2.0.
See the LICENSE file for the full license text.

For purposes of the Elastic License 2.0, the licensor is Kenn Software LLC.

A commercial license is available for uses not permitted under the Elastic
License 2.0 (for example, providing Middleman to third parties as a hosted or
managed service). For commercial licensing, contact Kenn Software LLC at
info@kenn.io.
```

### 3. `README.md` — §License

Replace the single line `MIT` with:

```
## License

Middleman is source-available software, licensed under the
[Elastic License 2.0](LICENSE) (ELv2).

You can use, copy, modify, and redistribute it for free. The main restriction is
that you may not provide Middleman to third parties as a hosted or managed
service that gives users access to a substantial set of its features. You also
may not remove the project's licensing or copyright notices. The
[LICENSE](LICENSE) file is the authoritative text; this paragraph is a
non-binding summary.

A commercial license is available for uses not permitted by ELv2. For commercial
licensing, contact [Kenn Software](https://kenn.io) at info@kenn.io.
```

The summary deliberately omits ELv2's license-key clause: Middleman ships no
license-key functionality, so that limitation is inert and mentioning it would
mislead. The full `LICENSE` still governs.

### 4. `package.json` (×3)

Add `"license": "Elastic-2.0"` to:

- `package.json` (root)
- `frontend/package.json`
- `packages/ui/package.json`

These packages are `private` and unpublished, so the field is accurate metadata
rather than a publishing requirement. `Elastic-2.0` is the registered SPDX
identifier, so license-scanning tooling resolves it correctly.

## Out of scope (flagged, not done)

- **Contributor licensing (CLA/DCO).** A dual-license model requires Kenn
  Software LLC to hold rights to relicense *all* contributions commercially. The
  project is effectively solo today, so this is fine now. Before accepting
  outside contributions, add a CLA or DCO so the commercial grant stays clean.
  Track this separately.
- **Per-file license headers.** Explicitly declined; license info is centralized.
- **App/CLI license display.** None exists today; none is added.

## Verification

- `LICENSE` first line is `Elastic License 2.0`; file `sha256` equals
  `48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2`.
- `rg -ni "\bMIT\b"` over tracked files (excluding `node_modules`) returns no
  matches.
- `NOTICE` exists with the content above.
- README §License renders the new content; links resolve (`LICENSE`,
  https://kenn.io).
- `make build` succeeds — confirms the three `package.json` edits are valid JSON
  and do not break the frontend or Go build.

No automated tests are added: the change touches legal/metadata files only, with
no API, data-flow, or behavior surface for an e2e test to exercise.
