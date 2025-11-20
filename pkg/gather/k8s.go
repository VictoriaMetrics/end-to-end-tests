package gather

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cespare/xxhash/v2"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests/allure"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/testing"

	ginkgo "github.com/onsi/ginkgo/v2"
)

// K8sAfterAll provides cleanup and data collection logic for Kubernetes resources.
// It collects crust-gather information, archives it, and adds it to the report.
func K8sAfterAll(ctx context.Context, t testing.TestingT, kubeOpts *k8s.KubectlOptions, resourceWaitTimeout time.Duration) {
	timeBoundContext, cancel := context.WithTimeout(ctx, resourceWaitTimeout)
	defer cancel()

	// Delete license secret from cluster to avoid leaking it
	logger.Default.Logf(t, "Deleting license secret %s from cluster", consts.LicenseSecretName)
	if consts.LicenseFile() != "" {
		namespaces := k8s.ListNamespaces(t, kubeOpts, metav1.ListOptions{})
		for _, ns := range namespaces {
			nsKubeOpts := k8s.KubectlOptions{
				Namespace: ns.Name,
			}
			if _, err := k8s.GetSecretE(t, &nsKubeOpts, consts.LicenseSecretName); k8serrors.IsNotFound(err) {
				continue
			}
			cmd := exec.CommandContext(timeBoundContext, "kubectl", "delete", "secret", consts.LicenseSecretName, "-n", ns.Name)
			var outb, errb bytes.Buffer
			cmd.Stdout = &outb
			cmd.Stderr = &errb
			err := cmd.Run()
			if err != nil {
				logger.Default.Logf(t, "kubectl delete secret from namespace %s failed: %v, stdout: %s, stderr: %s", ns.Name, err, outb.String(), errb.String())
			}
		}
	}

	reportsLocation := "/tmp/crust-gather"
	report := ginkgo.CurrentSpecReport()
	reportHash := fmt.Sprintf("%016x", xxhash.Sum64([]byte(report.FullText())))
	reportDir := filepath.Join(reportsLocation, reportHash)

	// Collect crust-gather folder
	cmd := exec.CommandContext(timeBoundContext, "kubectl-crust-gather", "collect", "-v", "WARN", "-f", reportDir)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		logger.Default.Logf(t, "crust-gather collect failed: %v, stdout: %s, stderr: %s", err, outb.String(), errb.String())
	} else {
		if errb.Len() > 0 {
			logger.Default.Logf(t, "crust-gather collect stderr: %s", errb.String())
		}
	}

	// Archive crust-gather folder
	archiveName := reportHash + ".tar.gz"
	archivePath := filepath.Join(reportsLocation, archiveName)
	cmd = exec.CommandContext(timeBoundContext, "tar", "-czvf", archiveName, reportHash)
	cmd.Dir = reportsLocation
	outb.Reset()
	errb.Reset()
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()
	if err != nil {
		logger.Default.Logf(t, "tar command failed: %v, stdout: %s, stderr: %s", err, outb.String(), errb.String())
	} else {
		if errb.Len() > 0 {
			logger.Default.Logf(t, "tar command stderr: %s", errb.String())
		}
	}

	// Add crust-gather.tar.gz to report
	tarGzFileContent, err := os.ReadFile(archivePath)
	if err != nil {
		logger.Default.Logf(t, "failed to read %s: %v", archivePath, err)
	} else {
		logger.Default.Logf(t, "Saved crust-gather.tar.gz to %s", archivePath)
		allure.AddAttachment("crust-gather.tar.gz", allure.MimeTypeGZIP, tarGzFileContent)
	}
}
