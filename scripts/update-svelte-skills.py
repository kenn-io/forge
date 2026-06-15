#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
import shutil
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path


REPO_API_ROOT = "https://api.github.com/repos/sveltejs/ai-tools/contents"
DEFAULT_REF = "e429cd783992de3d39770aab480c5489cd0a5dca"
SKILLS_API_PATH = "tools/skills"
MANIFEST_NAME = ".svelte-managed.json"
SVELTE_MCP_VERSION = "0.1.24"

TARGETS = {
    "codex": Path(".agents/skills"),
    "claude": Path(".claude/skills"),
}


@dataclass(frozen=True)
class RemoteEntry:
    entry_type: str
    path: str
    name: str
    download_url: str | None = None


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Sync Svelte AI skills from sveltejs/ai-tools into repo-local skills/"
            " and manage per-skill symlinks for Codex and Claude."
        ),
        epilog=(
            "Notes:\n"
            "- skills/ is checked-in source of truth.\n"
            "- .agents/skills/ and .claude/skills/ entries are managed as symlinks.\n"
            "- Existing non-symlink paths at target locations are preserved as local overrides.\n"
            "- Default sync source is pinned to an immutable upstream commit.\n"
            "- Pass --ref explicitly if you intentionally want a different upstream ref.\n"
            "- If an older clone still has generated directories where tracked symlinks should land, remove those directories once and rerun this command."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--ref",
        default=DEFAULT_REF,
        help=(
            "Git ref to read from the upstream repository. Defaults to pinned "
            f"commit {DEFAULT_REF}."
        ),
    )
    parser.add_argument(
        "--target",
        choices=("all", "codex", "claude"),
        default="all",
        help="Which agent skill targets to update.",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print planned changes without writing files.",
    )
    return parser.parse_args()


def repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def selected_targets(target_arg: str) -> dict[str, Path]:
    if target_arg == "all":
        return TARGETS.copy()
    return {target_arg: TARGETS[target_arg]}


def fetch_json(url: str) -> object:
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "User-Agent": "middleman-svelte-skills",
        },
    )
    with urllib.request.urlopen(request) as response:
        return json.load(response)


def contents_url(api_path: str, ref: str) -> str:
    quoted_path = urllib.parse.quote(api_path.strip("/"), safe="/")
    return f"{REPO_API_ROOT}/{quoted_path}?ref={urllib.parse.quote(ref, safe='')}"


def list_remote_directory(api_path: str, ref: str) -> list[RemoteEntry]:
    payload = fetch_json(contents_url(api_path, ref))
    if not isinstance(payload, list):
        raise RuntimeError(f"Unexpected API response for {api_path}")

    entries: list[RemoteEntry] = []
    for entry in payload:
        entry_type = entry.get("type")
        path = entry.get("path")
        name = entry.get("name")
        download_url = entry.get("download_url")

        if not isinstance(entry_type, str) or not isinstance(path, str) or not isinstance(name, str):
            raise RuntimeError(f"Malformed entry in GitHub API response for {api_path}")

        if download_url is not None and not isinstance(download_url, str):
            raise RuntimeError(f"Malformed download URL for {path}")

        entries.append(
            RemoteEntry(
                entry_type=entry_type,
                path=path,
                name=name,
                download_url=download_url,
            )
        )

    return entries


def download_file(url: str, destination: Path) -> None:
    request = urllib.request.Request(url, headers={"User-Agent": "middleman-svelte-skills"})
    with urllib.request.urlopen(request) as response, destination.open("wb") as output:
        shutil.copyfileobj(response, output)


def download_directory(api_path: str, destination: Path, ref: str) -> None:
    entries = list_remote_directory(api_path, ref)
    destination.mkdir(parents=True, exist_ok=True)

    for entry in entries:
        target_path = destination / entry.name
        if entry.entry_type == "dir":
            download_directory(entry.path, target_path, ref)
            continue

        if entry.entry_type != "file" or entry.download_url is None:
            raise RuntimeError(f"Unsupported entry in upstream skills tree: {entry.path}")

        target_path.parent.mkdir(parents=True, exist_ok=True)
        download_file(entry.download_url, target_path)


def normalize_markdown_code_examples(text: str) -> str:
    return text.replace("+++", "")


def apply_repo_skill_overrides(skill_name: str, source_dir: Path) -> None:
    for markdown_path in source_dir.rglob("*.md"):
        markdown_path.write_text(
            normalize_markdown_code_examples(markdown_path.read_text()),
        )

    if skill_name != "svelte-code-writer":
        return

    skill_path = source_dir / "SKILL.md"
    text = skill_path.read_text()
    text = text.replace(
        "You have access to `@sveltejs/mcp` CLI for Svelte-specific assistance. Use these commands via `npx`:",
        f"Use the repo-installed `@sveltejs/mcp@{SVELTE_MCP_VERSION}` CLI for Svelte-specific assistance:\n\n"
        "```bash\n"
        "vp exec svelte-mcp <command>\n"
        "```\n\n"
        "Run these commands from the repository root. Do not use `npx`, `npm`, or `bun x` for this tool.",
    )
    text = text.replace("npx @sveltejs/mcp ", "vp exec svelte-mcp ")
    text = text.replace(
        'vp exec svelte-mcp get-documentation "$state,$derived,$effect"',
        "vp exec svelte-mcp get-documentation '$state,$derived,$effect'",
    )
    text = text.replace(
        "# Analyze inline code (escape $ as \\$)\n"
        "vp exec svelte-mcp svelte-autofixer '<script>let count = \\$state(0);</script>'",
        "# Analyze inline code\n"
        "vp exec svelte-mcp svelte-autofixer '<script>let count = $state(0);</script>'",
    )
    text = text.replace(
        "vp exec svelte-mcp svelte-autofixer ./src/lib/Component.svelte",
        "vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/Component.svelte",
    )
    text = text.replace(
        "**Important:** When passing code with runes (`$state`, `$derived`, etc.) via the terminal, escape the `$` character as `\\$` to prevent shell variable substitution.",
        "**Important:** When passing code with runes (`$state`, `$derived`, etc.) via the terminal, wrap inline code in single quotes. If you use double quotes, escape `$` as `\\$` to prevent shell variable substitution.",
    )
    text = text.replace(
        "1. **Uncertain about syntax?** Run `list-sections` then `get-documentation` for relevant topics\n"
        "2. **Reviewing/debugging?** Run `svelte-autofixer` on the code to detect issues",
        "1. **Uncertain about syntax?** Run `list-sections` then `get-documentation` for relevant topics with `vp exec svelte-mcp`\n"
        "2. **Reviewing/debugging?** Run `svelte-autofixer` on the code with `vp exec svelte-mcp` to detect issues",
    )
    skill_path.write_text(text)


def install_directory(source_dir: Path, target_dir: Path, dry_run: bool) -> None:
    action = "replace" if target_dir.exists() or target_dir.is_symlink() else "create"
    print(f"{action}: {target_dir}")

    if dry_run:
        return

    target_dir.parent.mkdir(parents=True, exist_ok=True)
    backup_dir = target_dir.parent / f".{target_dir.name}.bak"

    if backup_dir.exists():
        if backup_dir.is_symlink():
            backup_dir.unlink()
        else:
            shutil.rmtree(backup_dir)

    with tempfile.TemporaryDirectory(dir=str(target_dir.parent), prefix=f".{target_dir.name}.tmp-") as tmp:
        staged_dir = Path(tmp) / target_dir.name
        shutil.copytree(source_dir, staged_dir)

        try:
            if target_dir.exists() or target_dir.is_symlink():
                target_dir.rename(backup_dir)

            staged_dir.rename(target_dir)

            if backup_dir.exists():
                if backup_dir.is_symlink():
                    backup_dir.unlink()
                else:
                    shutil.rmtree(backup_dir)
        except Exception:
            if backup_dir.exists() and not target_dir.exists() and not target_dir.is_symlink():
                backup_dir.rename(target_dir)
            raise


def manifest_path(shared_root: Path) -> Path:
    return shared_root / MANIFEST_NAME


def read_managed_skills(shared_root: Path) -> set[str]:
    path = manifest_path(shared_root)
    if not path.is_file():
        return set()

    payload = json.loads(path.read_text())
    if not isinstance(payload, dict):
        raise RuntimeError(f"Malformed managed skills manifest: {path}")

    skills = payload.get("skills")
    if not isinstance(skills, list) or not all(isinstance(skill, str) for skill in skills):
        raise RuntimeError(f"Malformed managed skills manifest: {path}")

    return set(skills)


def write_managed_skills(shared_root: Path, skill_names: set[str], dry_run: bool) -> None:
    path = manifest_path(shared_root)
    print(f"write manifest: {path}")
    if dry_run:
        return

    shared_root.mkdir(parents=True, exist_ok=True)
    payload = json.dumps({"skills": sorted(skill_names)}, indent=2) + "\n"
    with tempfile.NamedTemporaryFile(
        dir=str(shared_root),
        prefix=f".{MANIFEST_NAME}.",
        delete=False,
        mode="w",
        encoding="utf-8",
    ) as temp_file:
        temp_file.write(payload)
        temp_path = Path(temp_file.name)

    temp_path.replace(path)


def prune_shared_skills(shared_root: Path, previous_managed: set[str], current_managed: set[str], dry_run: bool) -> None:
    for skill_name in sorted(previous_managed - current_managed):
        entry = shared_root / skill_name
        if not entry.exists() and not entry.is_symlink():
            continue

        print(f"prune shared: {entry}")
        if dry_run:
            continue

        if entry.is_symlink() or entry.is_file():
            entry.unlink()
        else:
            shutil.rmtree(entry)


def prune_target_links(target_root: Path, shared_root: Path, previous_managed: set[str], current_managed: set[str], dry_run: bool) -> None:
    if not target_root.exists():
        return

    for skill_name in sorted(previous_managed - current_managed):
        entry = target_root / skill_name
        if not entry.is_symlink():
            continue

        resolved = (entry.parent / entry.readlink()).resolve()
        expected_target = (shared_root / skill_name).resolve()
        if resolved != expected_target:
            continue

        print(f"prune link: {entry}")
        if not dry_run:
            entry.unlink()


def ensure_symlink(link_path: Path, target_path: Path, dry_run: bool) -> None:
    link_parent = link_path.parent
    relative_target = Path(
        os.path.relpath(target_path.resolve(), start=link_parent.resolve())
    )

    if link_path.is_symlink():
        current_target = Path(link_path.readlink())
        if current_target == relative_target:
            print(f"ok: {link_path} -> {relative_target}")
            return

        print(f"relink: {link_path} -> {relative_target}")
        if not dry_run:
            link_path.unlink()
            link_path.symlink_to(relative_target, target_is_directory=True)
        return

    if link_path.exists():
        print(f"preserve override: {link_path}")
        return

    print(f"link: {link_path} -> {relative_target}")
    if dry_run:
        return

    link_parent.mkdir(parents=True, exist_ok=True)
    link_path.symlink_to(relative_target, target_is_directory=True)


def main() -> int:
    args = parse_args()
    root = repo_root()
    targets = selected_targets(args.target)
    shared_root = root / "skills"

    print(f"repo: {root}")
    print(f"upstream ref: {args.ref}")
    print("targets:")
    for name, rel_path in targets.items():
        print(f"  - {name}: {root / rel_path}")
    if args.dry_run:
        print("mode: dry-run")

    upstream_skills = sorted(
        [entry for entry in list_remote_directory(SKILLS_API_PATH, args.ref) if entry.entry_type == "dir"],
        key=lambda entry: entry.name,
    )
    skill_names = [entry.name for entry in upstream_skills]
    print("skills:")
    for skill_name in skill_names:
        print(f"  - {skill_name}")

    with tempfile.TemporaryDirectory(prefix="svelte-skills-") as temp_dir:
        workspace = Path(temp_dir)
        extracted: dict[str, Path] = {}
        skill_name_set = set(skill_names)
        previous_managed = read_managed_skills(shared_root)

        for skill in upstream_skills:
            print(f"fetch: {skill.name}")
            local_dir = workspace / skill.name
            download_directory(skill.path, local_dir, args.ref)

            if not (local_dir / "SKILL.md").is_file():
                raise RuntimeError(f"{skill.name}: missing SKILL.md")

            apply_repo_skill_overrides(skill.name, local_dir)
            extracted[skill.name] = local_dir

        for skill_name, source_dir in extracted.items():
            install_directory(source_dir, shared_root / skill_name, args.dry_run)

        for target_name, target_rel_path in targets.items():
            print(f"link target: {target_name}")
            target_root = root / target_rel_path
            for skill_name in skill_names:
                ensure_symlink(
                    target_root / skill_name,
                    shared_root / skill_name,
                    args.dry_run,
                )

            prune_target_links(
                target_root,
                shared_root,
                previous_managed,
                skill_name_set,
                args.dry_run,
            )

        prune_shared_skills(shared_root, previous_managed, skill_name_set, args.dry_run)
        write_managed_skills(shared_root, skill_name_set, args.dry_run)

    print("done")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except urllib.error.HTTPError as exc:
        print(f"error: upstream fetch failed with HTTP {exc.code}: {exc.reason}", file=sys.stderr)
        raise SystemExit(1)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
