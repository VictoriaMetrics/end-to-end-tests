import json
import os
from pathlib import Path
import subprocess
import sys
import unittest


GENERATOR = Path(__file__).with_name("generate_pipeline.py")


def generate_test_commands(labels):
    env = {
        **os.environ,
        "BUILDKITE_PULL_REQUEST_LABELS": labels,
        "BUILDKITE_BRANCH": "feature",
        "BUILDKITE_COMMIT": "12345678",
        "RUNNER_IMAGE_REPO": "runner",
    }
    result = subprocess.run(
        [sys.executable, str(GENERATOR)], env=env, capture_output=True, text=True, check=True
    )
    return {
        step["key"]: step["command"]
        for step in (json.loads(result.stdout)["steps"] if result.stdout else [])
        if "key" in step and "make test-gke" in step.get("command", "")
    }


class EnterpriseLabelsTest(unittest.TestCase):
    def test_vm_enterprise_selects_vm_functional_with_license(self):
        commands = generate_test_commands("vm-enterprise")
        self.assertEqual(set(commands), {"vm-functional"})
        self.assertIn("LICENSE_FILE=/buildkite-secrets/license.txt VM_ENTERPRISE=1", commands["vm-functional"])

    def test_vl_enterprise_selects_vl_functional_with_license(self):
        commands = generate_test_commands("vl-enterprise")
        self.assertEqual(set(commands), {"vl-functional"})
        self.assertIn("LICENSE_FILE=/buildkite-secrets/license.txt VM_ENTERPRISE=1", commands["vl-functional"])

    def test_enterprise_license_applies_to_other_selected_suites(self):
        commands = generate_test_commands("vm-load,vl-enterprise")
        self.assertEqual(set(commands), {"vm-load", "vl-functional"})
        for command in commands.values():
            self.assertIn("LICENSE_FILE=/buildkite-secrets/license.txt VM_ENTERPRISE=1", command)

    def test_lts_still_selects_functional_suite_with_license(self):
        commands = generate_test_commands("lts-current")
        self.assertEqual(set(commands), {"vm-functional"})
        self.assertIn("LICENSE_FILE=/buildkite-secrets/license.txt VM_ENTERPRISE=1", commands["vm-functional"])


if __name__ == "__main__":
    unittest.main()
