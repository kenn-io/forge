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
        self.assertIn(
            "== memory.current ==\n123",
            (self.diagnostics / "cgroup-before.txt").read_text(),
        )
        self.assertIn(
            "== memory.current ==\n123",
            (self.diagnostics / "cgroup-after.txt").read_text(),
        )

    def test_missing_child_returns_command_not_found(self) -> None:
        status, _ = self.run_child("", test_command=("definitely-not-a-command",))
        self.assertEqual(127, status)

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
