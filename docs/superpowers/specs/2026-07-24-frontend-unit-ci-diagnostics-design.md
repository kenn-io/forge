# Frontend Unit CI Diagnostics

## Purpose

The frontend unit job can currently exit after reporting passing test files but
before Vitest prints a final summary or assertion failure. A rerun of the same
commit can pass, which leaves the original termination unexplained.

This change will preserve enough process and container evidence to distinguish a
Node/Vitest fatal error from cgroup memory pressure or another abrupt exit. It
will also remove Vite+ setup-version drift. The existing 14-worker limit remains
unchanged while the new evidence is collected.

## Scope

The change is limited to the CI workflow, one standard-library Python helper,
its focused tests, and the testing context:

- pin every non-container `Setup Vite+` step to the repository's `0.2.3`
  dependency version;
- instrument the `Frontend unit tests` job;
- keep diagnostic orchestration out of workflow YAML;
- upload the resulting diagnostics even when the test step fails;
- document the temporary GitHub runner-debug switch for a recurrence.

This change does not add test retries, suppress failures, alter Vitest's pool or
worker count, or change PR #744.

## Components

`.github/workflows/ci.yml` invokes `python3 scripts/frontend_unit_ci.py` as the
only command in the authoritative frontend-unit test step. GitHub Actions still
owns artifact upload because its `if: always()` and `continue-on-error` behavior
are workflow concerns.

`scripts/frontend_unit_ci.py` owns diagnostic setup, tool-version capture,
cgroup snapshots, Node report configuration, test-process execution, output
streaming, and exit-status propagation. It uses only the Python standard
library and does not require a Python setup action.

`scripts/test_frontend_unit_ci.py` exercises the helper's owned behavior against
temporary directories and controlled child commands. It does not parse or
assert workflow text. The production module accepts paths and the child command
as function arguments for testability; its command-line entry point uses fixed
repository defaults and has no test-only environment switches.

## Test Execution

Before running Vitest, the job creates
`tmp/frontend-unit-diagnostics/node-reports` and records tool versions plus a
pre-test cgroup snapshot when cgroup v2 memory files are readable.

The existing Vitest command continues to determine the job result. It runs
under `/usr/bin/time -v`. The Python helper streams combined child output to
both the Actions console and `vitest.log`, then returns the child process's
exact exit status. There is no shell pipeline whose status can mask the test
result.

`NODE_OPTIONS` enables reports for uncaught exceptions and Node fatal errors.
Reports use their default unique filenames under the diagnostics directory and
exclude environment variables. These reports are additive evidence; an
external `SIGKILL` may still prevent Node from writing one.

## Error Handling

The helper verifies the diagnostic directories and output files before starting
the timed test process. If that preflight fails, it emits a concise warning and
runs the original Vitest command without `/usr/bin/time`, output capture, or
diagnostic `NODE_OPTIONS`.

Tool-version reads and cgroup snapshots are best effort. Their failures produce
diagnostic warnings but never replace the child process's exit status. A child
command that cannot be started returns the same conventional command-not-found
status as the previous shell invocation.

## Cgroup Evidence

Best-effort snapshots record the readable values of:

- `memory.current`
- `memory.peak`
- `memory.max`
- `memory.events`

The helper captures the post-test snapshot in a `finally` path so it runs after
a normal success, a test failure, or an execution exception. Missing cgroup v2
files do not fail the job. In particular, `memory.events` can confirm whether
the job cgroup observed an OOM condition or an OOM kill.

## Artifact And Failure Semantics

The diagnostics directory is uploaded with `if: always()` and short retention.
Artifact collection is telemetry: its absence or an upload-service failure must
not override the test result. The Vitest exit status remains the only result of
the test step.

If the helper cannot create and verify writable diagnostic paths, it invokes
the original `../node_modules/.bin/vp test run --project unit` command directly
from `frontend/`, without diagnostic environment changes. This fallback
preserves the pre-instrumentation behavior and prevents telemetry setup from
turning a successful test run into a failure.

The artifact may contain ordinary runner paths and process metadata, but Node
environment variables are excluded. It must not include secrets or a general
environment dump.

If the job again exits without a Vitest summary, maintainers can temporarily set
the repository variable `ACTIONS_RUNNER_DEBUG=true`, rerun the failed job, and
remove the variable after downloading the runner diagnostic logs. Runner debug
logging is not enabled persistently.

## Validation

Validation will:

- run `actionlint` against `.github/workflows/ci.yml`;
- run `scripts/context-sync --check`;
- run the focused Python tests for success and failure exit propagation, output
  streaming, cgroup snapshots, and unavailable-diagnostics fallback;
- execute the helper's command-line entry point against the frontend unit suite;
- inspect the final diff for secrets, private hostnames, and unrelated changes.

No test will assert workflow text or duplicate the behavior of GitHub Actions.
