#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_config="${MIDDLEMAN_CONFIG:-${HOME}/.config/middleman/config.toml}"
clone_dir="${MIDDLEMAN_DEV_CLONE_DIR:-${repo_root}/tmp/dev-db-clone}"
clone_port="${MIDDLEMAN_DEV_CLONE_PORT:-8092}"

python3 - "$source_config" "$clone_dir" "$clone_port" <<'PY'
import os
import re
import shutil
import sqlite3
import stat
import sys
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:  # pragma: no cover
    import tomli as tomllib

source_config = Path(sys.argv[1]).expanduser().resolve()
clone_dir = Path(sys.argv[2]).expanduser().resolve()
clone_port = int(sys.argv[3])
if clone_port < 1 or clone_port > 65535:
    raise SystemExit(f"invalid MIDDLEMAN_DEV_CLONE_PORT {clone_port!r}")
if not source_config.exists():
    raise SystemExit(f"source config does not exist: {source_config}")

config_text = source_config.read_text()
config = tomllib.loads(config_text)
source_data_dir = config.get("data_dir")
if source_data_dir:
    source_data_dir = Path(source_data_dir).expanduser()
    if not source_data_dir.is_absolute():
        source_data_dir = (source_config.parent / source_data_dir).resolve()
else:
    middleman_home = os.environ.get("MIDDLEMAN_HOME")
    if middleman_home:
        source_data_dir = Path(middleman_home).expanduser().resolve()
    else:
        source_data_dir = Path.home() / ".config" / "middleman"

source_db = source_data_dir / "middleman.db"
if not source_db.exists():
    raise SystemExit(f"source database does not exist: {source_db}")

clone_dir.mkdir(parents=True, exist_ok=True)
clone_dir.chmod(stat.S_IRWXU)
clone_db = clone_dir / "middleman.db"
tmp_db = clone_dir / ".middleman.db.tmp"
if tmp_db.exists():
    tmp_db.unlink()
with sqlite3.connect(f"file:{source_db}?mode=ro", uri=True) as src:
    with sqlite3.connect(tmp_db) as dst:
        src.backup(dst)
os.replace(tmp_db, clone_db)
clone_db.chmod(stat.S_IRUSR | stat.S_IWUSR)

def set_string_key(text: str, key: str, value: str) -> str:
    escaped = value.replace('\\', '\\\\').replace('"', '\\"')
    line = f'{key} = "{escaped}"'
    pattern = re.compile(rf'(?m)^{re.escape(key)}\s*=.*$')
    if pattern.search(text):
        return pattern.sub(line, text, count=1)
    return f'{line}\n{text}'

def set_int_key(text: str, key: str, value: int) -> str:
    line = f'{key} = {value}'
    pattern = re.compile(rf'(?m)^{re.escape(key)}\s*=.*$')
    if pattern.search(text):
        return pattern.sub(line, text, count=1)
    return f'{line}\n{text}'

clone_config = clone_dir / "config.toml"
config_text = set_string_key(config_text, "data_dir", str(clone_dir))
config_text = set_int_key(config_text, "port", clone_port)
clone_config.write_text(config_text)
clone_config.chmod(stat.S_IRUSR | stat.S_IWUSR)
print(clone_config)
PY
