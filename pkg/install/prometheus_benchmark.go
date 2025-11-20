package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2" //nolint
)

const (
	PrometheusBenchmarkRepoURL     = "https://github.com/VictoriaMetrics/prometheus-benchmark"
	PrometheusBenchmarkClonePath   = "/tmp/prometheus-benchmark"
	PrometheusBenchmarkReleaseName = "prometheus-benchmark"
)

// InstallPrometheusBenchmark clones the prometheus-benchmark repo and installs the helm chart.
func InstallPrometheusBenchmark(ctx context.Context, t terratesting.TestingT, namespace string, setValues map[string]string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)

	By("Clone prometheus-benchmark repository")
	// Clean up existing clone
	_ = os.RemoveAll(PrometheusBenchmarkClonePath)
	// require.NoError(t, err)

	cmd := exec.CommandContext(ctx, "git", "clone", PrometheusBenchmarkRepoURL, PrometheusBenchmarkClonePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to clone repository: %s, output: %s", err, string(output))
	}

	chartPath := filepath.Join(PrometheusBenchmarkClonePath, "chart")

	helmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		SetValues:      setValues,
		ExtraArgs: map[string][]string{
			"upgrade": {"--create-namespace", "--wait", "--timeout", "10m"},
		},
	}

	By("Install prometheus-benchmark chart")
	err = helm.UpgradeE(t, helmOpts, chartPath, PrometheusBenchmarkReleaseName)
	if err != nil {
		t.Fatalf("Failed to install prometheus-benchmark chart: %v", err)
	}
}
