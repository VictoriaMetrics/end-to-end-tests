package install

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

const (
	// webhookRetryAttempts is how many times to retry on transient webhook failures.
	webhookRetryAttempts = 5
	// webhookRetryDelay is the base delay between webhook retry attempts.
	webhookRetryDelay = 10 * time.Second
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

// KubectlApplyFromStringWithRetry applies a manifest string, retrying on transient webhook errors
// (e.g. "No agent available" from chaos-mesh before the controller is fully ready).
func KubectlApplyFromStringWithRetry(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifest string) {
	if lines := strings.Count(manifest, "\n"); lines <= maxLogLines {
		logger.Default.Logf(t, "Applying manifest from string:\n---\n%s\n---", manifest)
	}
	var lastErr error
	for attempt := 1; attempt <= webhookRetryAttempts; attempt++ {
		lastErr = k8s.KubectlApplyFromStringContextE(t, ctx, kubeOpts, manifest)
		if lastErr == nil {
			return
		}
		if !strings.Contains(lastErr.Error(), "No agent available") &&
			!strings.Contains(lastErr.Error(), "failed to call webhook") &&
			!strings.Contains(lastErr.Error(), "InternalError") {
			break
		}
		logger.Default.Logf(t, "kubectl apply webhook error (attempt %d/%d): %v — retrying in %s",
			attempt, webhookRetryAttempts, lastErr, webhookRetryDelay)
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while retrying kubectl apply: %v", ctx.Err())
		case <-time.After(webhookRetryDelay):
		}
	}
	if lastErr != nil {
		t.Fatalf("kubectl apply failed after %d attempts: %v", webhookRetryAttempts, lastErr)
	}
}
