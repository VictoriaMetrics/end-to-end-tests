package install

import (
	"os"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

// KubectlApply logs the manifest file contents before applying to the cluster.
func KubectlApply(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifestPath string) {
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		logger.Default.Logf(t, "WARNING: could not read manifest file %s: %v", manifestPath, err)
	} else {
		logger.Default.Logf(t, "Applying manifest from %s:\n---\n%s\n---", manifestPath, string(content))
	}
	k8s.KubectlApply(t, kubeOpts, manifestPath)
}

// KubectlApplyFromString logs the manifest contents before applying to the cluster.
func KubectlApplyFromString(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifest string) {
	logger.Default.Logf(t, "Applying manifest from string:\n---\n%s\n---", manifest)
	k8s.KubectlApplyFromString(t, kubeOpts, manifest)
}
