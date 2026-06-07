# Relicense Middleman to Elastic License 2.0 (dual-licensed)

- **Date:** 2026-06-06
- **Status:** Design approved; **BLOCKED on the contributor-rights gate below**
  before any implementation.
- **Author:** Wes McKinney

## Goal

Relicense Middleman from MIT to a dual-license model:

- **Default:** Elastic License 2.0 (ELv2), a source-available license.
- **Commercial alternative:** a commercial license offered by Kenn Software LLC
  for uses ELv2 does not permit (notably, offering Middleman as a hosted or
  managed service).

## Pre-implementation gate (BLOCKING): contributor rights

Middleman is **not** a solo project. `git shortlog -se HEAD` shows six
contributor identities:

| Contributor | Commits |
|-------------|--------:|
| Marius van Niekerk | 232 |
| Wes McKinney | 139 |
| Phillip Cloud | 54 |
| Andy Hadjigeorgiou | 2 |
| Christophe Dervieux | 1 |
| Eric Dill | 1 |

The current MIT license covers "Wes McKinney and middleman contributors," and the
majority of commits come from contributors outside Kenn Software. Relicensing to
ELv2 with a commercial option requires that Kenn Software LLC have the right to
(a) distribute every existing contribution under ELv2 and (b) offer those
contributions under a separate commercial license.

**This must be resolved before implementation.** The options below are not legal
advice; confirm the chosen path with counsel:

1. **Contributor consent / assignment.** Obtain written agreement from the five
   non-Kenn contributors to relicense their contributions under ELv2 and to the
   commercial grant. Cleanest, but requires reaching everyone.
2. **Rely on MIT's sublicense grant.** MIT permits redistribution and
   sublicensing under other terms, so existing contributions may be redistributed
   under ELv2 **provided the original MIT copyright and permission notice is
   retained** for those contributions (e.g., in a `NOTICE` or third-party-notices
   file). This interacts with the attribution decision: the retained MIT notice
   names "Wes McKinney and middleman contributors," not Kenn Software LLC alone.
3. **Remove or rewrite.** Identify contributions whose rights cannot be secured
   and remove or replace them before relicensing.

Given the volume of external contribution (Marius authored the majority of
commits), confirm the path with the user and, ideally, legal counsel. **No file
changes from the Design section happen until this gate is closed.**

## Current state

- `LICENSE` — MIT License, `Copyright (c) 2026 Wes McKinney and middleman
  contributors`.
- `README.md` (§License, ~line 389) — body is just `MIT`.
- Go module path is `go.kenn.io/middleman`. (`go.mod` carries no license field;
  Go has no such convention.)
- `package.json`: root and `frontend/` are `"private": true`; **`packages/ui/`
  (`@middleman/ui`) is not marked private**. None has a `license` field.
- Rust: root `Cargo.toml` is `[workspace]`-only; `rust/pty-manager/Cargo.toml`
  (`middleman-pty-manager`) has no `license` or `publish` field.
- No per-file license headers anywhere.
- No in-app, CLI, or API surface displays the license. In tracked non-doc files,
  `MIT` appears only in `LICENSE` and `README.md` (verified via `rg`).

This keeps the relicense contained: root legal files, the README section, and
package metadata. No source-code behavior changes.

## Decisions (from brainstorming)

| Decision | Choice |
|----------|--------|
| Licensor / copyright holder (legal) | **Kenn Software LLC** |
| Name used in README prose | **Kenn Software**, linked to https://kenn.io |
| Commercial license contact | **info@kenn.io** |
| Per-file SPDX/license headers | **No** — license info stays centralized |
| File layout | `LICENSE` (verbatim ELv2) + new `NOTICE` + README update |
| `package.json` `license` field | Add `"license": "Elastic-2.0"` (SPDX id) |
| `packages/ui` privacy | Add `"private": true` (internal workspace pkg) — *confirm* |
| Rust `pty-manager` metadata | Add `license = "Elastic-2.0"` + `publish = false` — *confirm* |

The last two rows extend the original scope slightly (surfaced in review); they
are flagged for explicit user confirmation during spec review.

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
the sha256 before writing (copying the file, not retyping), so the result is
byte-for-byte the canonical text.

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

**If gate path 2 (MIT sublicense) is chosen,** the original MIT copyright and
permission notice must also be retained — appended to this `NOTICE` (or a
`NOTICE`/third-party section) so the prior contributions keep their required
attribution. The final `NOTICE` shape therefore depends on the gate outcome.

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

### 4. Package metadata

- Add `"license": "Elastic-2.0"` (registered SPDX id) to all three
  `package.json` files: root, `frontend/`, `packages/ui/`.
- Add `"private": true` to `packages/ui/package.json` — it is currently the only
  workspace package not so marked, and it is an internal, unpublished package.
  *(Confirm during review; minor scope addition.)*
- Add `license = "Elastic-2.0"` and `publish = false` to
  `rust/pty-manager/Cargo.toml`. It is an internal build component, not a
  published crate. *(Confirm during review; minor scope addition.)*

These packages are unpublished, so the fields are accurate metadata rather than a
publishing requirement; license-scanning tooling resolves the SPDX id correctly.

## Out of scope (flagged, not done)

- **CLA / DCO for *future* contributions.** Distinct from the existing-
  contributions gate above: once relicensed, keeping the commercial grant clean
  for *new* outside contributions wants a CLA or DCO. Track separately.
- **Per-file license headers.** Explicitly declined; license info is centralized.
- **App / CLI license display.** None exists today; none is added.

## Verification

- `LICENSE` first line is `Elastic License 2.0`; file `sha256` equals
  `48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2`.
- No *active* license references to MIT remain. Scope the check to license
  surfaces and exclude historical docs:
  `rg -ni "\bMIT\b" -g '!docs/**' -g '!node_modules/**'` returns no matches —
  **except** an intentionally retained MIT notice if gate path 2 is chosen, which
  is expected and allowed.
- `NOTICE` exists with the content above (plus the retained MIT notice if path 2).
- README §License renders the new content; links resolve (`LICENSE`,
  https://kenn.io).
- All three `package.json` files are valid JSON; `make build` succeeds (confirms
  the frontend and Go build are intact).
- `rust/pty-manager/Cargo.toml` parses; `cargo check` (or the repo's Rust build)
  succeeds.

No automated tests are added: the change touches legal/metadata files only, with
no API, data-flow, or behavior surface for an e2e test to exercise.
