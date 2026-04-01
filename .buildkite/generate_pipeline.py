#!/usr/bin/env python3
"""
Generate Buildkite test steps based on PULL_REQUEST_LABELS and BUILDKITE_BRANCH.

Prints a JSON pipeline to stdout; the caller pipes it to
`buildkite-agent pipeline upload`.

A suite is included when:
  - BUILDKITE_BRANCH == "main"  (main branch — run everything), OR
  - the suite's label appears in PULL_REQUEST_LABELS
"""

import json
import os
import subprocess
import sys
import textwrap

branch = os.environ.get("BUILDKITE_BRANCH", "")
build_number = os.environ.get("BUILDKITE_BUILD_NUMBER", "")
labels = os.environ.get("BUILDKITE_PULL_REQUEST_LABELS", "")
if not labels:
    try:
        result = subprocess.run(
            ["buildkite-agent", "meta-data", "get", "BUILDKITE_PULL_REQUEST_LABELS"],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            labels = result.stdout.strip()
    except FileNotFoundError:
        pass
runner_image = (
    f"{os.environ.get('RUNNER_IMAGE_REPO', '')}:"
    f"{os.environ.get('GO_VERSION', '')}-tf{os.environ.get('TERRAFORM_VERSION', '')}"
)

COMMON_ENV = [
    "VM_VMSINGLEDEFAULT_VERSION",
    "VM_VMCLUSTERDEFAULT_VERSION",
    "VM_ENTERPRISE",
    "GCP_REGION",
    "DISTRIBUTED_ZONES",
    "GCP_CREDS",
    "PROJECT_ID",
    "BUILDKITE_BUILD_NUMBER",
]

SUITES = [
    # (pr-label,          emoji+text,                           key,                suite,        procs, flakes)
    ("load-test", ":chart_with_upwards_trend: Load Tests", "load-tests", "load", 1, 0),
    ("chaos-test", ":boom: Chaos Tests", "chaos-tests", "chaos", 5, 3),
    (
        "distributed-test",
        ":globe_with_meridians: Distributed Tests",
        "distributed-tests",
        "distributed",
        1,
        0,
    ),
    (
        "functional-test",
        ":white_check_mark: Functional Tests",
        "functional-tests",
        "functional",
        5,
        3,
    ),
]


def should_run(label: str) -> bool:
    return branch == "main" or label in labels.split(",")


def make_step(label: str, key: str, suite: str, procs: int, flakes: int) -> dict:
    command = textwrap.dedent(
        f"""\
        cat <<'EOF' > /tmp/gcp-creds.json
        $GCP_CREDS
        EOF
        export GOOGLE_APPLICATION_CREDENTIALS=/tmp/gcp-creds.json
        set +e
        echo "+++ Running {suite} tests"
        make test-gke TEST_BINARY=/tests/{suite}_test.test PROCS={procs} FLAKE_ATTEMPTS={flakes} TIMEOUT=90m BUILD_ID={build_number}
        EXIT_CODE=\\$?
        echo "--- Destroying GKE cluster"
        make clean-gke TEST_SUITE={suite}
        echo "--- Uploading results"
        make upload-results TEST_SUITE={suite} BUILD_ID={build_number}
        exit \\$EXIT_CODE"""
    )
    return {
        "label": label,
        "key": key,
        "timeout_in_minutes": 90,
        "command": command,
        "plugins": [
            {
                "docker#v5.0.0": {
                    "image": runner_image,
                    "environment": COMMON_ENV,
                    "volumes": ["/tmp:/tmp"],
                }
            }
        ],
    }


steps = [
    make_step(label, key, suite, procs, flakes)
    for pr_label, label, key, suite, procs, flakes in SUITES
    if should_run(pr_label)
]

if not steps:
    print("No PR labels matched any test suite; nothing to queue.", file=sys.stderr)
    sys.exit(0)

print(json.dumps({"steps": steps}))
