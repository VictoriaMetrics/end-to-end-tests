package install

import (
	"fmt"
	"os"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

// KubectlApply logs the manifest file contents before applying to the cluster.
func KubectlApply(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifestPath string) {
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Printf("WARNING: could not read manifest file %s: %v\n", manifestPath, err)
	} else {
		fmt.Printf("Applying manifest from %s:\n---\n%s\n---\n", manifestPath, string(content))
	}
	k8s.KubectlApply(t, kubeOpts, manifestPath)
}

// KubectlApplyFromString logs the manifest contents before applying to the cluster.
func KubectlApplyFromString(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifest string) {
	fmt.Printf("Applying manifest from string:\n---\n%s\n---\n", manifest)
	k8s.KubectlApplyFromString(t, kubeOpts, manifest)
}
