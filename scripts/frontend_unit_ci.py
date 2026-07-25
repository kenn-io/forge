"""Run frontend unit tests with best-effort CI diagnostics."""

import os
import subprocess
import sys
from pathlib import Path
from typing import Sequence


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


def _status(returncode: int) -> int:
    return 128 - returncode if returncode < 0 else returncode


def _run_direct(command: Sequence[str], cwd: Path) -> int:
    try:
        result = subprocess.run(
            command,
            cwd=cwd,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        sys.stdout.write(result.stdout)
        return _status(result.returncode)
    except FileNotFoundError:
        return 127
    except PermissionError:
        return 126


def _prepare_diagnostics(time_binary: Path, diagnostics_dir: Path) -> tuple[Path, Path, Path]:
    if not time_binary.is_file() or not os.access(time_binary, os.X_OK):
        raise OSError(f"{time_binary} is not executable")

    diagnostics_dir.mkdir(parents=True, exist_ok=True)
    node_reports = diagnostics_dir / "node-reports"
    node_reports.mkdir(exist_ok=True)
    time_file = diagnostics_dir / "time.txt"
    vitest_log = diagnostics_dir / "vitest.log"
    versions_file = diagnostics_dir / "versions.txt"
    time_file.touch()
    vitest_log.touch()
    versions_file.touch()
    return node_reports, time_file, vitest_log


def _record_versions(commands: Sequence[Sequence[str]], cwd: Path, destination: Path) -> None:
    with destination.open("w") as versions_file:
        for command in commands:
            try:
                result = subprocess.run(
                    command,
                    cwd=cwd,
                    check=False,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.STDOUT,
                    text=True,
                )
                output = result.stdout
            except (FileNotFoundError, PermissionError):
                output = f"{' '.join(command)}: unavailable\n"
            versions_file.write(output)


def _capture_cgroup_metrics(cgroup_root: Path, destination: Path) -> None:
    with destination.open("w") as metrics_file:
        for metric in CGROUP_METRICS:
            try:
                value = (cgroup_root / metric).read_text()
            except OSError:
                continue
            metrics_file.write(f"== {metric} ==\n{value}")
            if not value.endswith("\n"):
                metrics_file.write("\n")


def _diagnostic_node_options(node_reports: Path) -> str:
    return " ".join(
        (
            "--report-on-fatalerror",
            "--report-uncaught-exception",
            "--report-exclude-env",
            f"--report-directory={node_reports.resolve()}",
        )
    )


def run_frontend_unit(
    *,
    repo_root: Path = REPO_ROOT,
    diagnostics_dir: Path | None = None,
    cgroup_root: Path = CGROUP_ROOT,
    time_binary: Path = TIME_BINARY,
    test_command: Sequence[str] = TEST_COMMAND,
    version_commands: Sequence[Sequence[str]] = VERSION_COMMANDS,
) -> int:
    frontend_dir = repo_root / "frontend"
    diagnostics_dir = diagnostics_dir or repo_root / "tmp" / "frontend-unit-diagnostics"

    try:
        node_reports, time_file, vitest_log = _prepare_diagnostics(
            time_binary, diagnostics_dir
        )
    except OSError as error:
        print(
            f"warning: frontend diagnostics unavailable ({error}); running tests directly",
            file=sys.stderr,
        )
        return _run_direct(test_command, frontend_dir)

    _record_versions(version_commands, frontend_dir, diagnostics_dir / "versions.txt")
    _capture_cgroup_metrics(cgroup_root, diagnostics_dir / "cgroup-before.txt")

    environment = os.environ.copy()
    existing_options = environment.get("NODE_OPTIONS", "")
    environment["NODE_OPTIONS"] = " ".join(
        option
        for option in (existing_options, _diagnostic_node_options(node_reports))
        if option
    )

    try:
        try:
            process = subprocess.Popen(
                [str(time_binary), "-v", "-o", str(time_file), "--", *test_command],
                cwd=frontend_dir,
                env=environment,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
            )
        except OSError as error:
            print(
                f"warning: frontend diagnostics unavailable ({error}); running tests directly",
                file=sys.stderr,
            )
            return _run_direct(test_command, frontend_dir)

        assert process.stdout is not None
        with vitest_log.open("w") as log_file:
            for line in process.stdout:
                sys.stdout.write(line)
                log_file.write(line)
        return _status(process.wait())
    finally:
        _capture_cgroup_metrics(cgroup_root, diagnostics_dir / "cgroup-after.txt")


def main() -> int:
    return run_frontend_unit()


if __name__ == "__main__":
    raise SystemExit(main())
