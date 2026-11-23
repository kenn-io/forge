#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = ["fonttools[woff]==4.60.1"]
# ///
"""Regenerate the bundled MesloLGS NF WOFF2 files from the upstream TrueType files.

Run with `uv run scripts/build-meslo-fonts.py`.

Each face is split in two by Unicode range. The core file covers Latin text,
punctuation, arrows, math, and box drawing, and is small enough to inline
into the app, setup page, and website stylesheets for first paint. The
extended file holds every other glyph, mostly Nerd Font icons; only the
terminal loads it. The script prints both ranges in CSS `unicode-range`
form; the @font-face rules must use them.
"""

from __future__ import annotations

import hashlib
import io
import shutil
import urllib.parse
import urllib.request
from pathlib import Path

from fontTools import subset
from fontTools.ttLib import TTFont

UPSTREAM = "https://raw.githubusercontent.com/romkatv/powerlevel10k-media/145eb9fbc2f42ee408dacd9b22d8e6e0e553f83d"

# Upstream file name suffix, output style name, SHA-256 of the upstream file.
FACES = [
    ("Regular", "Regular", "d97946186e97f8d7c0139e8983abf40a1d2d086924f2c5dbf1c29bd8f2c6e57d"),
    ("Bold", "Bold", "b6c0199cf7c7483c8343ea020658925e6de0aeb318b89908152fcb4d19226003"),
    ("Italic", "Italic", "6f357bcbe2597704e157a915625928bca38364a89c22a4ac36e7a116dcd392ef"),
    ("Bold Italic", "Bold-Italic", "56b4131adecec052c4b324efb818dd326d586dbc316fc68f98f1cae2eb8d1220"),
]

CORE_RANGES = [
    (0x0000, 0x017F),  # Basic Latin, Latin-1, Latin Extended-A
    (0x0192, 0x0192),
    (0x02C6, 0x02DD),  # Spacing modifiers used by Latin-1 text
    (0x2000, 0x206F),  # General punctuation
    (0x20A0, 0x20CF),  # Currency
    (0x2100, 0x214F),  # Letterlike symbols
    (0x2190, 0x22FF),  # Arrows, math operators
    (0x2318, 0x2318),  # Keyboard symbols
    (0x2325, 0x2325),
    (0x2387, 0x2387),
    (0x238B, 0x238B),
    (0x23CE, 0x23CF),
    (0x2500, 0x25FF),  # Box drawing, block elements, geometric shapes
    (0x2713, 0x2718),  # Check and cross marks
    (0xFFFD, 0xFFFD),
]
EXTENDED_RANGES = []
_start = 0
for _lo, _hi in CORE_RANGES:
    if _lo > _start:
        EXTENDED_RANGES.append((_start, _lo - 1))
    _start = _hi + 1
EXTENDED_RANGES.append((_start, 0x10FFFF))

ROOT = Path(__file__).resolve().parent.parent
APP_DIR = ROOT / "frontend" / "src" / "fonts" / "meslo"
WEBSITE_DIR = ROOT / "website" / "fonts"
WEBSITE_STYLES = {"Regular", "Bold"}


def download(name: str, sha256: str) -> bytes:
    url = f"{UPSTREAM}/{urllib.parse.quote(f'MesloLGS NF {name}.ttf')}"
    with urllib.request.urlopen(url) as response:
        data = response.read()
    digest = hashlib.sha256(data).hexdigest()
    if digest != sha256:
        raise SystemExit(f"{url}: SHA-256 {digest} does not match {sha256}")
    return data


def write_subset(data: bytes, ranges: list[tuple[int, int]], out: Path) -> None:
    font = TTFont(io.BytesIO(data))
    cmap = font.getBestCmap()
    unicodes = [cp for cp in cmap if any(lo <= cp <= hi for lo, hi in ranges)]
    options = subset.Options()
    options.flavor = "woff2"
    options.layout_features = ["*"]
    options.name_IDs = ["*"]
    options.name_languages = ["*"]
    options.notdef_outline = True
    options.glyph_names = False
    options.drop_tables += ["FFTM", "PfEd"]
    subsetter = subset.Subsetter(options)
    subsetter.populate(unicodes=unicodes)
    subsetter.subset(font)
    font.flavor = "woff2"
    font.save(out)
    print(f"{out.relative_to(ROOT)}: {len(unicodes)} code points, {out.stat().st_size} bytes")


def css_ranges(ranges: list[tuple[int, int]]) -> str:
    return ", ".join(f"U+{lo:04X}" if lo == hi else f"U+{lo:04X}-{hi:04X}" for lo, hi in ranges)


def main() -> None:
    APP_DIR.mkdir(parents=True, exist_ok=True)
    for name, style, sha256 in FACES:
        data = download(name, sha256)
        core = APP_DIR / f"MesloLGS-NF-{style}.woff2"
        write_subset(data, CORE_RANGES, core)
        write_subset(data, EXTENDED_RANGES, APP_DIR / f"MesloLGS-NF-{style}-Extended.woff2")
        if style in WEBSITE_STYLES:
            shutil.copyfile(core, WEBSITE_DIR / core.name)
    print(f"core unicode-range: {css_ranges(CORE_RANGES)}")
    print(f"extended unicode-range: {css_ranges(EXTENDED_RANGES)}")


if __name__ == "__main__":
    main()
