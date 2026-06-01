#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# ///
"""List middleman dev-ephemeral instances across Codex worktrees."""

from __future__ import annotations

import argparse
import errno
import json
import os
import shlex
import subprocess
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class Check:
    alive: bool
    detail: str


@dataclass(frozen=True)
class Instance:
    status: str
    worktree: str
    run_dir: str
    branch: str
    status_file: str
    pid: str
    pid_check: Check
    backend_pid: str
    backend_pid_check: Check
    frontend_pid: str
    frontend_pid_check: Check
    backend_url: str
    backend_http_check: Check
    frontend_url: str
    frontend_http_check: Check
    reason: str


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="List middleman dev-ephemeral instances and health checks."
    )
    parser.add_argument(
        "--worktrees-root",
        default=str(Path.home() / ".codex" / "worktrees"),
        help="Root directory containing Codex git worktrees.",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=1.5,
        help="HTTP probe timeout in seconds.",
    )
    parser.add_argument(
        "--format",
        choices=("text", "json"),
        default="text",
        help="Output format.",
    )
    parser.add_argument(
        "--status",
        choices=("all", "live", "degraded", "stale", "invalid"),
        default="all",
        help="Filter rows by classification.",
    )
    parser.add_argument(
        "--exclude-worktree",
        action="append",
        default=[],
        help="Worktree path to exclude from output; may be passed more than once.",
    )
    parser.add_argument(
        "--emit-stop-commands",
        action="store_true",
        help="Print clean dev-ephemeral stop commands for matching non-invalid rows.",
    )
    return parser.parse_args()


def discover_status_files(worktrees_root: Path) -> list[Path]:
    if not worktrees_root.exists():
        return []
    return sorted(worktrees_root.glob("*/middleman/tmp/**/dev-ephemeral.json"))


def find_worktree(path: Path) -> Path:
    for parent in path.parents:
        if (parent / ".git").exists():
            return parent
    marker = "/tmp/"
    text = str(path)
    if marker in text:
        return Path(text.split(marker, 1)[0])
    return path.parent


def branch_name(worktree: Path) -> str:
    branch = run_git(worktree, ["branch", "--show-current"])
    if branch:
        return branch
    short_sha = run_git(worktree, ["rev-parse", "--short", "HEAD"])
    if short_sha:
        return f"detached HEAD ({short_sha})"
    return "unknown"


def run_git(worktree: Path, args: list[str]) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", str(worktree), *args],
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
    except OSError:
        return ""
    if result.returncode != 0:
        return ""
    return result.stdout.strip()


def pid_check(value: Any) -> Check:
    try:
        pid = int(value)
    except (TypeError, ValueError):
        return Check(False, "missing")
    if pid <= 0:
        return Check(False, "missing")
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return Check(False, "missing")
    except PermissionError:
        return Check(True, "alive:permission-denied")
    except OSError as exc:
        if exc.errno == errno.EPERM:
            return Check(True, "alive:permission-denied")
        return Check(False, f"error:{exc.errno}")
    return Check(True, "alive")


def http_check(url: Any, timeout: float) -> Check:
    if not isinstance(url, str) or not url:
        return Check(False, "missing")
    request = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            status = response.getcode()
    except urllib.error.HTTPError as exc:
        return Check(200 <= exc.code < 400, f"http:{exc.code}")
    except Exception as exc:
        return Check(False, type(exc).__name__)
    return Check(200 <= status < 400, f"http:{status}")


def make_instance(status_file: Path, timeout: float) -> Instance:
    worktree = find_worktree(status_file)
    run_dir = status_file.parent.name
    try:
        data = json.loads(status_file.read_text())
    except Exception as exc:
        failed = Check(False, type(exc).__name__)
        return Instance(
            status="invalid",
            worktree=str(worktree),
            run_dir=run_dir,
            branch=branch_name(worktree),
            status_file=str(status_file),
            pid="",
            pid_check=failed,
            backend_pid="",
            backend_pid_check=failed,
            frontend_pid="",
            frontend_pid_check=failed,
            backend_url="",
            backend_http_check=failed,
            frontend_url="",
            frontend_http_check=failed,
            reason=f"cannot parse status file: {type(exc).__name__}",
        )
    if not isinstance(data, dict):
        failed = Check(False, "not-object")
        return Instance(
            status="invalid",
            worktree=str(worktree),
            run_dir=run_dir,
            branch=branch_name(worktree),
            status_file=str(status_file),
            pid="",
            pid_check=failed,
            backend_pid="",
            backend_pid_check=failed,
            frontend_pid="",
            frontend_pid_check=failed,
            backend_url="",
            backend_http_check=failed,
            frontend_url="",
            frontend_http_check=failed,
            reason="status file JSON is not an object",
        )

    launcher = pid_check(data.get("pid"))
    backend_pid = pid_check(data.get("backend_pid"))
    frontend_pid = pid_check(data.get("frontend_pid"))
    backend_http = http_check(data.get("backend_url"), timeout)
    frontend_http = http_check(data.get("frontend_url"), timeout)

    pid_checks = (launcher, backend_pid, frontend_pid)
    http_checks = (backend_http, frontend_http)
    full_health = all(check.alive for check in (*pid_checks, *http_checks))
    any_health = any(check.alive for check in (*pid_checks, *http_checks))

    if full_health:
        status = "live"
        reason = ""
    elif any_health:
        status = "degraded"
        reason = summarize_failures(
            {
                "launcher pid": launcher,
                "backend pid": backend_pid,
                "frontend pid": frontend_pid,
                "backend url": backend_http,
                "frontend url": frontend_http,
            }
        )
    else:
        status = "stale"
        reason = "no recorded PIDs are alive and neither URL responds"

    return Instance(
        status=status,
        worktree=str(worktree),
        run_dir=run_dir,
        branch=branch_name(worktree),
        status_file=str(status_file),
        pid=str(data.get("pid", "")),
        pid_check=launcher,
        backend_pid=str(data.get("backend_pid", "")),
        backend_pid_check=backend_pid,
        frontend_pid=str(data.get("frontend_pid", "")),
        frontend_pid_check=frontend_pid,
        backend_url=str(data.get("backend_url", "")),
        backend_http_check=backend_http,
        frontend_url=str(data.get("frontend_url", "")),
        frontend_http_check=frontend_http,
        reason=reason,
    )


def summarize_failures(checks: dict[str, Check]) -> str:
    failures = [f"{name} {check.detail}" for name, check in checks.items() if not check.alive]
    return "; ".join(failures)


def instance_to_dict(instance: Instance) -> dict[str, Any]:
    return {
        "status": instance.status,
        "worktree": instance.worktree,
        "run_dir": instance.run_dir,
        "branch": instance.branch,
        "status_file": instance.status_file,
        "pid": instance.pid,
        "pid_status": instance.pid_check.detail,
        "backend_pid": instance.backend_pid,
        "backend_pid_status": instance.backend_pid_check.detail,
        "frontend_pid": instance.frontend_pid,
        "frontend_pid_status": instance.frontend_pid_check.detail,
        "backend_url": instance.backend_url,
        "backend_url_status": instance.backend_http_check.detail,
        "frontend_url": instance.frontend_url,
        "frontend_url_status": instance.frontend_http_check.detail,
        "reason": instance.reason,
    }


def print_text(instances: list[Instance]) -> None:
    if not instances:
        print("No dev-ephemeral status files found.")
        return

    for status in ("live", "degraded", "stale", "invalid"):
        rows = [instance for instance in instances if instance.status == status]
        if not rows:
            continue
        print(f"{status.upper()} ({len(rows)})")
        for instance in rows:
            print(f"- worktree: {instance.worktree}")
            print(f"  run: {instance.run_dir}")
            print(f"  branch: {instance.branch}")
            print(
                "  pids: "
                f"launcher {instance.pid} ({instance.pid_check.detail}), "
                f"backend {instance.backend_pid} ({instance.backend_pid_check.detail}), "
                f"frontend {instance.frontend_pid} ({instance.frontend_pid_check.detail})"
            )
            print(
                "  urls: "
                f"backend {instance.backend_url} ({instance.backend_http_check.detail}), "
                f"frontend {instance.frontend_url} ({instance.frontend_http_check.detail})"
            )
            if instance.reason:
                print(f"  reason: {instance.reason}")
        print()


def print_stop_commands(instances: list[Instance]) -> None:
    targets = [instance for instance in instances if instance.status != "invalid"]
    if not targets:
        print("No stoppable dev-ephemeral status files matched.")
        return

    print("STOP TARGETS")
    for instance in targets:
        print(f"- {instance.worktree}")
        print(f"  run: {instance.run_dir}")
        print(f"  status: {instance.status}")
        print(f"  status_file: {instance.status_file}")
        print(f"  backend: {instance.backend_url} ({instance.backend_http_check.detail})")
        print(f"  frontend: {instance.frontend_url} ({instance.frontend_http_check.detail})")
        if instance.reason:
            print(f"  reason: {instance.reason}")

    print()
    print("STOP COMMANDS")
    for instance in targets:
        print(f"go run ./tools/devephemeral -stop -status {shlex.quote(instance.status_file)}")


def filter_excluded_worktrees(instances: list[Instance], excluded: list[str]) -> list[Instance]:
    if not excluded:
        return instances
    excluded_paths = {str(Path(path).expanduser().resolve()) for path in excluded}
    return [
        instance
        for instance in instances
        if str(Path(instance.worktree).expanduser().resolve()) not in excluded_paths
    ]


def main() -> int:
    args = parse_args()
    status_files = discover_status_files(Path(args.worktrees_root).expanduser())
    instances = [make_instance(path, args.timeout) for path in status_files]
    instances = filter_excluded_worktrees(instances, args.exclude_worktree)
    if args.status != "all":
        instances = [instance for instance in instances if instance.status == args.status]

    if args.emit_stop_commands:
        print_stop_commands(instances)
    elif args.format == "json":
        print(json.dumps([instance_to_dict(instance) for instance in instances], indent=2))
    else:
        print_text(instances)
    return 0


if __name__ == "__main__":
    sys.exit(main())
