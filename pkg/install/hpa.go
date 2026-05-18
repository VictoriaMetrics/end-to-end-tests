package install

import (
	"context"
	"fmt"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2" //nolint:stylecheck,staticcheck
)

// hpaSpec holds the parameters needed to render a single HPA manifest.
type hpaSpec struct {
	name                        string
	namespace                   string
	kind                        string // Deployment or StatefulSet
	targetName                  string
	minReplicas                 int
	maxReplicas                 int
	cpuUtilization              int
	memUtilization              int
	scaleUpStabilizationSecs    int
	scaleDownStabilizationSecs  int
}

// buildHPAManifest renders an autoscaling/v2 HPA manifest from hpaSpec.
func buildHPAManifest(s hpaSpec) string {
	return fmt.Sprintf(`apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: %s
  namespace: %s
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: %s
    name: %s
  minReplicas: %d
  maxReplicas: %d
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: %d
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: %d
  behavior:
    scaleUp:
      stabilizationWindowSeconds: %d
      policies:
      - type: Pods
        value: 1
        periodSeconds: 30
    scaleDown:
      stabilizationWindowSeconds: %d
      policies:
      - type: Pods
        value: 1
        periodSeconds: 60
`,
		s.name, s.namespace, s.kind, s.targetName,
		s.minReplicas, s.maxReplicas,
		s.cpuUtilization, s.memUtilization,
		s.scaleUpStabilizationSecs, s.scaleDownStabilizationSecs,
	)
}

// InstallHPAForLB creates a Kubernetes HorizontalPodAutoscaler targeting the
// requestsLoadBalancer VMAuth Deployment (vmauth-<clusterName>).
//
// Scaling is driven by average CPU utilization (target 50%) and average memory
// utilization (target 80%), with replicas bounded between 1 and 3.
func InstallHPAForLB(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, clusterName string) {
	spec := hpaSpec{
		name:                       fmt.Sprintf("vmauth-%s-hpa", clusterName),
		namespace:                  namespace,
		kind:                       "Deployment",
		targetName:                 fmt.Sprintf("vmauth-%s", clusterName),
		minReplicas:                1,
		maxReplicas:                3,
		cpuUtilization:             50,
		memUtilization:             80,
		scaleUpStabilizationSecs:   30,
		scaleDownStabilizationSecs: 120,
	}
	By(fmt.Sprintf("Installing HPA %s for requestsLoadBalancer %s", spec.name, spec.targetName))
	KubectlApplyFromString(ctx, t, kubeOpts, buildHPAManifest(spec))
}

// InstallHPAForVMCluster creates HorizontalPodAutoscalers for each VMCluster
// component: vminsert (Deployment), vmselect (StatefulSet), and vmstorage
// (StatefulSet).
//
// All components scale between 2 and 4 replicas driven by CPU and memory
// utilization. vmstorage uses a longer scale-down stabilization window
// (300 s vs 120 s) to reduce the risk of data redistribution churn.
func InstallHPAForVMCluster(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, clusterName string) {
	components := []hpaSpec{
		{
			name:                       fmt.Sprintf("vminsert-%s-hpa", clusterName),
			namespace:                  namespace,
			kind:                       "Deployment",
			targetName:                 fmt.Sprintf("vminsert-%s", clusterName),
			minReplicas:                2,
			maxReplicas:                4,
			cpuUtilization:             50,
			memUtilization:             80,
			scaleUpStabilizationSecs:   30,
			scaleDownStabilizationSecs: 120,
		},
		{
			name:                       fmt.Sprintf("vmselect-%s-hpa", clusterName),
			namespace:                  namespace,
			kind:                       "StatefulSet",
			targetName:                 fmt.Sprintf("vmselect-%s", clusterName),
			minReplicas:                2,
			maxReplicas:                4,
			cpuUtilization:             50,
			memUtilization:             80,
			scaleUpStabilizationSecs:   30,
			scaleDownStabilizationSecs: 120,
		},
		{
			// vmstorage: conservative scale-down — shrinking the StatefulSet
			// triggers data rebalancing across remaining shards.
			name:                       fmt.Sprintf("vmstorage-%s-hpa", clusterName),
			namespace:                  namespace,
			kind:                       "StatefulSet",
			targetName:                 fmt.Sprintf("vmstorage-%s", clusterName),
			minReplicas:                2,
			maxReplicas:                4,
			cpuUtilization:             50,
			memUtilization:             80,
			scaleUpStabilizationSecs:   30,
			scaleDownStabilizationSecs: 300,
		},
	}

	By(fmt.Sprintf("Installing HPAs for VMCluster components in namespace %s", namespace))
	for _, s := range components {
		KubectlApplyFromString(ctx, t, kubeOpts, buildHPAManifest(s))
	}
}
