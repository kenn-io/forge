# Elastic License 2.0 Relicense Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Relicense Middleman from MIT to the Elastic License 2.0 (source-available) with a commercial license offered by Kenn Software LLC, retaining MIT for contributions made before the changepoint.

**Architecture:** Centralized license files only — no per-file headers. `LICENSE` becomes the verbatim ELv2 text; a new `NOTICE` names Kenn Software LLC as the ELv2 licensor, states the commercial option, and retains the MIT license for prior contributions (changepoint commit `4b28941c`). The README §License is rewritten, and `Elastic-2.0` SPDX metadata is added to the JS and Rust package manifests.

**Tech Stack:** Go + Svelte 5 (Vite/Bun workspaces) + a Rust crate (`middleman-pty-manager`). Build via `make`. Pre-commit hooks via `prek`.

**Spec:** `docs/superpowers/specs/2026-06-06-elastic-license-relicense-design.md`

---

## Execution notes (read first)

- **No automated tests.** This change touches legal/metadata files only — there is no API, data-flow, or behavior surface to exercise. Each task therefore ends in a **verification command with expected output** that plays the role a test would. Do not invent unit tests for this.
- **One atomic commit.** A relicense is a single logical changepoint, so Tasks 1–5 make their edits **without committing**, and Task 6 runs full verification and makes one commit. Prefer **inline execution** for this plan (subagent-per-task would fragment the atomic commit).
- **Never bypass hooks.** The commit in Task 6 must pass `prek` hooks (`check json`, `check toml`, trailing-whitespace, end-of-file). If a hook reports a fix, re-stage and commit again — never `--no-verify`.
- **Do not open a PR.** The repo owner will review locally and approve before any PR.

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `LICENSE` | Modify | Verbatim Elastic License 2.0 text (replaces MIT) |
| `NOTICE` | Create | ELv2 licensor attribution (Kenn Software LLC), commercial contact, retained MIT license for prior contributions |
| `README.md` | Modify | §License: ELv2 summary + MIT-prior pointer + commercial contact |
| `package.json` | Modify | Add `"license": "Elastic-2.0"` |
| `frontend/package.json` | Modify | Add `"license": "Elastic-2.0"` |
| `packages/ui/package.json` | Modify | Add `"license": "Elastic-2.0"` (stays non-private) |
| `rust/pty-manager/Cargo.toml` | Modify | Add `license = "Elastic-2.0"` and `publish = false` |

---

## Task 1: Replace LICENSE with verbatim Elastic License 2.0

**Files:**
- Modify: `LICENSE`

- [ ] **Step 1: Fetch the canonical ELv2 text and verify its hash**

Run:
```bash
curl -fsSL "https://raw.githubusercontent.com/elastic/elasticsearch/main/licenses/ELASTIC-LICENSE-2.0.txt" -o /tmp/elv2.txt
shasum -a 256 /tmp/elv2.txt
```
Expected output (the hash must match exactly):
```
48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2  /tmp/elv2.txt
```
If the hash differs, **STOP** — the upstream text changed and must not be used blindly. Fallback source (byte-identical file): `https://raw.githubusercontent.com/elastic/kibana/main/licenses/ELASTIC-LICENSE-2.0.txt`.

- [ ] **Step 2: Overwrite LICENSE with the verified text**

Run:
```bash
cp /tmp/elv2.txt LICENSE
```
(Copy the verified file; do not retype — the text contains a typographic apostrophe that must be preserved byte-for-byte.)

- [ ] **Step 3: Verify LICENSE content**

Run:
```bash
head -1 LICENSE
shasum -a 256 LICENSE
```
Expected:
```
Elastic License 2.0
48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2  LICENSE
```
Do not commit yet (atomic commit in Task 6).

---

## Task 2: Create NOTICE

**Files:**
- Create: `NOTICE`

- [ ] **Step 1: Write NOTICE with exact content**

Run (the single-quoted `'EOF'` keeps every character literal):
```bash
cat > NOTICE <<'EOF'
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
EOF
```

- [ ] **Step 2: Verify NOTICE content**

Run:
```bash
rg -c "Kenn Software LLC" NOTICE
rg -q "info@kenn.io" NOTICE && rg -q 'commit 4b28941c' NOTICE && rg -q "MIT License" NOTICE && echo "markers ok"
```
Expected:
```
3
markers ok
```

---

## Task 3: Rewrite README §License

**Files:**
- Modify: `README.md` (the `## License` section, currently the last section, ~line 389)

- [ ] **Step 1: Replace the License section**

Use the Edit tool on `README.md`.

old_string:
```
## License

MIT
```

new_string:
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

- [ ] **Step 2: Verify the README section**

Run:
```bash
rg -q "Elastic License 2.0\]\(LICENSE\)" README.md && rg -q "info@kenn.io" README.md && rg -q "\[NOTICE\]\(NOTICE\)" README.md && echo "readme ok"
```
Expected:
```
readme ok
```

---

## Task 4: Add the Elastic-2.0 license field to the JS package manifests

**Files:**
- Modify: `package.json`
- Modify: `frontend/package.json`
- Modify: `packages/ui/package.json`

- [ ] **Step 1: Edit root `package.json`**

Use the Edit tool on `package.json`.

old_string:
```
  "private": true,
  "type": "module",
```
new_string:
```
  "private": true,
  "license": "Elastic-2.0",
  "type": "module",
```

- [ ] **Step 2: Edit `frontend/package.json`**

Use the Edit tool on `frontend/package.json`.

old_string:
```
  "private": true,
  "type": "module",
```
new_string:
```
  "private": true,
  "license": "Elastic-2.0",
  "type": "module",
```

- [ ] **Step 3: Edit `packages/ui/package.json`**

Use the Edit tool on `packages/ui/package.json`. (No `"private"` field is added here — per the spec decision, only the license field changes.)

old_string:
```
  "version": "0.0.1",
  "type": "module",
```
new_string:
```
  "version": "0.0.1",
  "license": "Elastic-2.0",
  "type": "module",
```

- [ ] **Step 4: Verify the three manifests are valid JSON with the field set**

Run:
```bash
rg -c '"license": "Elastic-2.0"' package.json frontend/package.json packages/ui/package.json
python3 -c "import json; [json.load(open(f)) for f in ('package.json','frontend/package.json','packages/ui/package.json')]; print('valid json')"
```
Expected:
```
package.json:1
frontend/package.json:1
packages/ui/package.json:1
valid json
```

---

## Task 5: Add license + publish metadata to the Rust crate

**Files:**
- Modify: `rust/pty-manager/Cargo.toml`

- [ ] **Step 1: Edit `rust/pty-manager/Cargo.toml`**

Use the Edit tool on `rust/pty-manager/Cargo.toml`.

old_string:
```
edition = "2024"

[dependencies]
```
new_string:
```
edition = "2024"
license = "Elastic-2.0"
publish = false

[dependencies]
```

- [ ] **Step 2: Verify the Cargo manifest**

Run:
```bash
rg -c 'license = "Elastic-2.0"|publish = false' rust/pty-manager/Cargo.toml
```
Expected:
```
2
```
TOML syntax is also validated by the `check toml` pre-commit hook in Task 6. If a Rust toolchain is available, `cargo build -p middleman-pty-manager` should still succeed (these are metadata-only fields).

---

## Task 6: Full verification and atomic relicense commit

**Files:** none (verification + commit only)

- [ ] **Step 1: Re-verify LICENSE**

Run:
```bash
head -1 LICENSE && shasum -a 256 LICENSE
```
Expected:
```
Elastic License 2.0
48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2  LICENSE
```

- [ ] **Step 2: Confirm no stray MIT references on forward license surfaces**

Run:
```bash
rg -ni "\bMIT\b" -g '!docs/**' -g '!NOTICE' -g '!README.md' -g '!node_modules/**'; echo "exit=$?"
```
Expected: no matching lines, and `exit=1` (ripgrep exits 1 when nothing matches). The only intentional MIT references live in `NOTICE`, `README.md`, and `docs/`, which are excluded.

- [ ] **Step 3: Build to confirm the manifest edits are intact (thorough check)**

Run:
```bash
make build
```
Expected: the frontend build (Bun install + Vite) and `go build` complete with no error and produce the `middleman` binary. This is end-to-end confidence that the `package.json` edits are consumable by the toolchain.

If the environment cannot run `make build` (e.g. no network for `bun install`), this is acceptable: the metadata change cannot affect compilation, and JSON/TOML validity is already gated by Task 4 Step 4 and the `check json` / `check toml` pre-commit hooks in Step 5. Note in the handoff that `make build` was skipped and why.

- [ ] **Step 4: Review the change set**

Run:
```bash
git status --short
```
Expected exactly these seven paths (one new `NOTICE`, six modified):
```
 M LICENSE
?? NOTICE
 M README.md
 M package.json
 M frontend/package.json
 M packages/ui/package.json
 M rust/pty-manager/Cargo.toml
```
If `make build` left build artifacts (e.g. `middleman`, `internal/web/dist/`), they are gitignored and will not appear; do not stage them.

- [ ] **Step 5: Commit as a single atomic relicense**

Run:
```bash
git add LICENSE NOTICE README.md package.json frontend/package.json packages/ui/package.json rust/pty-manager/Cargo.toml
git commit -m "Relicense Middleman from MIT to Elastic License 2.0

Middleman is now source-available under the Elastic License 2.0, with a
commercial license available from Kenn Software LLC (info@kenn.io) for
uses ELv2 does not permit, such as offering it as a hosted service.

NOTICE names Kenn Software LLC as the ELv2 licensor and retains the MIT
license for contributions made through changepoint commit 4b28941c;
those prior contributions remain available under MIT.

Records the Elastic-2.0 SPDX id in the JS package.json files and in
rust/pty-manager/Cargo.toml (publish = false).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```
Expected: the `prek` pre-commit hooks run and pass (`check json`, `check toml`, trailing-whitespace, end-of-file, etc.). The canonical ELv2 text is already hook-clean, so `LICENSE` is unchanged by the hooks and its sha256 stays valid. If a hook auto-fixes any file, re-run `git add` for it and re-commit. Never use `--no-verify`.

- [ ] **Step 6: Confirm the commit and final LICENSE state**

Run:
```bash
git show --stat HEAD | head -20
shasum -a 256 LICENSE
```
Expected: the commit lists the seven files, and `LICENSE` still hashes to `48255018b41fc0e965b1115af7e6779bc218bb8a6747d561da800d5022622aa2`.

---

## Done

After Task 6, the relicense is complete locally. Do **not** open a PR — hand back to the repo owner for review and approval.
