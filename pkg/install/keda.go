package install

import (
	"context"
	"fmt"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2" //nolint:stylecheck,staticcheck

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

// InstallKEDA installs the KEDA operator Helm chart into the given namespace.
//
// The function performs a Helm upgrade (creates the release if absent) using
// the bundled values file and waits until the KEDA operator and metrics
// apiserver deployments are available.
//
// Parameters:
// - ctx: parent context for waiting operations.
// - t: terratest testing interface used for applying manifests and assertions.
// - namespace: Kubernetes namespace in which to install KEDA.
func InstallKEDA(ctx context.Context, t terratesting.TestingT, namespace string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	helmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{consts.KEDAValuesFile()},
		ExtraArgs: map[string][]string{
			"upgrade": {"--create-namespace", "--wait", "--timeout", "10m"},
		},
	}

	By(fmt.Sprintf("Install %s chart", consts.KEDAChart))
	err := helm.UpgradeE(t, helmOpts, consts.KEDAChart, consts.KEDAReleaseName)
	if err != nil {
		t.Fatalf("Failed to install KEDA chart: %v", err)
	}

	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "keda-operator", consts.Retries, consts.PollingInterval)
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "keda-operator-metrics-apiserver", consts.Retries, consts.PollingInterval)
}

// buildScaledObjectYAML returns a KEDA ScaledObject manifest that targets the
// given Deployment or StatefulSet and uses a Prometheus trigger against the
// overwatch VMSingle.
func buildScaledObjectYAML(name, namespace, kind, targetName, overwatchSvc, metricName, query string, threshold int) string {
	return fmt.Sprintf(`apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: %s
  namespace: %s
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: %s
    name: %s
  minReplicaCount: 1
  maxReplicaCount: 3
  cooldownPeriod: 30
  pollingInterval: 15
  triggers:
  - type: prometheus
    metadata:
      serverAddress: http://%s
      metricName: %s
      threshold: "%d"
      query: '%s'
`, name, namespace, kind, targetName, overwatchSvc, metricName, threshold, query)
}

// InstallKEDAScaledObjects creates KEDA ScaledObjects for every VMCluster component
// (vminsert, vmselect, vmstorage) and for the requestsLoadBalancer VMAuth deployment.
//
// Each ScaledObject uses a Prometheus trigger that queries the overwatch VMSingle
// for per-component request or row-insertion rates and scales the target workload
// between 1 and 3 replicas.
//
// The requestsLoadBalancer (enabled via VMCluster.spec.requestsLoadBalancer) is
// managed by the VM operator as a VMAuth Deployment named vmauth-<clusterName>.
//
// Parameters:
// - ctx: parent context for kubectl operations.
// - t: terratest testing interface used for applying manifests.
// - kubeOpts: kubectl options pointing at the target namespace.
// - namespace: Kubernetes namespace of the VMCluster deployment.
// - clusterName: name of the VMCluster resource (and its component workloads).
func InstallKEDAScaledObjects(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, clusterName string) {
	overwatchSvc := consts.GetVMSingleSvc("overwatch", consts.OverwatchNamespace)

	type scaledComponent struct {
		name       string
		kind       string
		targetName string
		metricName string
		query      string
		threshold  int
	}

	components := []scaledComponent{
		{
			name:       fmt.Sprintf("vminsert-%s", clusterName),
			kind:       "Deployment",
			targetName: fmt.Sprintf("vminsert-%s", clusterName),
			metricName: "vminsert_http_requests",
			query:      fmt.Sprintf(`sum(rate(vm_http_requests_total{namespace="%s",job=~"vminsert.*"}[1m])) or vector(0)`, namespace),
			threshold:  50,
		},
		{
			name:       fmt.Sprintf("vmselect-%s", clusterName),
			kind:       "StatefulSet",
			targetName: fmt.Sprintf("vmselect-%s", clusterName),
			metricName: "vmselect_http_requests",
			query:      fmt.Sprintf(`sum(rate(vm_http_requests_total{namespace="%s",job=~"vmselect.*"}[1m])) or vector(0)`, namespace),
			threshold:  20,
		},
		{
			name:       fmt.Sprintf("vmstorage-%s", clusterName),
			kind:       "StatefulSet",
			targetName: fmt.Sprintf("vmstorage-%s", clusterName),
			metricName: "vmstorage_rows_inserted",
			query:      fmt.Sprintf(`sum(rate(vm_rows_inserted_total{namespace="%s"}[1m])) or vector(0)`, namespace),
			threshold:  50000,
		},
		{
			// The requestsLoadBalancer creates a VMAuth Deployment named vmauth-<clusterName>.
			name:       fmt.Sprintf("vmauth-%s", clusterName),
			kind:       "Deployment",
			targetName: fmt.Sprintf("vmauth-%s", clusterName),
			metricName: "vmauth_http_requests",
			query:      fmt.Sprintf(`sum(rate(vm_http_requests_total{namespace="%s",job=~"vmauth.*"}[1m])) or vector(0)`, namespace),
			threshold:  50,
		},
	}

	By(fmt.Sprintf("Installing KEDA ScaledObjects for VMCluster %s in namespace %s", clusterName, namespace))
	for _, c := range components {
		manifest := buildScaledObjectYAML(
			c.name, namespace,
			c.kind, c.targetName,
			overwatchSvc,
			c.metricName,
			c.query,
			c.threshold,
		)
		KubectlApplyFromString(ctx, t, kubeOpts, manifest)
	}
}
