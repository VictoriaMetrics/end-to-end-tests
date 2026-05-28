#!/usr/bin/env python3
"""bench-press: enrich and re-align metrics, then push to VictoriaMetrics."""

import argparse
import sys
import time

from utils import enrich_series, parse_labels, push_to_vm, read_jsonl


def align_parallel(groups: list[list[dict]], start_ms: int | None = None) -> list[dict]:
    """
    Re-align all groups so every run starts at the same timestamp and the
    latest endpoint does not exceed start_ms (wall-clock now by default).

    Algorithm:
      1. Per run: compute run_start = min(timestamps), run_end = max(timestamps).
      2. max_duration = max(run_end - run_start) across all runs.
      3. common_start = start_ms - max_duration.
      4. Each run is shifted independently: offset_i = common_start - run_start_i.
    """
    if start_ms is None:
        start_ms = int(time.time() * 1000)

    # Per-group bounds.
    group_bounds: list[tuple[int, int] | None] = []
    for group in groups:
        all_ts = [t for entry in group for t in entry.get("timestamps", [])]
        if all_ts:
            group_bounds.append((min(all_ts), max(all_ts)))
        else:
            group_bounds.append(None)

    valid = [(s, e) for b in group_bounds if b is not None for s, e in [b]]
    if not valid:
        return [entry for group in groups for entry in group]

    max_duration = max(e - s for s, e in valid)
    common_start = start_ms - max_duration

    out: list[dict] = []
    for group, bounds in zip(groups, group_bounds):
        if bounds is None:
            out.extend(group)
            continue
        run_start = bounds[0]
        offset = common_start - run_start
        for entry in group:
            timestamps = entry.get("timestamps", [])
            if not timestamps:
                out.append(entry)
                continue
            shifted = [t + offset for t in timestamps]
            out.append({**entry, "timestamps": shifted, "values": entry.get("values", [])})
    return out


def align_sequential(groups: list[list[dict]], start_ms: int | None = None) -> list[dict]:
    """
    Re-align groups so runs are placed in CLI order, end-to-end, with the last
    point of the last run landing at start_ms (wall-clock now by default).

    Algorithm:
      1. Per group: compute group_start = min(ts), group_end = max(ts).
      2. total_span = sum of all group durations.
      3. cursor = start_ms - total_span  (oldest point in time).
      4. Each group is shifted so its start aligns with cursor; cursor advances
         by the group's duration. Last group's last point == start_ms.
    """
    if start_ms is None:
        start_ms = int(time.time() * 1000)

    # Per-group bounds over all timestamps (not just first/last element).
    group_bounds: list[tuple[int, int] | None] = []
    for group in groups:
        all_ts = [t for entry in group for t in entry.get("timestamps", [])]
        if all_ts:
            group_bounds.append((min(all_ts), max(all_ts)))
        else:
            group_bounds.append(None)

    total_span = sum(e - s for b in group_bounds if b is not None for s, e in [b])
    cursor_ms = start_ms - total_span

    out: list[dict] = []
    for group, bounds in zip(groups, group_bounds):
        if bounds is None:
            out.extend(group)
            continue
        group_start, group_end = bounds
        offset = cursor_ms - group_start
        for entry in group:
            timestamps = entry.get("timestamps", [])
            if not timestamps:
                out.append(entry)
                continue
            shifted = [t + offset for t in timestamps]
            out.append({**entry, "timestamps": shifted, "values": entry.get("values", [])})
        cursor_ms += group_end - group_start

    return out


# Flags that consume the next token as their value.
_FLAGS_WITH_VALUE = {"--mode", "--vm-url", "--start-ms", "--batch-size"}
# Flags that are boolean (no following value).
_FLAGS_STANDALONE = {"--dry-run"}


def _is_label_flag(arg: str) -> bool:
    return arg in ("-l", "--label") or arg.startswith("--label=") or arg.startswith("-l=")


def group_argv(
    argv: list[str],
) -> tuple[list[str], list[tuple[str, list[str]]]]:
    """
    Split argv into (global_argv, runs).

    runs is a list of (file_path, label_strings) where label_strings are the
    raw KEY=VALUE tokens collected from --label/-l flags that follow that file
    and precede the next file.

    Global flags (--mode, --vm-url, etc.) and --label flags that appear before
    any positional file are kept in global_argv for argparse to handle.
    """
    global_argv: list[str] = []
    runs: list[tuple[str, list[str]]] = []

    i = 0
    while i < len(argv):
        arg = argv[i]

        if arg in _FLAGS_WITH_VALUE:
            global_argv += [arg, argv[i + 1]]
            i += 2
        elif arg in _FLAGS_STANDALONE:
            global_argv.append(arg)
            i += 1
        elif _is_label_flag(arg):
            if arg in ("-l", "--label"):
                value = argv[i + 1]
                i += 2
            else:
                # --label=KEY=VALUE or -l=KEY=VALUE: value is after the first '='
                # that follows the flag name, e.g. "--label=foo=bar" → "foo=bar"
                value = arg.split("=", 1)[1]
                i += 1
            if runs:
                runs[-1][1].append(value)
            else:
                global_argv += ["--label", value]
        elif not arg.startswith("-"):
            # Positional: file path starts a new run.
            runs.append((arg, []))
            i += 1
        else:
            # Unknown flag — pass through to argparse as global.
            global_argv.append(arg)
            i += 1

    return global_argv, runs


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="bench-press",
        description="Enrich and re-align JSONL metrics, push to VictoriaMetrics.",
        epilog=(
            "Per-run labels: place --label flags immediately after each FILE.\n"
            "Global --label flags (before the first FILE) apply to all runs."
        ),
    )
    p.add_argument(
        "--label",
        "-l",
        action="append",
        default=[],
        metavar="KEY=VALUE",
        help="Global label added to every series (repeatable).",
    )
    p.add_argument(
        "--vm-url",
        default="http://localhost:8428",
        help="VictoriaMetrics base URL (default: http://localhost:8428).",
    )
    p.add_argument(
        "--mode",
        choices=["parallel", "sequential"],
        default="parallel",
        help="Alignment mode (default: parallel).",
    )
    p.add_argument(
        "--start-ms",
        type=int,
        default=None,
        metavar="EPOCH_MS",
        help="Override synthetic start timestamp (milliseconds). Defaults to now.",
    )
    p.add_argument(
        "--batch-size",
        type=int,
        default=500,
        metavar="N",
        help="Series per POST request (default: 500). Reduce if VM resets connection.",
    )
    p.add_argument(
        "--dry-run",
        action="store_true",
        help="Print resulting JSONL to stdout instead of pushing.",
    )
    return p


def main(argv: list[str] | None = None) -> int:
    raw_argv = list(argv if argv is not None else sys.argv[1:])
    global_argv, runs = group_argv(raw_argv)

    if not runs:
        build_parser().print_usage(sys.stderr)
        print("error: at least one FILE is required", file=sys.stderr)
        return 1

    args = build_parser().parse_args(global_argv)

    try:
        global_labels = parse_labels(args.label)
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    groups: list[list[dict]] = []
    for path, run_label_strs in runs:
        try:
            run_labels = parse_labels(run_label_strs)
        except ValueError as exc:
            print(f"error: {path}: {exc}", file=sys.stderr)
            return 1
        try:
            raw = list(read_jsonl(path))
        except (OSError, ValueError) as exc:
            print(f"error: {exc}", file=sys.stderr)
            return 1
        # precedence: benchpress_filename < global_labels < per-run labels
        file_labels = {"benchpress_filename": path, **global_labels, **run_labels}
        raw = enrich_series(raw, file_labels)
        groups.append(raw)

    if args.mode == "parallel":
        series = align_parallel(groups, start_ms=args.start_ms)
    else:
        series = align_sequential(groups, start_ms=args.start_ms)

    if args.dry_run:
        for s in series:
            print(json.dumps(s))
        return 0

    try:
        push_to_vm(series, args.vm_url, batch_size=args.batch_size)
    except RuntimeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
