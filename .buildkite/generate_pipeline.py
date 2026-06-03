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


def runner_image_tag() -> str:
    commit = os.environ.get("BUILDKITE_COMMIT", "")
    if commit:
        return commit[:8]
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--short=8", "HEAD"],
            capture_output=True,
            check=True,
            text=True,
        )
        return result.stdout.strip()
    except (FileNotFoundError, subprocess.CalledProcessError):
        return build_number


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

label_list = [l.strip() for l in labels.split(",") if l.strip()]
is_enterprise = "enterprise" in label_list
is_rc = "rc" in label_list
is_lts_current = "lts-current" in label_list
is_lts_previous = "lts-previous" in label_list
runner_image = f"{os.environ.get('RUNNER_IMAGE_REPO', '')}:{runner_image_tag()}"

COMMON_ENV = [
    "GCP_REGION",
    "GCP_CREDS",
    "PROJECT_ID",
    "BUILDKITE_BUILD_NUMBER",
    "BUILDKITE_COMMIT",
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
    (
        "enterprise",
        ":lock: Enterprise Tests",
        "enterprise-tests",
        "enterprise",
        1,
        0,
    ),
]


NO_LABEL_DEFAULT_SUITES = {
    "load-test",
    "chaos-test",
    "distributed-test",
    "functional-test",
}


def should_run(label: str) -> bool:
    if label == "enterprise":
        return is_enterprise or is_lts_current or is_lts_previous
    if branch == "main":
        return True
    if not label_list:
        return label in NO_LABEL_DEFAULT_SUITES
    return label in label_list


def make_step(
    label: str,
    key: str,
    suite: str,
    procs: int,
    flakes: int,
) -> dict:
    make_cmd = f"make test-gke TEST_BINARY=/tests/{suite}_test.test PROCS={procs} FLAKE_ATTEMPTS={flakes} TIMEOUT=90m BUILD_ID={build_number} REPORT_DIR=./allure-results"
    if is_enterprise or is_lts_current or is_lts_previous:
        make_cmd += " LICENSE_FILE=/buildkite-secrets/license.txt VM_ENTERPRISE=1"
    if is_rc:
        make_cmd += " VM_RC=1"
    if is_lts_current:
        make_cmd += " VM_LTS_VERSION=current"
    if is_lts_previous:
        make_cmd += " VM_LTS_VERSION=previous"

    if branch.startswith("gh-readonly-queue/main/"):
        command = textwrap.dedent(
            f"""\
            export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
            set +e
            echo "+++ Running {suite} tests"
            {make_cmd}
            EXIT_CODE=\\$?
            echo "--- Uploading results"
            make upload-results TEST_SUITE={suite} BUILD_ID={build_number} REPORT_DIR=./allure-results
            exit \\$EXIT_CODE"""
        )
    else:
        command = textwrap.dedent(
            f"""\
            export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
            echo "+++ Running {suite} tests"
            {make_cmd}"""
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
                        "./allure-results:/tests/allure-results",
                    ],
                }
            }
        ],
    }
    if not branch.startswith("gh-readonly-queue/main/"):
        step["artifact_paths"] = [
            f"allure-results/{suite}/**/*",
            f"allure-results/{suite}/*",
        ]
    return step


def make_cleanup_step(key: str, suite: str) -> dict:
    command = textwrap.dedent(
        f"""\
        export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
        echo "--- Destroying GKE cluster"
        make clean-gke TEST_SUITE={suite} BUILD_ID={build_number}"""
    )
    return {
        "label": f":broom: Cleanup {suite}",
        "key": f"{key}-cleanup",
        "depends_on": [{"step": key, "allow_failure": True}],
        "cancel_on_build_failing": False,
        "timeout_in_minutes": 20,
        "command": command,
        "plugins": [
            {
                "docker#v5.0.0": {
                    "image": runner_image,
                    "environment": COMMON_ENV,
                    "volumes": [
                        "/tmp:/tmp",
                        "/buildkite-secrets:/buildkite-secrets",
                    ],
                }
            }
        ],
    }


steps = []
for pr_label, label, key, suite, procs, flakes in SUITES:
    if should_run(pr_label):
        steps.append(make_step(label, key, suite, procs, flakes))
        steps.append(make_cleanup_step(key, suite))

if not steps:
    print("No test suites selected; nothing to queue.", file=sys.stderr)
    sys.exit(0)

if branch.startswith("gh-readonly-queue/main/"):
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
            "artifact_paths": ["report/**/*", "report/*"],
            "plugins": [
                {
                    "artifacts#v1.9.3": {
                        "download": "allure-results/**",
                        "ignore-missing": True,
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
