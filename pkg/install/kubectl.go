package install

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

const (
	// webhookRetryAttempts is how many times to retry on transient webhook failures.
	webhookRetryAttempts = 5
	// webhookRetryDelay is the base delay between webhook retry attempts.
	webhookRetryDelay = 10 * time.Second
	// webhookRetryFactor doubles the delay after each failed attempt.
	webhookRetryFactor = 2
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
	KubectlApplyFromStringWithRetry(ctx, t, kubeOpts, manifest)
}

// KubectlApplyFromStringWithRetry applies a manifest string, retrying on transient webhook errors
// (e.g. "No agent available" from chaos-mesh before the controller is fully ready).
func kubectlApplyFromStringContextE(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifest string) error {
	file, err := os.CreateTemp("", "kubectl_apply")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()

	if _, err := file.WriteString(manifest); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	logger.Default.Logf(t, "Running command: %s", kubectlApplyCommand(kubeOpts, file.Name()))
	_, err = k8s.RunKubectlAndGetOutputContextE(t, ctx, kubeOpts, "apply", "-f", file.Name())
	return err
}

func kubectlApplyArgs(kubeOpts *k8s.KubectlOptions, manifestPath string) []string {
	args := make([]string, 0, 10)
	if kubeOpts.ContextName != "" {
		args = append(args, "--context", kubeOpts.ContextName)
	}
	if kubeOpts.ConfigPath != "" {
		args = append(args, "--kubeconfig", kubeOpts.ConfigPath)
	}
	if kubeOpts.Namespace != "" {
		args = append(args, "--namespace", kubeOpts.Namespace)
	}
	if kubeOpts.RequestTimeout > 0 {
		args = append(args, "--request-timeout", kubeOpts.RequestTimeout.String())
	}
	return append(args, "apply", "-f", manifestPath)
}

func kubectlApplyCommand(kubeOpts *k8s.KubectlOptions, manifestPath string) string {
	return "kubectl " + strings.Join(kubectlApplyArgs(kubeOpts, manifestPath), " ")
}

func KubectlApplyFromStringWithRetry(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, manifest string) {
	if lines := strings.Count(manifest, "\n"); lines <= maxLogLines {
		logger.Default.Logf(t, "Applying manifest from string:\n---\n%s\n---", manifest)
	}
	var lastErr error
	backoff := wait.Backoff{Duration: webhookRetryDelay, Factor: webhookRetryFactor, Steps: webhookRetryAttempts}
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(backoffCtx context.Context) (bool, error) {
		lastErr = kubectlApplyFromStringContextE(backoffCtx, t, kubeOpts, manifest)
		if lastErr == nil {
			return true, nil
		}
		if !strings.Contains(lastErr.Error(), "No agent available") &&
			!strings.Contains(lastErr.Error(), "failed to call webhook") &&
			!strings.Contains(lastErr.Error(), "InternalError") {
			return false, lastErr
		}
		logger.Default.Logf(t, "kubectl apply webhook error: %v — retrying in %s", lastErr, webhookRetryDelay)
		return false, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("context cancelled while retrying kubectl apply: %v", ctx.Err())
		}
		t.Fatalf("kubectl apply failed after %d attempts: %v", webhookRetryAttempts, lastErr)
	}
}

// EnsureVPACRDs installs the VerticalPodAutoscaler CRDs if they are not already present.
func EnsureVPACRDs(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) {
	_, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts, "get", "crd", "verticalpodautoscalers.autoscaling.k8s.io")
	if err == nil {
		return
	}
	KubectlApply(ctx, t, kubeOpts, consts.VPACRDsYaml())
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", "--for=condition=Established",
		"crd", "verticalpodautoscalers.autoscaling.k8s.io",
		"verticalpodautoscalercheckpoints.autoscaling.k8s.io",
		fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
}

// EnsureGatewayAPICRDs installs Gateway API CRDs if they are not already present.
func EnsureGatewayAPICRDs(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) {
	_, err := k8s.RunKubectlAndGetOutputE(t, kubeOpts, "get", "crd", "httproutes.gateway.networking.k8s.io")
	if err == nil {
		return
	}
	k8s.RunKubectlContext(t, ctx, kubeOpts, "apply", "-f", consts.GatewayAPIStandardInstallURL())
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", "--for=condition=Established",
		"crd", "gatewayclasses.gateway.networking.k8s.io",
		"gateways.gateway.networking.k8s.io",
		"httproutes.gateway.networking.k8s.io",
		"referencegrants.gateway.networking.k8s.io",
		fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
}
