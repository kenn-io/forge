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

The change is limited to `.github/workflows/ci.yml` and the testing context:

- pin every non-container `Setup Vite+` step to the repository's `0.2.3`
  dependency version;
- instrument the `Frontend unit tests` job;
- upload the resulting diagnostics even when the test step fails;
- document the temporary GitHub runner-debug switch for a recurrence.

This change does not add test retries, suppress failures, alter Vitest's pool or
worker count, or change PR #744.

## Test Execution

Before running Vitest, the job creates
`tmp/frontend-unit-diagnostics/node-reports` and records tool versions plus a
pre-test cgroup snapshot when cgroup v2 memory files are readable.

The existing Vitest command continues to determine the job result. It runs
under `/usr/bin/time -v`, with combined output copied to a log. The shell
captures the timed command's pipeline status and exits with that exact status,
so logging cannot convert a failed test run to success.

`NODE_OPTIONS` enables reports for uncaught exceptions and Node fatal errors.
Reports use their default unique filenames under the diagnostics directory and
exclude environment variables. These reports are additive evidence; an
external `SIGKILL` may still prevent Node from writing one.

## Cgroup Evidence

Best-effort snapshots record the readable values of:

- `memory.current`
- `memory.peak`
- `memory.max`
- `memory.events`

The post-test snapshot uses `if: always()` so it runs after a failed Vitest
step. Missing cgroup v2 files do not fail the job. In particular,
`memory.events` can confirm whether the job cgroup observed an OOM condition or
an OOM kill.

## Artifact And Failure Semantics

The diagnostics directory is uploaded with `if: always()` and short retention.
Artifact collection is telemetry: its absence or an upload-service failure must
not override the test result. The Vitest exit status remains the only result of
the test step.

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
- execute the diagnostic shell fragments locally against success and failure
  commands to confirm exact exit-status propagation and best-effort cgroup
  handling;
- inspect the final diff for secrets, private hostnames, and unrelated changes.

No test will assert workflow text or duplicate the behavior of GitHub Actions.
