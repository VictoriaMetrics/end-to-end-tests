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

    def label_values(self, label_name: str, matcher: str, start: str, end: str) -> list[str]:
        return self.get(f"/api/v1/label/{label_name}/values", {"match[]": matcher, "start": start, "end": end})["data"]

    def query_range(self, query: str, start: str, end: str, step: str | None) -> list[dict[str, Any]]:
        params = {"query": query, "start": start, "end": end}
        if step is not None:
            params["step"] = step
        return self.get("/api/v1/query_range", params)["data"]["result"]

def _basic_auth(username: str, password: str) -> str:
    import base64

    token = base64.b64encode(f"{username}:{password}".encode()).decode()
    return f"Basic {token}"


def escape_label_value(value: str) -> str:
    return value.replace(chr(92), chr(92) * 2).replace(chr(34), chr(92) + chr(34))


def prometheus_selector(labels: dict[str, str]) -> str:
    labels = dict(labels)
    name = labels.pop("__name__", "")
    matchers = [f'{key}="{escape_label_value(value)}"' for key, value in sorted(labels.items())]
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


def probe_step(start: datetime, end: datetime, max_points: int = 29000) -> str:
    """Smallest whole-minute step that keeps a probe query's point count
    under VictoriaMetrics' -search.maxPointsPerTimeseries limit (default
    30000) for the given [start, end) range."""
    range_seconds = max(int((end - start).total_seconds()), 60)
    total_minutes = -(-range_seconds // 60)  # ceil to whole minutes
    minutes = max(1, -(-total_minutes // max_points))  # ceil division
    return f"{minutes}m"


def find_data_bounds(client: VMClient, matcher: str, config: Config) -> tuple[datetime, datetime] | None:
    """Return (first, last) timestamps with data for matcher within config's range, or None if no data.

    Uses the finest 1m probe step that fits within VM's max-points-per-series
    limit for the requested range (widening only if needed): VictoriaMetrics
    defaults the lookback window for a bare instant-vector selector in a
    range query to the step itself, so a coarse step (e.g. 1h) can report
    false-positive presence up to a full step away from the nearest real
    sample. Widening only when the range demands it keeps that error bound
    as tight as possible.
    """
    step = probe_step(parse_time(config.start), parse_time(config.end))
    result = client.query_range(f"count({matcher})", config.start, config.end, step)
    timestamps = [sample[0] for series in result for sample in series.get("values", [])]
    if not timestamps:
        return None
    return (
        datetime.fromtimestamp(min(timestamps), timezone.utc),
        datetime.fromtimestamp(max(timestamps), timezone.utc),
    )


def extract_cluster(client: VMClient, cluster_id: str, job: str, config: Config, retries: int) -> int:
    matcher = "{" + f'cluster_id="{escape_label_value(cluster_id)}",job="{escape_label_value(job)}"' + "}"
    print(f"cluster {cluster_id}: exporting matcher {matcher}")
    bounds = find_data_bounds(client, matcher, config)
    if bounds is None:
        print(f"cluster {cluster_id}: no data found for {matcher} in range, skipping export")
        return 0
    buffer = timedelta(minutes=5)
    start = max(parse_time(config.start), bounds[0] - buffer)
    end = min(parse_time(config.end), bounds[1] + buffer)
    print(f"cluster {cluster_id}: data available {format_time(start)}..{format_time(end)}")
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
        if not rows:
            if entries_by_metric:
                print(f"cluster {cluster_id}: chunk {chunk_start_value}..{chunk_end_value} empty, stopping export")
                break
            print(f"cluster {cluster_id}: chunk {chunk_start_value}..{chunk_end_value} empty, no data found yet, continuing")
            cursor = chunk_end
            continue
        for row in rows:
            key = json.dumps(row["metric"], sort_keys=True, separators=(",", ":"))
            entry = entries_by_metric.setdefault(key, {"metric": row["metric"], "timestamps": [], "values": []})
            entry["timestamps"].extend(row["timestamps"])
            entry["values"].extend(row["values"])
        cursor = chunk_end
    entries = list(entries_by_metric.values())
    print(f"cluster {cluster_id}: exported {len(entries)} series")

    config.output_dir.mkdir(parents=True, exist_ok=True)
    target = config.output_dir / f"cluster_{safe_cluster_name(cluster_id)}_{safe_cluster_name(job)}.jsonl"
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
    # label_values finds every cluster_id present anywhere in the range in a
    # single cheap call. A step-sampled query_range probe was used here
    # previously but missed most runs: vm_app_version only has samples during
    # a run's brief active window, so a coarse grid (e.g. 1h) rarely lands
    # within VM's default 5m staleness lookback of that window.
    matcher = "{" + f'job="{escape_label_value(job)}"' + "}"
    clusters = sorted(client.label_values("cluster_id", matcher, config.start, config.end))
    print(f"discovered clusters: {len(clusters)}: {clusters}")
    return {cluster: extract_cluster(client, cluster, job, config, retries) for cluster in clusters}


def _literal_regex(value: str) -> str:
    """Escape regex metacharacters for a MetricsQL label matcher.

    re.escape() also escapes '-', which is unnecessary outside a character
    class and produces an invalid "\\-" escape sequence inside a MetricsQL
    string literal, so it is restored to a literal '-' afterwards.
    """
    return re.escape(value).replace("\\-", "-")


# Shared Overwatch infra jobs: not scenario-suffixed, but relevant to every run.
_SHARED_JOBS = ("vmagent-vmks", "vmauth-vmks", "vmks-victoria-metrics-operator")


def discover_jobs(client: VMClient, scenario: str, config: Config) -> tuple[list[str], list[str]]:
    """Return (scenario_jobs, shared_jobs).

    scenario_jobs are components running the scenario itself (job ends in
    '-<scenario>'), which is enough to scope them to that scenario's own
    cluster_id values. shared_jobs (vmagent/vmauth/operator) are singletons
    serving every test category, so they must NOT be cluster-discovered on
    their own — doing so pulls in unrelated load/functional test runs. The
    caller instead extracts shared_jobs only for the cluster_id set already
    found via scenario_jobs.
    """
    scenario_matcher = "{" + f'job=~".*-{_literal_regex(scenario)}$"' + "}"
    scenario_jobs = sorted(client.label_values("job", scenario_matcher, config.start, config.end))
    shared_matcher = "{" + f'job=~"{"|".join(_literal_regex(job) for job in _SHARED_JOBS)}"' + "}"
    shared_jobs = sorted(client.label_values("job", shared_matcher, config.start, config.end))
    return scenario_jobs, shared_jobs


def confirm_jobs(jobs: list[str], scenario: str, assume_yes: bool) -> bool:
    if not jobs:
        print(f"error: no jobs found matching scenario '{scenario}' in the given time range", file=sys.stderr)
        return False
    print(f"discovered {len(jobs)} job(s) for scenario '{scenario}':")
    for job in jobs:
        print(f"  - {job}")
    if assume_yes:
        return True
    if not sys.stdin.isatty():
        print("error: non-interactive session; pass --yes to proceed without confirmation", file=sys.stderr)
        return False
    reply = input("proceed with export? [y/N] ").strip().lower()
    return reply in ("y", "yes")


def extract_scenario(config: Config, scenario: str, retries: int, assume_yes: bool) -> dict[str, dict[str, int]]:
    client = VMClient(config)
    scenario_jobs, shared_jobs = discover_jobs(client, scenario, config)
    if not confirm_jobs(scenario_jobs + shared_jobs, scenario, assume_yes):
        raise VMAPIError("aborted: job discovery not confirmed")
    results: dict[str, dict[str, int]] = {}
    clusters: set[str] = set()
    for job in scenario_jobs:
        counts = extract(config, job, retries)
        results[job] = counts
        clusters.update(counts)
    # Shared jobs are scoped to the cluster_ids the scenario itself produced,
    # not independently discovered (they serve every test category).
    for job in shared_jobs:
        results[job] = {cluster: extract_cluster(client, cluster, job, config, retries) for cluster in sorted(clusters)}
    return results


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("scenario", help="scenario name (matches job values ending in '-<scenario>')")
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
    result.add_argument("--yes", "-y", action="store_true", help="skip confirmation prompt before exporting")
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
        results = extract_scenario(config, args.scenario, args.retries, args.yes)
    except (VMAPIError, ValueError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    for job, counts in results.items():
        for cluster, count in counts.items():
            print(f"job {job}, cluster {cluster}: wrote {count} series")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
