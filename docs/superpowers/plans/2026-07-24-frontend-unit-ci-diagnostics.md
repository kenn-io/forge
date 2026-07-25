# Frontend Unit CI Python Runner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the frontend-unit diagnostic Bash blocks with one small, standard-library Python runner without changing Vitest selection, concurrency, or exit behavior.

**Architecture:** A focused Python module owns diagnostic preflight, tool and cgroup capture, timed Vitest execution, output streaming, and exact status propagation. The GitHub Actions job invokes that module in one line and retains only the platform-owned `always()` artifact upload behavior.

**Tech Stack:** Python 3 standard library, `unittest`, GitHub Actions YAML, `/usr/bin/time`, Node.js diagnostic reports, cgroup v2, Vite+ 0.2.3, Vitest 4.1.9

## Global Constraints

- Keep the helper standard-library-only and runnable with the runner's existing `python3`.
- Do not add a Python setup action or a Go toolchain to the frontend-unit job.
- Keep `../node_modules/.bin/vp test run --project unit` as the authoritative test command.
- Do not change Vitest's pool, 14-worker limit, test selection, or retry policy.
- Return the child process's exact status; return 127 when the command cannot be found.
- Diagnostic collection and upload are best effort and must not replace the test result.
- Exclude environment variables from Node diagnostic reports and never dump the environment.
- Keep the production CLI fixed to repository defaults; do not add test-only CLI flags or environment switches.
- Test the Python behavior, not workflow text, GitHub Actions,
  `/usr/bin/time` formatting, or Python standard-library behavior.
- Keep PR #744 unchanged.

---

### Task 1: Add The Frontend Unit Diagnostic Runner

**Files:**
- Create: `scripts/frontend_unit_ci.py`
- Create: `scripts/test_frontend_unit_ci.py`

**Interfaces:**
- Consumes: repository root, diagnostics directory, cgroup root, `/usr/bin/time`, version commands, and the existing Vitest command
- Produces: `run_frontend_unit(...) -> int`, `main() -> int`, and `tmp/frontend-unit-diagnostics/{versions.txt,cgroup-before.txt,cgroup-after.txt,time.txt,vitest.log,node-reports/*}`

- [ ] **Step 1: Write focused failing tests for authoritative process behavior**

Create `scripts/test_frontend_unit_ci.py` with `unittest` cases that invoke real
controlled Python child processes:

```python
import contextlib
import io
import sys
import tempfile
import unittest
from pathlib import Path

from scripts.frontend_unit_ci import run_frontend_unit


class FrontendUnitCITest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        self.root = Path(self.temp_dir.name)
        (self.root / "frontend").mkdir()
        self.diagnostics = self.root / "tmp" / "frontend-unit-diagnostics"

    def run_child(self, source: str, **overrides: object) -> tuple[int, str]:
        options = {
            "repo_root": self.root,
            "diagnostics_dir": self.diagnostics,
            "test_command": (sys.executable, "-c", source),
            "version_commands": (),
        }
        options.update(overrides)
        output = io.StringIO()
        warnings = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(warnings):
            status = run_frontend_unit(**options)
        return status, output.getvalue()

    def test_streams_output_and_preserves_success(self) -> None:
        status, output = self.run_child("print('child output')")
        self.assertEqual(0, status)
        self.assertIn("child output", output)
        self.assertIn("child output", (self.diagnostics / "vitest.log").read_text())
        self.assertTrue((self.diagnostics / "time.txt").read_text())

    def test_preserves_failure_status(self) -> None:
        status, _ = self.run_child("import sys; print('failed'); sys.exit(23)")
        self.assertEqual(23, status)
        self.assertIn("failed", (self.diagnostics / "vitest.log").read_text())
```

Add separate tests for:

```python
def test_unavailable_diagnostics_runs_direct_command(self) -> None:
    blocked_parent = self.root / "blocked"
    blocked_parent.write_text("not a directory")
    status, output = self.run_child(
        "print('direct fallback')",
        diagnostics_dir=blocked_parent / "diagnostics",
    )
    self.assertEqual(0, status)
    self.assertIn("direct fallback", output)

def test_captures_readable_cgroup_metrics_before_and_after(self) -> None:
    cgroup_root = self.root / "cgroup"
    cgroup_root.mkdir()
    (cgroup_root / "memory.current").write_text("123\n")
    status, _ = self.run_child("pass", cgroup_root=cgroup_root)
    self.assertEqual(0, status)
    self.assertIn("== memory.current ==\n123", (self.diagnostics / "cgroup-before.txt").read_text())
    self.assertIn("== memory.current ==\n123", (self.diagnostics / "cgroup-after.txt").read_text())

def test_missing_child_returns_command_not_found(self) -> None:
    status, _ = self.run_child("", test_command=("definitely-not-a-command",))
    self.assertEqual(127, status)
```

- [ ] **Step 2: Run the tests and confirm the missing helper is the reason for failure**

Run:

```bash
python3 -m unittest scripts/test_frontend_unit_ci.py
```

Expected: FAIL because `scripts.frontend_unit_ci` or
`run_frontend_unit` does not exist.

- [ ] **Step 3: Implement the minimal standard-library runner**

Create `scripts/frontend_unit_ci.py` with these fixed production defaults:

```python
REPO_ROOT = Path(__file__).resolve().parents[1]
CGROUP_ROOT = Path("/sys/fs/cgroup")
TIME_BINARY = Path("/usr/bin/time")
TEST_COMMAND = ("../node_modules/.bin/vp", "test", "run", "--project", "unit")
VERSION_COMMANDS = (
    ("node", "--version"),
    ("bun", "--version"),
    ("./node_modules/.bin/vp", "--version"),
)
CGROUP_METRICS = ("memory.current", "memory.peak", "memory.max", "memory.events")
```

Expose one orchestration function with injectable values used directly by the
tests:

```python
def run_frontend_unit(
    *,
    repo_root: Path = REPO_ROOT,
    diagnostics_dir: Path | None = None,
    cgroup_root: Path = CGROUP_ROOT,
    time_binary: Path = TIME_BINARY,
    test_command: Sequence[str] = TEST_COMMAND,
    version_commands: Sequence[Sequence[str]] = VERSION_COMMANDS,
) -> int:
```

Keep its control flow linear:

1. Derive `frontend/` and the default diagnostics directory from `repo_root`.
2. Verify `/usr/bin/time` is executable, then create `node-reports/`,
   `time.txt`, and `vitest.log` before starting tests.
3. If preflight raises `OSError`, write one warning to stderr and call the
   direct-command function without timing, logging, or diagnostic
   `NODE_OPTIONS`.
4. Record each version command independently; store its combined output or a
   concise unavailable message in `versions.txt`.
5. Capture every readable cgroup metric into `cgroup-before.txt`.
6. Add these exact child-only Node options:

```text
--report-on-fatalerror
--report-uncaught-exception
--report-exclude-env
--report-directory=<absolute node-reports path>
```

7. Run `/usr/bin/time -v -o <time.txt> -- <test command>` from `frontend/`
   with stderr merged into stdout. Stream each line to both `sys.stdout` and
   `vitest.log`.
8. In `finally`, capture readable metrics into `cgroup-after.txt`.
9. Return the timed process status unchanged.

If starting `/usr/bin/time` raises `OSError` after preflight, emit the same
concise warning and use the direct-command path without diagnostic
`NODE_OPTIONS`.

Use a small direct-command function for the fallback:

```python
def _run_direct(command: Sequence[str], cwd: Path) -> int:
    try:
        returncode = subprocess.run(command, cwd=cwd, check=False).returncode
        return 128 - returncode if returncode < 0 else returncode
    except FileNotFoundError:
        return 127
    except PermissionError:
        return 126
```

The CLI remains fixed:

```python
def main() -> int:
    return run_frontend_unit()


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 4: Run focused tests and make them pass**

Run:

```bash
python3 -m unittest scripts/test_frontend_unit_ci.py
```

Expected: all tests pass with no warnings or errors.

- [ ] **Step 5: Add the best-effort evidence tests**

Add focused cases proving the remaining owned behavior:

```python
def test_missing_version_command_does_not_change_success(self) -> None:
    status, _ = self.run_child(
        "pass",
        version_commands=(("definitely-not-a-command", "--version"),),
    )
    self.assertEqual(0, status)
    self.assertIn("unavailable", (self.diagnostics / "versions.txt").read_text())

def test_failure_still_captures_post_test_cgroup_metrics(self) -> None:
    cgroup_root = self.root / "cgroup"
    cgroup_root.mkdir()
    (cgroup_root / "memory.events").write_text("oom_kill 0\n")
    status, _ = self.run_child(
        "import sys; sys.exit(23)",
        cgroup_root=cgroup_root,
    )
    self.assertEqual(23, status)
    after = (self.diagnostics / "cgroup-after.txt").read_text()
    self.assertIn("== memory.events ==\noom_kill 0", after)
    self.assertNotIn("memory.max", after)

def test_child_receives_environment_safe_node_report_options(self) -> None:
    source = (
        "import os; "
        "print(os.environ.get('NODE_OPTIONS', '')); "
        "assert '--report-exclude-env' in os.environ['NODE_OPTIONS']"
    )
    status, _ = self.run_child(source)
    self.assertEqual(0, status)
    log = (self.diagnostics / "vitest.log").read_text()
    self.assertIn("--report-exclude-env", log)
    self.assertIn(str((self.diagnostics / "node-reports").resolve()), log)
```

Do not assert `/usr/bin/time` formatting, hard-coded version output, workflow
text, or Python subprocess behavior.

- [ ] **Step 6: Run the full Python helper test file**

Run:

```bash
python3 -m unittest scripts/test_frontend_unit_ci.py
```

Expected: all tests pass.

- [ ] **Step 7: Validate and commit the helper**

Run:

```bash
python3 -m py_compile scripts/frontend_unit_ci.py scripts/test_frontend_unit_ci.py
git diff --check
scripts/context-sync --check
```

Run the repository-local `context-sync --commit` workflow, then:

```bash
git add scripts/frontend_unit_ci.py scripts/test_frontend_unit_ci.py
git commit -m "ci: move frontend unit diagnostics into Python" \
  -m "Keep process orchestration testable and out of workflow YAML while preserving the original Vitest result."
```

### Task 2: Reduce The Workflow To Invocation And Upload

**Files:**
- Modify: `.github/workflows/ci.yml:297-357`
- Verify: `context/testing.md:116-126`

**Interfaces:**
- Consumes: `scripts/frontend_unit_ci.py::main() -> int`
- Produces: a frontend-unit job with one authoritative Python invocation and one best-effort GitHub artifact upload

- [ ] **Step 1: Replace the three diagnostic Bash blocks**

Remove `Prepare frontend unit diagnostics`, the multiline Bash body under
`Run frontend unit tests`, and `Capture frontend unit cgroup diagnostics`.
Keep one authoritative step:

```yaml
- name: Run frontend unit tests
  run: python3 scripts/frontend_unit_ci.py
```

Keep the existing upload step unchanged:

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

- [ ] **Step 2: Run the actual helper entry point**

Run:

```bash
python3 scripts/frontend_unit_ci.py
```

Expected: the full frontend unit suite passes, its output appears in the
terminal and `tmp/frontend-unit-diagnostics/vitest.log`, and timing plus
pre/post cgroup files are created.

- [ ] **Step 3: Run workflow and repository validation**

Run:

```bash
actionlint .github/workflows/ci.yml
python3 -m unittest scripts/test_frontend_unit_ci.py
python3 -m py_compile scripts/frontend_unit_ci.py scripts/test_frontend_unit_ci.py
scripts/context-sync --check
git diff --check
```

Inspect the complete `origin/main...HEAD` diff and confirm:

- the workflow contains no diagnostic Bash block;
- the job invokes only `python3 scripts/frontend_unit_ci.py` before upload;
- the 14-worker policy and `--project unit` selection are unchanged;
- no retry or test-result suppression was added;
- all four setup-vp steps remain pinned to 0.2.3;
- Node reports exclude environment variables;
- `context/testing.md` still describes the artifact and bounded
  `ACTIONS_RUNNER_DEBUG` recovery procedure accurately;
- no credential, private hostname, runner secret, or unrelated change appears.

- [ ] **Step 4: Commit the workflow simplification**

Run the repository-local `context-sync --commit` workflow, then:

```bash
git add .github/workflows/ci.yml
git commit -m "ci: keep frontend unit workflow declarative" \
  -m "Delegate diagnostic collection and exact test-status handling to the tested Python runner."
```

- [ ] **Step 5: Perform final review**

Review the final branch against
`docs/superpowers/specs/2026-07-24-frontend-unit-ci-diagnostics-design.md`.
Resolve all critical and important findings, rerun Step 3, and leave the
worktree clean.
