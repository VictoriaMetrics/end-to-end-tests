#!/usr/bin/env python3
"""
Merge Allure result files from per-suite subdirectories into a single
allure-results directory suitable for Allure v3.

Output layout:
  <merged-dir>/
    allure-results/     ← single flat results dir; Allure reads from here
      *-result.json     ← parentSuite label injected per source suite
      *-container.json
      attachments/
      ...

Each *-result.json receives a parentSuite label set to the suite directory name
(e.g. "load", "chaos") so the combined report groups tests by suite. Its
historyId/testCaseId are also rescoped per suite, since Allure otherwise
collapses identically-named tests from different suites (e.g. the same test
run across every Kubernetes version in the matrix) into a single entry.

Result files use random UUIDs so merging into a flat directory is collision-safe.

The history/ subdirectory inside each source suite dir is skipped — history is
managed separately by the deploy-report Makefile target.

Double-nesting (<suite>/<suite>/) and an allure-results subdir in the source
are both handled transparently.

Usage: merge_suites.py <results-dir> <merged-dir>

Exit codes:
  0  Suites merged successfully.
  1  No suite subdirectories found; caller should skip report generation.
  2  Unexpected error.
"""

import hashlib
import json
import shutil
import sys
from pathlib import Path


def inject_parent_suite(src: Path, suite: str, dst_dir: Path) -> None:
    with src.open() as f:
        result = json.load(f)
    result["labels"] = [
        l for l in result.get("labels", []) if l.get("name") != "parentSuite"
    ]
    result["labels"].append({"name": "parentSuite", "value": suite})
    # Allure groups results by historyId/testCaseId across the whole report.
    # Since the same test name runs identically in every suite (e.g. each
    # Kubernetes version matrix entry), these ids collide and Allure
    # collapses all but one suite's execution into a single test entry.
    # Scope them to the suite so each suite's run is kept as a distinct test.
    new_history_id = None
    for key in ("historyId", "testCaseId"):
        value = result.get(key)
        if value:
            scoped = hashlib.md5(f"{suite}:{value}".encode()).hexdigest()
            result[key] = scoped
            if key == "historyId":
                new_history_id = scoped
    # The Go writer names *-result.json files after the original historyId,
    # which is identical across suites for "the same" test. Writing every
    # suite's file under that unscoped name would silently overwrite the
    # previous suite's result in the flat merged directory before Allure
    # ever sees a collision, so the destination filename must be re-derived
    # from the rescoped id too.
    dst_name = f"{new_history_id}-result.json" if new_history_id else src.name
    with (dst_dir / dst_name).open("w") as f:
        json.dump(result, f)


def merge_suites(results_dir: Path, merged_dir: Path) -> int:
    suite_dirs = [
        d for d in results_dir.iterdir() if d.is_dir() and d.name != merged_dir.name
    ]

    if not suite_dirs:
        print("No suite results found, skipping report", file=sys.stderr)
        return 1

    merged_dir.mkdir(parents=True, exist_ok=True)
    counts: dict = {}

    for suite_dir in suite_dirs:
        suite = suite_dir.name
        src = suite_dir

        # Handle double-nesting: <suite>/<suite>/
        if (src / suite).is_dir():
            src = src / suite

        # Handle a reporter that writes into an allure-results subdir
        if (src / "allure-results").is_dir():
            src = src / "allure-results"

        out_dir = merged_dir / "allure-results"
        out_dir.mkdir(parents=True, exist_ok=True)

        result_count = 0
        for entry in src.iterdir():
            if entry.name == "history":
                continue
            if entry.is_file() and entry.name.endswith("-result.json"):
                inject_parent_suite(entry, f"end-to-end tests - {suite}", out_dir)
                result_count += 1
            elif entry.is_dir():
                shutil.copytree(entry, out_dir / entry.name, dirs_exist_ok=True)
            else:
                shutil.copy2(entry, out_dir / entry.name)

        counts[suite] = result_count
        print(f"[merge_suites] {suite}: merged {result_count} result file(s)", file=sys.stderr)
        if result_count == 0:
            print(f"[merge_suites] WARNING: {suite} contributed 0 result files", file=sys.stderr)

    _verify_merge(merged_dir / "allure-results", suite_dirs, counts)
    return 0


def _verify_merge(out_dir: Path, suite_dirs: list[Path], counts: dict) -> None:
    """Print a merge summary and flag historyId collisions across suites.

    A collision here means two suites' executions of "the same" test still
    share a historyId after rescoping, so Allure would collapse them into a
    single entry again — the exact symptom this script exists to prevent.
    """
    history_owners: dict[str, set] = {}
    for result_file in out_dir.glob("*-result.json"):
        with result_file.open() as f:
            data = json.load(f)
        history_id = data.get("historyId")
        parent_suite = next(
            (l["value"] for l in data.get("labels", []) if l.get("name") == "parentSuite"),
            None,
        )
        if history_id and parent_suite:
            history_owners.setdefault(history_id, set()).add(parent_suite)

    collisions = {h: s for h, s in history_owners.items() if len(s) > 1}
    total_results = sum(counts.values())
    print(
        f"[merge_suites] merged {len(suite_dirs)} suite(s), {total_results} result file(s) total",
        file=sys.stderr,
    )
    if collisions:
        print(
            f"[merge_suites] WARNING: {len(collisions)} historyId(s) still shared across suites: {collisions}",
            file=sys.stderr,
        )
    else:
        print("[merge_suites] no historyId collisions across suites", file=sys.stderr)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <results-dir> <merged-dir>", file=sys.stderr)
        sys.exit(2)

    try:
        sys.exit(merge_suites(Path(sys.argv[1]), Path(sys.argv[2])))
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(2)
