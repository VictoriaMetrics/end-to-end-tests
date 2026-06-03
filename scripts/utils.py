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
    """Yield parsed JSON objects from a JSONL file."""
    with open(path) as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.strip()
            if not line:
                continue
            try:
                yield json.loads(line)
            except json.JSONDecodeError as exc:
                raise ValueError(f"{path}:{lineno}: invalid JSON: {exc}") from exc


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
        body = "\n".join(json.dumps(s) for s in chunk).encode()
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
