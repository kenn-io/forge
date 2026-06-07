# Relicense Middleman to Elastic License 2.0 (dual-licensed)

- **Date:** 2026-06-06
- **Status:** Approved; contributor-rights gate resolved. Ready for the
  implementation plan.
- **Author:** Wes McKinney

## Goal

Relicense Middleman from MIT to a dual-license model, effective from the
changepoint commit forward:

- **Default:** Elastic License 2.0 (ELv2), a source-available license.
- **Commercial alternative:** a commercial license offered by Kenn Software LLC
  for uses ELv2 does not permit (notably, offering Middleman as a hosted or
  managed service).
- **Prior contributions:** remain available under the MIT License (see gate).

## Contributor rights (resolved)

`git shortlog -se HEAD` shows six contributor identities:

| Contributor | Commits | Affiliation |
|-------------|--------:|-------------|
| Marius van Niekerk | 232 | Kenn Software |
| Wes McKinney | 139 | Kenn Software |
| Phillip Cloud | 54 | Kenn Software |
| Andy Hadjigeorgiou | 2 | external |
| Christophe Dervieux | 1 | external |
| Eric Dill | 1 | external |

Wes, Marius, and Phillip are all part of Kenn Software (425 of 429 commits). The
four external commits (Andy ×2, Christophe ×1, Eric ×1) all predate the
changepoint commit `4b28941c`.

**Resolution.** Relicense forward to ELv2. Because MIT permits commercial use and
sublicensing, prior contributions need no separate consent: the `NOTICE` records
that the MIT License covers all contributions up to and including the changepoint
commit and reproduces the MIT text, satisfying MIT's notice-retention
requirement. From the changepoint forward, the project is under ELv2.

- **Changepoint commit:** `4b28941cbbc27b30747e39269ba86c0cb2a3e92c`
  (`4b28941c`, "Use Vite+ for frontend tooling (#436)", 2026-06-06). Only
  documentation/spec commits sit between it and the relicense commit.

This is not legal advice; the user owns the decision.

## Current state

- `LICENSE` — MIT License, `Copyright (c) 2026 Wes McKinney and middleman
  contributors`.
- `README.md` (§License, ~line 389) — body is just `MIT`.
- Go module path is `go.kenn.io/middleman`. (`go.mod` carries no license field;
  Go has no such convention.)
- `package.json`: root and `frontend/` are `"private": true`; `packages/ui/`
  (`@middleman/ui`) is not marked private. None has a `license` field.
- Rust: root `Cargo.toml` is `[workspace]`-only; `rust/pty-manager/Cargo.toml`
  (`middleman-pty-manager`) has no `license` or `publish` field.
- No per-file license headers anywhere.
- No in-app, CLI, or API surface displays the license. In tracked non-doc files,
  `MIT` appears only in `LICENSE` and `README.md` (verified via `rg`).

This keeps the relicense contained: root legal files, the README section, and
package metadata. No source-code behavior changes.

## Decisions

| Decision | Choice |
|----------|--------|
| Licensor / copyright holder (legal) | **Kenn Software LLC** |
| Name used in README prose | **Kenn Software**, linked to https://kenn.io |
| Commercial license contact | **info@kenn.io** |
| Per-file SPDX/license headers | **No** — license info stays centralized |
| File layout | `LICENSE` (verbatim ELv2) + new `NOTICE` + README update |
| Prior contributions | MIT retained in `NOTICE`; changepoint = `4b28941c` |
| `package.json` `license` field | Add `"license": "Elastic-2.0"` to all three |
| `packages/ui` privacy | Leave as-is (license field only; **not** private) |
| Rust `pty-manager` metadata | Add `license = "Elastic-2.0"` + `publish = false` |

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

The legal text is **not** modified. ELv2 defines "the licensor" generically; the
licensor is named in `NOTICE` and the README, not by editing the license body.
Implementation must re-fetch and verify the sha256 before writing (copying the
file, not retyping), so the result is byte-for-byte the canonical text.

### 2. `NOTICE` (new file)

Names Kenn Software LLC as the ELv2 licensor, states the commercial option, and
retains the MIT License for prior contributions. Exact content:

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

-------------------------------------------------------------------------------

Prior license

Middleman was originally distributed under the MIT License. All contributions
made up to and including commit 4b28941c ("Use Vite+ for frontend tooling")
remain available under the MIT License set out below. Contributions made after
that commit are licensed under the Elastic License 2.0.

MIT License

Copyright (c) 2026 Wes McKinney and middleman contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
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

Contributions made before the relicense remain available under the MIT License;
see the [NOTICE](NOTICE) file.

A commercial license is available for uses not permitted by ELv2. For commercial
licensing, contact [Kenn Software](https://kenn.io) at info@kenn.io.
```

The summary deliberately omits ELv2's license-key clause: Middleman ships no
license-key functionality, so that limitation is inert and mentioning it would
mislead. The full `LICENSE` still governs.

### 4. Package metadata

- Add `"license": "Elastic-2.0"` (registered SPDX id) to all three
  `package.json` files: root, `frontend/`, `packages/ui/`.
- Do **not** add `"private": true` to `packages/ui/package.json` (per review
  decision; only the license field changes there).
- Add `license = "Elastic-2.0"` and `publish = false` to
  `rust/pty-manager/Cargo.toml` — an internal build component, not a published
  crate.

These packages are unpublished, so the fields are accurate metadata rather than a
publishing requirement; license-scanning tooling resolves the SPDX id correctly.

## Out of scope (flagged, not done)

- **CLA / DCO for *future* contributions.** To keep the commercial grant clean
  for new outside contributions after the relicense, a CLA or DCO is advisable.
  Track separately.
- **Per-file license headers.** Explicitly declined; license info is centralized.
- **App / CLI license display.** None exists today; none is added.

## Verification

- `LICENSE` first line is `Elastic License 2.0`; file `sha256` equals
  `48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2`.
- No MIT references remain on the *forward* license surfaces. `NOTICE` (which
  intentionally retains the MIT text) and `docs/` are excluded:
  `rg -ni "\bMIT\b" -g '!docs/**' -g '!NOTICE' -g '!node_modules/**'` returns no
  matches (LICENSE is ELv2, README §License is ELv2, source has none).
- `NOTICE` exists with the content above (ELv2 attribution + retained MIT text +
  changepoint statement).
- README §License renders the new content; links resolve (`LICENSE`, `NOTICE`,
  https://kenn.io).
- All three `package.json` files are valid JSON; `make build` succeeds (confirms
  the frontend and Go build are intact).
- `rust/pty-manager/Cargo.toml` parses and `publish = false`; `cargo check` (or
  the repo's Rust build) succeeds.

No automated tests are added: the change touches legal/metadata files only, with
no API, data-flow, or behavior surface for an e2e test to exercise.
