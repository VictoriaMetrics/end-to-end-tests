#!/usr/bin/env python3
"""Extract VMCluster metrics for bench_press import."""

import argparse
import json
import os
import re
import sys
import tempfile
import time
from datetime import datetime, timedelta, timezone
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import urlencode, urlsplit, urlunsplit
from urllib.error import HTTPError
from urllib.request import Request, urlopen


class VMAPIError(RuntimeError):
    """VictoriaMetrics API returned an error."""


@dataclass(frozen=True)
class Config:
    base_url: str
    username: str
    password: str
    start: str
    end: str
    step: str | None
    output_dir: Path
    concurrency: int
    timeout: float


def build_vm_url(vm_url: str) -> str:
    """Validate and return VM's configured Prometheus API base URL."""
    parsed = urlsplit(vm_url.rstrip("/"))
    if not parsed.scheme or not parsed.netloc:
        raise ValueError("--vm-url must be an absolute URL")
    path = parsed.path.rstrip("/")
    if not path:
        raise ValueError("--vm-url must include VM Prometheus API path, such as /metrics/select/prometheus")
    return urlunsplit((parsed.scheme, parsed.netloc, path, "", ""))


class VMClient:
    def __init__(self, config: Config):
        self.base_url = config.base_url.rstrip("/")
        self.auth = (config.username, config.password)
        self.timeout = config.timeout

    def get(self, endpoint: str, params: dict[str, str]) -> dict[str, Any]:
        url = f"{self.base_url}{endpoint}?{urlencode(params)}"
        print(f"request url: {url}")
        request = Request(url, headers={"Accept": "application/json"})
        request.add_header("Authorization", _basic_auth(*self.auth))
        print("request headers: {'Accept': 'application/json', 'Authorization': '<redacted>'}")
        try:
            with urlopen(request, timeout=self.timeout) as response:
                body = response.read()
                print(f"response: HTTP {response.status}, content-type={response.headers.get('Content-Type')}, bytes={len(body)}")
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace").strip()
            print(f"response: HTTP {exc.code}, content-type={exc.headers.get('Content-Type')}, body={detail[:1000]}")
            raise VMAPIError(f"request failed for {endpoint}: HTTP {exc.code}: {detail}") from exc
        except Exception as exc:
            raise VMAPIError(f"request failed for {endpoint}: {exc}") from exc
        try:
            payload = json.loads(body)
        except json.JSONDecodeError as exc:
            raise VMAPIError(f"invalid JSON from {endpoint}") from exc
        if payload.get("status") != "success":
            error = payload.get("error", "unknown API error")
            raise VMAPIError(f"{endpoint}: {error}")
        print(f"response payload: status={payload.get('status')}, data_type={type(payload.get('data')).__name__}")
        return payload

    def get_export(self, matcher: str, start: str, end: str) -> list[dict[str, Any]]:
        url = f"{self.base_url}/api/v1/export?{urlencode({'match[]': matcher, 'start': start, 'end': end})}"
        print(f"request url: {url}")
        request = Request(url, headers={"Accept": "application/stream+json"})
        request.add_header("Authorization", _basic_auth(*self.auth))
        try:
            with urlopen(request, timeout=self.timeout) as response:
                rows = []
                for line in response:
                    if line.strip():
                        rows.append(json.loads(line))
                print(f"response: HTTP {response.status}, content-type={response.headers.get('Content-Type')}, rows={len(rows)}")
                return rows
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace").strip()
            raise VMAPIError(f"request failed for /api/v1/export: HTTP {exc.code}: {detail}") from exc
        except Exception as exc:
            raise VMAPIError(f"request failed for /api/v1/export: {exc}") from exc

    def query(self, query: str) -> list[dict[str, Any]]:
        return self.get("/api/v1/query", {"query": query})["data"]["result"]

    def series(self, matcher: str, start: str, end: str) -> list[dict[str, str]]:
        return self.get("/api/v1/series", {"match[]": matcher, "start": start, "end": end})["data"]

    def query_range(self, query: str, start: str, end: str, step: str | None) -> list[dict[str, Any]]:
        params = {"query": query, "start": start, "end": end}
        if step is not None:
            params["step"] = step
        return self.get("/api/v1/query_range", params)["data"]["result"]

def _basic_auth(username: str, password: str) -> str:
    import base64

    token = base64.b64encode(f"{username}:{password}".encode()).decode()
    return f"Basic {token}"


def prometheus_selector(labels: dict[str, str]) -> str:
    labels = dict(labels)
    name = labels.pop("__name__", "")
    matchers = [f'{key}="{value.replace(chr(92), chr(92) * 2).replace(chr(34), chr(92) + chr(34))}"' for key, value in sorted(labels.items())]
    return f"{name}{{{','.join(matchers)}}}" if name else "{" + ",".join(matchers) + "}"


def metric_entry(result: dict[str, Any]) -> dict[str, Any]:
    values = result.get("values", [])
    return {
        "metric": result.get("metric", {}),
        "timestamps": [int(float(sample[0]) * 1000) for sample in values],
        "values": [float(sample[1]) for sample in values],
    }


def merge_export_rows(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    merged: dict[str, dict[str, Any]] = {}
    for row in rows:
        metric = row.get("metric", {})
        key = json.dumps(metric, sort_keys=True, separators=(",", ":"))
        entry = merged.setdefault(key, {"metric": metric, "timestamps": [], "values": []})
        if "timestamps" in row:
            timestamps = row.get("timestamps", [])
            values = row.get("values", [])
            if len(timestamps) != len(values):
                raise VMAPIError(f"export row has {len(timestamps)} timestamps but {len(values)} values")
            for timestamp, value in zip(timestamps, values):
                if value is None:
                    continue
                entry["timestamps"].append(int(float(timestamp) * 1000))
                entry["values"].append(float(value))
        else:
            for sample in row.get("values", []):
                entry["timestamps"].append(int(float(sample[0]) * 1000))
                entry["values"].append(float(sample[1]))
    return list(merged.values())


def safe_cluster_name(cluster_id: str) -> str:
    name = re.sub(r"[^A-Za-z0-9_.-]+", "_", cluster_id).strip("._")
    return name or "unknown"


def parse_time(value: str) -> datetime:
    if value.isdigit():
        return datetime.fromtimestamp(int(value) / 1000, timezone.utc)
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def format_time(value: datetime) -> str:
    return str(int(value.timestamp() * 1000))


def extract_cluster(client: VMClient, cluster_id: str, config: Config, retries: int) -> int:
    matcher = "{" + f'cluster_id="{cluster_id.replace(chr(92), chr(92) * 2).replace(chr(34), chr(92) + chr(34))}' + '"}'
    print(f"cluster {cluster_id}: exporting matcher {matcher}")
    start = parse_time(config.start)
    end = parse_time(config.end)
    entries_by_metric: dict[str, dict[str, Any]] = {}
    chunk = timedelta(hours=1)
    cursor = start
    while cursor < end:
        chunk_end = min(cursor + chunk, end)
        chunk_start_value = format_time(cursor)
        chunk_end_value = format_time(chunk_end)
        print(f"cluster {cluster_id}: exporting {chunk_start_value}..{chunk_end_value}")
        for attempt in range(retries + 1):
            try:
                rows = merge_export_rows(client.get_export(matcher, chunk_start_value, chunk_end_value))
                break
            except (VMAPIError, json.JSONDecodeError, ValueError) as exc:
                if attempt >= retries:
                    raise
                delay = min(2**attempt, 10)
                print(f"cluster {cluster_id}: chunk failed (attempt {attempt + 1}/{retries + 1}): {exc}; retrying in {delay}s")
                time.sleep(delay)
        for row in rows:
            key = json.dumps(row["metric"], sort_keys=True, separators=(",", ":"))
            entry = entries_by_metric.setdefault(key, {"metric": row["metric"], "timestamps": [], "values": []})
            entry["timestamps"].extend(row["timestamps"])
            entry["values"].extend(row["values"])
        cursor = chunk_end
    entries = list(entries_by_metric.values())
    print(f"cluster {cluster_id}: exported {len(entries)} series")

    config.output_dir.mkdir(parents=True, exist_ok=True)
    target = config.output_dir / f"cluster_{safe_cluster_name(cluster_id)}.jsonl"
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=config.output_dir, delete=False) as temporary:
        try:
            for entry in entries:
                temporary.write(json.dumps(entry, separators=(",", ":")) + "\n")
            temporary_path = Path(temporary.name)
            temporary_path.replace(target)
            print(f"cluster {cluster_id}: wrote {len(entries)} series to {target}")
        except Exception:
            Path(temporary.name).unlink(missing_ok=True)
            raise
    return len(entries)


def extract(config: Config, job: str, retries: int) -> dict[str, int]:
    client = VMClient(config)
    query = f'sum(vm_app_version{{job="{job.replace(chr(92), chr(92) * 2).replace(chr(34), chr(92) + chr(34))}"}}) by (cluster_id)'
    discovery = client.query_range(query, config.start, config.end, config.step or "1h")
    clusters = sorted({item.get("metric", {}).get("cluster_id") for item in discovery if item.get("metric", {}).get("cluster_id")})
    print(f"discovered clusters: {len(clusters)}: {clusters}")
    return {cluster: extract_cluster(client, cluster, config, retries) for cluster in clusters}


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("job", help="job label value")
    result.add_argument("--vm-url", required=True, help="VM Prometheus API base URL, e.g. https://host/metrics/select/prometheus")
    result.add_argument("--start", required=True, help="query start time accepted by VM")
    result.add_argument("--end", required=True, help="query end time accepted by VM")
    result.add_argument("--step", help="optional query step")
    result.add_argument("--username", default=os.getenv("VM_USERNAME", ""))
    result.add_argument("--password", default=os.getenv("VM_PASSWORD", ""))
    result.add_argument("--output-dir", type=Path, default=Path("exports"))
    result.add_argument("--concurrency", type=int, default=8)
    result.add_argument("--timeout", type=float, default=60)
    result.add_argument("--retries", type=int, default=3, help="retries for failed export chunks")
    return result


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if args.concurrency < 1:
        parser().error("--concurrency must be positive")
    if args.retries < 0:
        parser().error("--retries cannot be negative")
    if not args.username or not args.password:
        parser().error("credentials required via --username/--password or VM_USERNAME/VM_PASSWORD")
    try:
        base_url = build_vm_url(args.vm_url)
        config = Config(base_url, args.username, args.password, args.start, args.end, args.step, args.output_dir, args.concurrency, args.timeout)
        counts = extract(config, args.job, args.retries)
    except (VMAPIError, ValueError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    for cluster, count in counts.items():
        print(f"cluster {cluster}: wrote {count} series")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
