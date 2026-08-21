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
is_enterprise = "vm-enterprise" in label_list
is_rc = "rc" in label_list
is_lts_current = "lts-current" in label_list
is_lts_previous = "lts-previous" in label_list
is_operator = "operator" in label_list
is_operator_lts = "operator-lts" in label_list
is_operator_rc = "operator-rc" in label_list
runner_image = f"{os.environ.get('RUNNER_IMAGE_REPO', '')}:{runner_image_tag()}"

COMMON_ENV = [
    "GCP_REGION",
    "GCP_CREDS",
    "PROJECT_ID",
    "BUILDKITE_BUILD_NUMBER",
    "BUILDKITE_COMMIT",
]

K8S_VERSIONS = [
    "1.36",
    "1.35",
    "1.34",
    "1.33",
]

SUITES = [
    # (suite, emoji+text, procs)
    (
        "vm-load",
        ":chart_with_upwards_trend: VM Load Tests",
        3,
    ),
    (
        "vm-chaos",
        ":boom: VM Chaos Tests",
        6,
    ),
    (
        "vm-distributed",
        ":globe_with_meridians: VM Distributed Tests",
        2,
    ),
    (
        "vm-functional",
        ":white_check_mark: VM Functional Tests",
        5,
    ),
    (
        "vm-enterprise",
        ":lock: VM Enterprise Tests",
        1,
    ),
    (
        "vl-functional",
        ":page_with_curl: VL Functional Tests",
        2,
    ),
    (
        "vl-chaos",
        ":boom: VL Chaos Tests",
        6,
    ),
    (
        "vl-load",
        ":chart_with_upwards_trend: VL Load Tests",
        5,
    ),
    (
        "vl-enterprise",
        ":lock: VL Enterprise Tests",
        1,
    ),
    (
        "operator",
        ":gear: Operator Tests",
        4,
    ),
]


NO_LABEL_DEFAULT_SUITES = {
    "vm-functional",
    "vl-functional",
}


def should_run(suite: str) -> bool:
    # Run enterprise tests on enterprise branches, on LTS updates, or on operator updates
    if suite == "vm-enterprise":
        return is_enterprise or is_lts_current or is_lts_previous
    # Run operator tests on operator updates
    if suite == "operator":
        return is_operator or is_operator_lts or is_operator_rc
    # Run all other tests on main branches
    if branch.startswith("gh-readonly-queue/main/"):
        return True
    # Run default suites on PRs without labels
    if not label_list:
        return suite in NO_LABEL_DEFAULT_SUITES
    return suite in label_list


def make_step(
    label: str,
    suite: str,
    procs: int,
    k8s_version: str = "",
) -> dict:
    make_cmd = f"make test-gke TEST_BINARY=/tests/{suite}_test.test PROCS={procs} TIMEOUT=75m BUILD_ID={build_number} REPORT_DIR=./allure-results BIN_DIR=/usr/local/bin"
    if k8s_version:
        make_cmd += f" K8S_VERSION={k8s_version}"
    # Enterprise suites (vm-enterprise, vl-enterprise) gate their only specs
    # behind Label("enterprise"); without VM_ENTERPRISE the Makefile applies
    # --label-filter='!enterprise' and every spec is skipped, regardless of
    # which trigger (label/lts/operator/main branch) started the suite.
    is_suite_enterprise = "enterprise" in suite
    if is_suite_enterprise or is_enterprise or is_lts_current or is_lts_previous:
        make_cmd += " LICENSE_FILE=/buildkite-secrets/license.txt VM_ENTERPRISE=1"
    if is_rc:
        make_cmd += " VM_RC=1"
    if is_lts_current:
        make_cmd += " VM_LTS_VERSION=current"
    if is_lts_previous:
        make_cmd += " VM_LTS_VERSION=previous"
    if is_operator_lts:
        make_cmd += " OPERATOR_LTS_VERSION=current"
    if is_operator_rc:
        make_cmd += " OPERATOR_RC=1"

    if branch.startswith("gh-readonly-queue/main/"):
        # Send data to MDX when running the merge queue
        make_cmd += " MDX_PASSWORD=/buildkite-secrets/mdx-password.txt"
        # Upload results to GCP bucket
        command = textwrap.dedent(
            f"""\
            export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
            set +e
            echo "+++ Running {suite} tests"
            {make_cmd}; test_exit_code=$?
            echo "--- Uploading results"
            make upload-results TEST_SUITE={suite} BUILD_ID={build_number} REPORT_DIR=./allure-results; upload_exit_code=$?
            if [ "$test_exit_code" -ne 0 ]; then
                exit "$test_exit_code"
            fi
            exit $upload_exit_code"""
        )
    else:
        command = textwrap.dedent(
            f"""\
            export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
            echo "+++ Running {suite} tests"
            {make_cmd}"""
        )
    step_key = f"{suite}-k8s-{k8s_version.replace('.', '-')}" if k8s_version else suite
    step = {
        "label": label,
        "key": step_key,
        "timeout_in_minutes": 120,
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


def make_cleanup_step(suite: str, k8s_version: str = "") -> dict:
    version_arg = f" K8S_VERSION={k8s_version}" if k8s_version else ""
    command = textwrap.dedent(
        f"""\
        export GOOGLE_APPLICATION_CREDENTIALS=/buildkite-secrets/gcp-creds.json
        echo "--- Destroying GKE cluster"
        make clean-gke TEST_SUITE={suite} BUILD_ID={build_number}{version_arg}"""
    )
    step_key = f"{suite}-k8s-{k8s_version.replace('.', '-')}" if k8s_version else suite
    return {
        "label": f":broom: Cleanup {suite}{' (k8s ' + k8s_version + ')' if k8s_version else ''}",
        "key": f"{step_key}-cleanup",
        "depends_on": [{"step": step_key, "allow_failure": True}],
        "cancel_on_build_failing": False,
        "timeout_in_minutes": 30,
        "retry": {"automatic": [{"exit_status": "*", "limit": 3}]},
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
for suite, label, procs in SUITES:
    if should_run(suite):
        versions = K8S_VERSIONS if suite == "operator" else [""]
        for k8s_version in versions:
            version_label = f" (k8s {k8s_version})" if k8s_version else ""
            steps.append(make_step(label + version_label, suite, procs, k8s_version))
            steps.append(make_cleanup_step(suite, k8s_version))

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
            "artifact_paths": ["report.tar.gz"],
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
