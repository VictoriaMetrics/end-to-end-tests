package install

import (
	"context"
	"os"
	"strings"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

const maxLogLines = 80

// KubectlApply logs the manifest file contents before applying to the cluster.
func KubectlApply(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifestPath string) {
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		logger.Default.Logf(t, "WARNING: could not read manifest file %s: %v", manifestPath, err)
	} else if lines := strings.Count(string(content), "\n"); lines <= maxLogLines {
		logger.Default.Logf(t, "Applying manifest from %s:\n---\n%s\n---", manifestPath, string(content))
	}
	k8s.KubectlApplyContext(t, ctx, kubeOpts, manifestPath)
}

// KubectlApplyFromString logs the manifest contents before applying to the cluster.
func KubectlApplyFromString(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifest string) {
	if lines := strings.Count(manifest, "\n"); lines <= maxLogLines {
		logger.Default.Logf(t, "Applying manifest from string:\n---\n%s\n---", manifest)
	}
	k8s.KubectlApplyFromStringContext(t, ctx, kubeOpts, manifest)
}
