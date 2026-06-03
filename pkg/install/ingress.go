package install

import (
	"context"
	"fmt"
	"net"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
)

// DiscoverIngressHost finds and records the external host/IP address of the
// ingress controller used by the test environment.
//
// Behavior:
//   - Waits for the `ingress-nginx-controller` deployment to be available.
//   - If the cluster distro (as returned by consts.EnvK8SDistro()) is "kind",
//     it assumes the ingress is accessible via localhost and sets the nginx host
//     to 127.0.0.1 immediately.
//   - For non-kind environments, it waits for the `ingress-nginx-controller`
//     Service to have a LoadBalancer ingress address and uses that address.
//
// The discovered host is stored via `consts.SetNginxHost` for consumption by
// other test helpers.
//
// Parameters:
// - ctx: context used for timeouts/cancellation while waiting for resources.
// - t: terratest testing interface used for running commands and assertions.
func DiscoverIngressHost(ctx context.Context, t terratesting.TestingT) {
	// If host was pre-configured (e.g. via -nginx-host flag from terraform output),
	// the IP is already known but the GKE LoadBalancer forwarding rule may not be
	// fully provisioned yet. Verify TCP port 80 is reachable before returning.
	if consts.NginxHost() != "" {
		logger.Default.Logf(t, "nginxHost pre-configured: %s, verifying TCP port 80 reachability...", consts.NginxHost())
		waitForTCPPort(ctx, t, consts.NginxHost(), 80)
		logger.Default.Logf(t, "nginxHost %s is reachable on port 80", consts.NginxHost())
		return
	}

	kubeOpts := k8s.NewKubectlOptions("", "", "ingress-nginx")

	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "ingress-nginx-controller", consts.Retries, consts.PollingInterval)

	var nginxHost string

	// For kind environments, use localhost immediately
	if consts.EnvK8SDistro() == "kind" {
		logger.Default.Logf(t, "Kind environment detected, using localhost")
		nginxHost = "127.0.0.1"
	} else {
		// For non-kind environments, watch the service until LoadBalancer.Ingress is set
		nginxHost = waitForIngressLoadBalancerIngress(ctx, t, kubeOpts)
	}

	logger.Default.Logf(t, "nginxHost: %s", nginxHost)

	// Set the discovered host in consts
	consts.SetNginxHost(nginxHost)
}

// waitForTCPPort polls host:port with TCP dial until the connection succeeds or
// ResourceWaitTimeout is exceeded. Fails the test on timeout.
func waitForTCPPort(ctx context.Context, t terratesting.TestingT, host string, port int) {
	addr := fmt.Sprintf("%s:%d", host, port)
	require.Eventually(t, func() bool {
		d := net.Dialer{Timeout: consts.HTTPClientTimeout}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, consts.ResourceWaitTimeout, consts.PollingInterval,
		"TCP port %s did not become reachable within timeout", addr)
}

// waitForIngressLoadBalancerIngress watches the ingress-nginx-controller Service until
// its status contains a LoadBalancer ingress IP, then returns that IP.
//
// It performs an initial check and if needed sets up a watch on the specific
// Service object. On error or timeout it will fail the test via the provided
// terratest testing interface.
func waitForIngressLoadBalancerIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) string {
	logger.Default.Logf(t, "Waiting for ingress-nginx-controller service to have LoadBalancer.Ingress set...")

	// Create Kubernetes client from kubeOpts
	clientset, err := k8s.GetKubernetesClientFromOptionsE(t, kubeOpts)
	if err != nil {
		t.Fatalf("Failed to create Kubernetes client: %v", err)
		return ""
	}

	// Create a context with timeout
	watchCtx, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	defer cancel()

	// First, check if the service already has LoadBalancer ingress
	svc, err := clientset.CoreV1().Services("ingress-nginx").Get(watchCtx, "ingress-nginx-controller", metav1.GetOptions{})
	if err != nil {
		if watchCtx.Err() != nil {
			t.Fatalf("timed out waiting for ingress-nginx-controller service: %v", watchCtx.Err())
		} else {
			t.Fatalf("Failed to get ingress-nginx-controller service: %v", err)
		}
		return ""
	}

	// Check if LoadBalancer ingress IP is already available
	if host := extractIngressHost(svc); host != "" {
		return host
	}

	// Set up field selector to watch only the specific service
	fieldSelector := fields.OneTermEqualSelector("metadata.name", "ingress-nginx-controller").String()

	// Create a watch for the service
	watcher, err := clientset.CoreV1().Services("ingress-nginx").Watch(watchCtx, metav1.ListOptions{
		FieldSelector: fieldSelector,
	})
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
		return ""
	}
	defer watcher.Stop()

	// Define the condition function
	conditionFunc := func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Modified, watch.Added:
			svc, ok := event.Object.(*corev1.Service)
			if !ok {
				return false, nil
			}
			// Check if LoadBalancer ingress IP is available
			if host := extractIngressHost(svc); host != "" {
				return true, nil
			}
			return false, nil
		default:
			return false, nil
		}
	}

	// Use watchtools.UntilWithoutRetry to watch for the condition
	event, err := watchtools.UntilWithoutRetry(watchCtx, watcher, conditionFunc)
	if err != nil {
		if watchCtx.Err() != nil {
			t.Fatalf("timed out waiting for LoadBalancer ingress: %v", watchCtx.Err())
		} else {
			t.Fatalf("Failed to watch for LoadBalancer ingress: %v", err)
		}
		return ""
	}

	// Extract the host from the final event
	if svc, ok := event.Object.(*corev1.Service); ok {
		if host := extractIngressHost(svc); host != "" {
			return host
		}
	}

	t.Fatalf("Failed to extract ingress host from final watch event")
	return ""
}

// extractIngressHost returns the IP address from the first LoadBalancer ingress
// entry of the provided Service, or an empty string if none is present. Only IP
// addresses are considered; hostnames are ignored by this helper.
func extractIngressHost(svc *corev1.Service) string {
	if len(svc.Status.LoadBalancer.Ingress) > 0 {
		ingress := svc.Status.LoadBalancer.Ingress[0]

		// Only use IP address
		if ingress.IP != "" {
			return ingress.IP
		}
	}
	return ""
}
