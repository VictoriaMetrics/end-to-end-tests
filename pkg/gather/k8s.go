package gather

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// SecretRef identifies a Kubernetes secret by name and namespace.
type SecretRef struct {
	Name      string
	Namespace string
}

// K8sAfterAll provides cleanup and data collection logic for Kubernetes resources.
// It collects crust-gather information, archives it, and adds it to the report.
func K8sAfterAll(ctx context.Context, t testing.TestingT, kubeOpts *k8s.KubectlOptions, resourceWaitTimeout time.Duration) {
	timeBoundContext, cancel := context.WithTimeout(ctx, resourceWaitTimeout)
	defer cancel()

	reportsLocation := "/tmp/crust-gather"
	report := ginkgo.CurrentSpecReport()
	reportHash := fmt.Sprintf("%016x", xxhash.Sum64([]byte(report.FullText())))
	reportDir := filepath.Join(reportsLocation, reportHash)

	// Collect crust-gather folder; pass sensitive values via --secrets-file to redact from output
	crustGatherArgs := []string{"collect", "-v", "WARN", "--exclude-kind", "Event", "-f", reportDir}
	var secretRefs []SecretRef
	namespaces := k8s.ListNamespaces(t, kubeOpts, metav1.ListOptions{})
	for _, ns := range namespaces {
		if consts.LicenseFile() != "" {
			secretRefs = append(secretRefs, SecretRef{Name: consts.LicenseSecretName, Namespace: ns.Name})
		}
		if os.Getenv("MDX_PASSWORD") != "" {
			secretRefs = append(secretRefs, SecretRef{Name: consts.MDXRemoteWriteSecretName, Namespace: ns.Name})
		}
	}
	if secretsFilePath := collectSecretsFile(t, secretRefs); secretsFilePath != "" {
		defer os.Remove(secretsFilePath) //nolint:errcheck
		crustGatherArgs = append(crustGatherArgs, "--secrets-file", secretsFilePath)
	}
	logger.Default.Logf(t, "Running crust-gather %s", crustGatherArgs)
	cmd := exec.CommandContext(timeBoundContext, "kubectl-crust-gather", crustGatherArgs...)
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
	if err := os.RemoveAll(filepath.Join(reportDir, "namespaces", "kube-system", "v1")); err != nil {
		logger.Default.Logf(t, "failed to remove kube-system from crust-gather report: %v", err)
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

// collectSecretsFile reads data from the given K8s secrets and writes their values
// to a temp file suitable for crust-gather's --secrets-file flag.
// Returns the temp file path, or "" if no secret data was found.
// Not-found secrets are silently skipped.
func collectSecretsFile(t testing.TestingT, refs []SecretRef) string {
	var lines []string
	for _, ref := range refs {
		opts := k8s.KubectlOptions{Namespace: ref.Namespace}
		secret, err := k8s.GetSecretE(t, &opts, ref.Name)
		if k8serrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			logger.Default.Logf(t, "failed to get secret %s/%s for crust-gather: %v", ref.Namespace, ref.Name, err)
			continue
		}
		for _, v := range secret.Data {
			lines = append(lines, strings.TrimSpace(string(v)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	tmpFile, err := os.CreateTemp("", "crust-gather-secrets-*")
	if err != nil {
		logger.Default.Logf(t, "failed to create secrets file for crust-gather: %v", err)
		return ""
	}
	_, _ = tmpFile.WriteString(strings.Join(lines, "\n"))
	tmpFile.Close()
	return tmpFile.Name()
}
