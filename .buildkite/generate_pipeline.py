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

label_list = [l.strip() for l in labels.split(",")]
is_enterprise = "enterprise" in label_list
is_rc = "rc" in label_list
runner_image = (
    f"{os.environ.get('RUNNER_IMAGE_REPO', '')}:"
    f"{os.environ.get('BUILDKITE_BUILD_NUMBER', '')}"
)

COMMON_ENV = [
    "GCP_REGION",
    "GCP_CREDS",
    "PROJECT_ID",
    "BUILDKITE_BUILD_NUMBER",
]

SUITES = [
    # (pr-label,          emoji+text,                           key,                suite,        procs, flakes)
    ("load-test", ":chart_with_upwards_trend: Load Tests", "load-tests", "load", 3, 0),
    ("chaos-test", ":boom: Chaos Tests", "chaos-tests", "chaos", 10, 0),
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
    make_cmd = f"make test-gke TEST_BINARY=/tests/{suite}_test.test PROCS={procs} FLAKE_ATTEMPTS={flakes} TIMEOUT=90m BUILD_ID={build_number} REPORT_DIR=./allure-results"
    if is_enterprise:
        make_cmd += " LICENSE_FILE=/buildkite-secrets/license.txt VM_ENTERPRISE=1"
    if is_rc:
        make_cmd += " VM_RC=1"

    upload_results = ""
    if branch == "main":
        upload_results = textwrap.dedent(f"""\
            echo "--- Uploading results"
            make upload-results TEST_SUITE={suite} BUILD_ID={build_number} REPORT_DIR=/tests/allure-results
            """)

    command = textwrap.dedent(
        f"""\
        export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
        set +e
        echo "+++ Running {suite} tests"
        {make_cmd}
        EXIT_CODE=\\$?
        {upload_results}echo "--- Destroying GKE cluster"
        make clean-gke TEST_SUITE={suite}
        exit \\$EXIT_CODE"""
    )
    step = {
        "label": label,
        "key": key,
        "timeout_in_minutes": 90,
        "command": command,
        "plugins": [
            {
                "docker#v5.0.0": {
                    "image": runner_image,
                    "environment": COMMON_ENV,
                    "volumes": [
                        "/tmp:/tmp",
                        "/buildkite-secrets:/buildkite-secrets",
                        "$BUILDKITE_BUILD_CHECKOUT_PATH/allure-results:/tests/allure-results",
                    ],
                }
            }
        ],
    }
    if branch != "main":
        step["artifact_paths"] = f"allure-results/{suite}/**"
    return step


steps = [
    make_step(label, key, suite, procs, flakes)
    for pr_label, label, key, suite, procs, flakes in SUITES
    if should_run(pr_label)
]

if not steps:
    print("No PR labels matched any test suite; nothing to queue.", file=sys.stderr)
    sys.exit(0)

if branch == "main":
    deploy_command = textwrap.dedent(
        f"""\
        export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
        gcloud auth activate-service-account --key-file=/buildkite-secrets/gcp-creds.json
        make deploy-report BUILD_ID={build_number} CURDIR=/tests"""
    )
    steps += [
        {"wait": None, "continue_on_failure": True},
        {
            "label": ":bar_chart: Deploy Report",
            "key": "deploy-report",
            "timeout_in_minutes": 30,
            "command": deploy_command,
            "plugins": [
                {
                    "docker#v5.0.0": {
                        "image": runner_image,
                        "environment": [
                            "GCP_CREDS",
                            "BUILDKITE_BUILD_NUMBER",
                            "BUILDKITE_BRANCH",
                        ],
                        "volumes": [
                            "/buildkite-secrets:/buildkite-secrets",
                            "/tmp:/tmp",
                            "$BUILDKITE_BUILD_CHECKOUT_PATH:/tests",
                        ],
                    }
                }
            ],
        },
    ]
else:
    pr_report_command = textwrap.dedent(
        """\
        export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
        gcloud auth activate-service-account --key-file=/buildkite-secrets/gcp-creds.json
        make generate-pr-report ALLURE_RESULTS_DIR=./allure-results PR_REPORT_DIR=./report"""
    )
    steps += [
        {"wait": None, "continue_on_failure": True},
        {
            "label": ":bar_chart: Generate PR Report",
            "key": "pr-report",
            "timeout_in_minutes": 30,
            "command": pr_report_command,
            "artifact_paths": "report/**",
            "plugins": [
                {
                    "artifacts#v1.9.3": {
                        "download": "allure-results/**",
                    }
                },
                {
                    "docker#v5.0.0": {
                        "image": runner_image,
                        "environment": ["GCP_CREDS", "BUILDKITE_BUILD_NUMBER"],
                        "volumes": [
                            "/buildkite-secrets:/buildkite-secrets",
                            "/tmp:/tmp",
                        ],
                    }
                },
            ],
        },
    ]

print(json.dumps({"steps": steps}))
