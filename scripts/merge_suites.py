#!/usr/bin/env python3
"""
Merge Allure result files from per-suite subdirectories into a single flat directory.

For each *-result.json file a parentSuite label is injected (set to the suite
directory name, e.g. "load", "chaos") so the combined Allure report groups tests
by their originating suite. All other files (attachments, container JSONs, etc.)
are copied as-is. The history/ subdirectory is skipped — history is managed
separately by the deploy-report Makefile target.

Handles the double-nesting case where GCS downloads produce <suite>/<suite>/
instead of <suite>/.

Usage: merge_suites.py <results-dir> <merged-dir>

Exit codes:
  0  Suites merged successfully.
  1  No suite subdirectories found; caller should skip report generation.
  2  Unexpected error.
"""

import json
import shutil
import sys
from pathlib import Path


def inject_parent_suite(src: Path, suite: str, dst: Path) -> None:
    with src.open() as f:
        result = json.load(f)
    result["labels"] = [l for l in result.get("labels", []) if l.get("name") != "parentSuite"]
    result["labels"].append({"name": "parentSuite", "value": suite})
    with dst.open("w") as f:
        json.dump(result, f)


def merge_suites(results_dir: Path, merged_dir: Path) -> int:
    suite_dirs = [
        d for d in results_dir.iterdir()
        if d.is_dir() and d.name != merged_dir.name
    ]

    if not suite_dirs:
        print("No suite results found, skipping report", file=sys.stderr)
        return 1

    merged_dir.mkdir(parents=True, exist_ok=True)

    for suite_dir in suite_dirs:
        suite = suite_dir.name
        src = suite_dir
        # Handle double-nesting: <suite>/<suite>/
        if (suite_dir / suite).is_dir():
            src = suite_dir / suite

        for entry in src.iterdir():
            if entry.name == "history":
                continue
            if entry.is_file() and entry.name.endswith("-result.json"):
                inject_parent_suite(entry, suite, merged_dir / entry.name)
            elif entry.is_dir():
                shutil.copytree(entry, merged_dir / entry.name, dirs_exist_ok=True)
            else:
                shutil.copy2(entry, merged_dir / entry.name)

    return 0


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <results-dir> <merged-dir>", file=sys.stderr)
        sys.exit(2)

    try:
        sys.exit(merge_suites(Path(sys.argv[1]), Path(sys.argv[2])))
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(2)
