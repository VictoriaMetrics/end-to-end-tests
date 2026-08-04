"""Utility functions for bench-press."""

import json
from typing import Iterator


def parse_labels(label_args: list[str]) -> dict[str, str]:
    """Parse 'key=value' strings into a dict."""
    labels: dict[str, str] = {}
    for item in label_args:
        if "=" not in item:
            raise ValueError(f"Label must be key=value, got: {item!r}")
        k, v = item.split("=", 1)
        labels[k.strip()] = v.strip()
    return labels


def read_jsonl(path: str) -> Iterator[dict]:
    """Yield parsed JSON objects from a JSONL file.

    mdx-export timestamps are Unix epoch microseconds (16-digit); convert to
    milliseconds here so every downstream consumer (duration math in
    align_parallel/align_sequential, VictoriaMetrics import) works in a
    single, consistent unit.
    """
    with open(path) as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except json.JSONDecodeError as exc:
                raise ValueError(f"{path}:{lineno}: invalid JSON: {exc}") from exc
            timestamps = entry.get("timestamps")
            if timestamps:
                entry["timestamps"] = [t // 1000 for t in timestamps]
            yield entry


def enrich_series(series: list[dict], extra_labels: dict[str, str]) -> list[dict]:
    """Add extra_labels to every series' metric dict."""
    result = []
    for entry in series:
        e = dict(entry)
        e["metric"] = {**entry.get("metric", {}), **extra_labels}
        result.append(e)
    return result


def push_to_vm(series: list[dict], vm_url: str, batch_size: int = 500) -> None:
    """POST series to VictoriaMetrics /api/v1/import (JSONL), in batches."""
    import urllib.request

    url = vm_url.rstrip("/") + "/api/v1/import"
    total = 0
    for i in range(0, len(series), batch_size):
        chunk = series[i : i + batch_size]
        body = "\n".join(json.dumps(_to_vm_import_record(s)) for s in chunk).encode()
        req = urllib.request.Request(
            url,
            data=body,
            headers={"Content-Type": "application/x-ndjson"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req) as resp:
                status = resp.status
        except Exception as exc:
            raise RuntimeError(f"Failed to push to {url}: {exc}") from exc

        if status not in (200, 204):
            raise RuntimeError(f"VM returned HTTP {status}")
        total += len(chunk)

    print(f"Pushed {total} series to {url}")


def _to_vm_import_record(series: dict) -> dict:
    """Convert bench-press series to VictoriaMetrics JSON import format."""
    if "timestamps" not in series:
        return series
    timestamps = series["timestamps"]
    values = series.get("values", [])
    if len(timestamps) != len(values):
        raise ValueError("series timestamps and values must have equal lengths")
    return {
        "metric": series.get("metric", {}),
        "values": values,
        "timestamps": timestamps,
    }
