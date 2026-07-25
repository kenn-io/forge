# Frontend Unit CI Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve actionable process and cgroup evidence when the frontend unit job exits abruptly, while removing setup-vp version drift.

**Architecture:** Keep the existing Vitest command and 14-worker policy authoritative. Add best-effort diagnostic steps around it, capture the timed command's exact exit status through `tee`, and upload the resulting directory after both successful and failed runs.

**Tech Stack:** GitHub Actions YAML, Bash, Node.js diagnostic reports, cgroup v2, Vite+ 0.2.3, Vitest 4.1.9

## Global Constraints

- Do not change Vitest's pool, worker count, test selection, or retry policy.
- Do not suppress or reinterpret the frontend unit test exit status.
- Do not dump environment variables or credentials into diagnostic artifacts.
- Diagnostic collection and upload are best effort and must not fail the job.
- Keep PR #744 unchanged.
- Do not add tests that inspect workflow text or assert tool configuration.

---

### Task 1: Pin Vite+ Setup

**Files:**
- Modify: `.github/workflows/ci.yml:167-173`
- Modify: `.github/workflows/ci.yml:288-294`
- Modify: `.github/workflows/ci.yml:421-427`
- Modify: `.github/workflows/ci.yml:610-616`

**Interfaces:**
- Consumes: the root `package.json` dependency `vite-plus: "0.2.3"`
- Produces: four setup-vp invocations that install Vite+ 0.2.3 before running lockfile installation

- [ ] **Step 1: Add the explicit version to every non-container setup**

Add the same input to each existing `Setup Vite+` block:

```yaml
with:
  version: "0.2.3"
  node-version-file: package.json
  cache: false
  run-install: true
```

- [ ] **Step 2: Inspect the workflow diff**

Run:

```bash
git diff --check
git diff -- .github/workflows/ci.yml
```

Expected: four `version: "0.2.3"` additions and no unrelated changes.

- [ ] **Step 3: Commit the toolchain pin**

Run the repository-local `context-sync --commit` workflow, then:

```bash
git add .github/workflows/ci.yml
git commit -m "ci: pin Vite+ setup to the lockfile version" \
  -m "Keep setup-vp from selecting a newer bootstrap CLI than the repository dependency."
```

### Task 2: Capture Frontend Unit Termination Evidence

**Files:**
- Modify: `.github/workflows/ci.yml:274-297`

**Interfaces:**
- Consumes: cgroup v2 files under `/sys/fs/cgroup`, `/usr/bin/time`, Node's diagnostic-report flags, and the existing unit command
- Produces: `tmp/frontend-unit-diagnostics/{versions.txt,cgroup-before.txt,cgroup-after.txt,time.txt,vitest.log,node-reports/*}`

- [ ] **Step 1: Prepare the diagnostic directory and baseline evidence**

Before the test step, add a preparation step that creates
`tmp/frontend-unit-diagnostics/node-reports`, records `node`, `bun`, and local
`vp` versions, then writes any readable cgroup memory files:

```bash
set -u
diagnostics=tmp/frontend-unit-diagnostics
mkdir -p "$diagnostics/node-reports"
{
  node --version
  bun --version
  ./node_modules/.bin/vp --version
} > "$diagnostics/versions.txt"
for metric in memory.current memory.peak memory.max memory.events; do
  if [[ -r "/sys/fs/cgroup/$metric" ]]; then
    {
      echo "== $metric =="
      sed -n '1,120p' "/sys/fs/cgroup/$metric"
    } >> "$diagnostics/cgroup-before.txt"
  fi
done
```

Mark this telemetry step `continue-on-error: true`.

- [ ] **Step 2: Instrument Vitest without changing its result**

Replace the bare unit-test command with:

```yaml
env:
  NODE_OPTIONS: >-
    --report-on-fatalerror
    --report-uncaught-exception
    --report-exclude-env
    --report-directory=../tmp/frontend-unit-diagnostics/node-reports
run: |
  set +e
  cd frontend
  /usr/bin/time -v -o ../tmp/frontend-unit-diagnostics/time.txt \
    ../node_modules/.bin/vp test run --project unit \
    2>&1 | tee ../tmp/frontend-unit-diagnostics/vitest.log
  test_status=${PIPESTATUS[0]}
  exit "$test_status"
```

GitHub's Bash shell keeps `pipefail`; `set +e` allows the script to capture
`PIPESTATUS[0]` before exiting with the timed Vitest command's result.

- [ ] **Step 3: Capture post-test cgroup evidence**

Add an `if: always()` step that repeats the readable cgroup snapshot into
`cgroup-after.txt`. Mark it `continue-on-error: true` so telemetry cannot
override the test result.

- [ ] **Step 4: Upload diagnostics after every result**

Use the existing pinned artifact action:

```yaml
- name: Upload frontend unit diagnostics
  if: always()
  continue-on-error: true
  uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2
  with:
    name: frontend-unit-diagnostics
    path: tmp/frontend-unit-diagnostics/
    if-no-files-found: warn
    retention-days: 7
```

- [ ] **Step 5: Exercise the shell behavior**

Run controlled success and failure commands through the same
`/usr/bin/time | tee` status-capture pattern.

Expected:

- the success command exits 0 and writes both log and timing files;
- a command that exits 23 propagates exit 23 while still writing both files;
- the cgroup snapshot command exits 0 whether each optional metric exists or
  not;
- Node accepts every configured diagnostic-report option.

- [ ] **Step 6: Validate and commit diagnostics**

Run:

```bash
actionlint .github/workflows/ci.yml
git diff --check
scripts/context-sync --check
```

Run the repository-local `context-sync --commit` workflow, then commit:

```bash
git add .github/workflows/ci.yml
git commit -m "ci: preserve frontend unit termination evidence" \
  -m "Capture Node reports, process resource use, logs, and cgroup memory counters without changing Vitest failure semantics."
```

### Task 3: Document The Recovery Workflow

**Files:**
- Modify: `context/testing.md:116-123`

**Interfaces:**
- Consumes: the artifact and GitHub runner-debug behavior introduced in Task 2
- Produces: a durable maintainer procedure for a frontend unit run that exits without a summary

- [ ] **Step 1: Add the testing-context rule**

Document that the frontend unit artifact contains Vitest output, `/usr/bin/time`
resource usage, Node fatal reports, and pre/post cgroup memory counters. State
that a repeat silent exit should be rerun once with the repository variable
`ACTIONS_RUNNER_DEBUG=true`, followed by removal of that variable after the
runner diagnostic logs are downloaded.

- [ ] **Step 2: Run final verification**

Run:

```bash
actionlint .github/workflows/ci.yml
scripts/context-sync --check
git diff --check
git status --short
```

Inspect the complete branch diff from `origin/main` and confirm:

- the worker count remains 14;
- the unit command still selects `--project unit`;
- no retry or failure suppression was added to the test step;
- `--report-exclude-env` is present;
- telemetry steps cannot fail the job;
- all four setup-vp steps use 0.2.3;
- no private hostname, credential, or unrelated change appears.

- [ ] **Step 3: Commit the context update**

Run the repository-local `context-sync --commit` workflow, then:

```bash
git add context/testing.md
git commit -m "docs: record frontend CI diagnostic recovery" \
  -m "Keep the temporary runner-debug procedure and diagnostic artifact contract discoverable for future silent exits."
```
