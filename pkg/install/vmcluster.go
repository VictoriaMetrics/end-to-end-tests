package install

import (
	"context"
	"fmt"
	"os"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/clientcmd"
	watchtools "k8s.io/client-go/tools/watch"

	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

// InstallVMCluster installs a VMCluster custom resource into the target namespace.
//
// The function ensures the namespace exists, reads a VMCluster template manifest
// from the repository manifests, replaces occurrences of the hardcoded cluster
// name `vm` with the provided namespace (so multiple test namespaces can coexist),
// writes the modified manifest to a temporary file and applies it to the cluster.
// After applying the manifest it waits for the VMCluster to reach an operational
// state by calling WaitForVMClusterToBeOperational.
//
// Parameters:
// - ctx: context used for waiting operations (timeouts are applied by the wait helper).
// - t: terratest testing interface used for assertions and running kubectl operations.
// - kubeOpts: terratest KubectlOptions pointing at the cluster to operate against.
// - namespace: Kubernetes namespace where the VMCluster will be created.
// - vmclient: client for interacting with VictoriaMetrics Operator CRDs.
// - jsonPatches: list of json patches to apply to the VMCluster resource.
func InstallVMCluster(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, jsonPatches []jsonpatch.Patch) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceE(t, kubeOpts, namespace); err != nil {
		k8s.CreateNamespace(t, kubeOpts, namespace)
	}

	// Read VMCluster and patch it
	vmclusterYamlPath := "../../manifests/overwatch/vmcluster.yaml"
	vmclusterYaml, err := os.ReadFile(vmclusterYamlPath)
	require.NoError(t, err, "failed to read VMCluster YAML")

	vmclusterJson, err := yaml.YAMLToJSON(vmclusterYaml)
	require.NoError(t, err, "failed to convert VMCluster YAML to JSON")

	for _, patch := range jsonPatches {
		vmclusterJson, err = patch.Apply(vmclusterJson)
		require.NoError(t, err, "failed to apply patch")
	}

	// Apply the VMCluster manifest
	fmt.Printf("Installing VMCluster in namespace %s\n", namespace)
	vmclusterString := string(vmclusterJson)
	k8s.KubectlApplyFromString(t, kubeOpts, vmclusterString)

	// Wait for VMCluster to become operational
	WaitForVMClusterToBeOperational(ctx, t, kubeOpts, namespace, vmclient)

	// Expose VMSelect as ingress
	ExposeVMSelectAsIngress(ctx, t, kubeOpts, namespace)

	// Expose VMInsert as ingress
	ExposeVMInsertAsIngress(ctx, t, kubeOpts, namespace)

	// Wait for all pods to be running
	k8s.RunKubectl(t, kubeOpts, "wait", "--for=condition=Ready", "pods", "--all", fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))
}

// EnsureVMClusterComponents validates that the given VMCluster resource is properly configured
// and that its components' specifications look reasonable.
//
// The function fetches the VMCluster by name and performs basic checks such as:
// - retention period is set
// - VMStorage, VMSelect and VMInsert specs are present
// - replica counts and storage data path are set for VMStorage
// It also prints status information and reports non-fatal test errors through the
// provided testing interface when misconfigurations are detected.
//
// Parameters:
// - ctx: parent context for the operation (not used directly in this helper).
// - t: terratest testing interface used for assertions and reporting errors.
// - kubeOpts: terratest KubectlOptions (not used by the client but kept for symmetry).
// - namespace: Kubernetes namespace where the VMCluster resource is located.
// - vmclient: client for interacting with VictoriaMetrics Operator CRDs.
// - vmclusterName: name of the VMCluster custom resource to validate.
func EnsureVMClusterComponents(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, vmclusterName string) {
	// Get the VMCluster resource
	vmcluster, err := vmclient.OperatorV1beta1().VMClusters(namespace).Get(ctx, vmclusterName, metav1.GetOptions{})
	require.NoError(t, err)

	// Validate VMCluster specification
	if vmcluster.Spec.RetentionPeriod == "" {
		t.Errorf("VMCluster %s in namespace %s has empty retention period", vmclusterName, namespace)
	} else {
		fmt.Printf("VMCluster %s has retention period: %s\n", vmclusterName, vmcluster.Spec.RetentionPeriod)
	}

	// Validate VMStorage configuration
	if vmcluster.Spec.VMStorage == nil {
		t.Errorf("VMCluster %s in namespace %s has no VMStorage configuration", vmclusterName, namespace)
	} else {
		fmt.Printf("VMCluster %s VMStorage replica count: %d\n", vmclusterName, *vmcluster.Spec.VMStorage.ReplicaCount)
		if vmcluster.Spec.VMStorage.StorageDataPath == "" {
			t.Errorf("VMCluster %s VMStorage has empty storage data path", vmclusterName)
		}
	}

	// Validate VMSelect configuration
	if vmcluster.Spec.VMSelect == nil {
		t.Errorf("VMCluster %s in namespace %s has no VMSelect configuration", vmclusterName, namespace)
	} else {
		fmt.Printf("VMCluster %s VMSelect replica count: %d\n", vmclusterName, *vmcluster.Spec.VMSelect.ReplicaCount)
	}

	// Validate VMInsert configuration
	if vmcluster.Spec.VMInsert == nil {
		t.Errorf("VMCluster %s in namespace %s has no VMInsert configuration", vmclusterName, namespace)
	} else {
		fmt.Printf("VMCluster %s VMInsert replica count: %d\n", vmclusterName, *vmcluster.Spec.VMInsert.ReplicaCount)
	}

	// Check operational status
	if vmcluster.Status.UpdateStatus != "ExpandSuccess" && vmcluster.Status.UpdateStatus != "Operational" {
		fmt.Printf("VMCluster %s status: %s (reason: %s)\n", vmclusterName, vmcluster.Status.UpdateStatus, vmcluster.Status.Reason)
	} else {
		fmt.Printf("VMCluster %s is operational\n", vmclusterName)
	}
}

// GetVMClusterServiceEndpoints returns the DNS service endpoints for core VMCluster components.
//
// The returned endpoints point to the namespaced Kubernetes service addresses for
// VMInsert, VMSelect and VMStorage components for the given cluster name.
func GetVMClusterServiceEndpoints(namespace string, vmclusterName string) VMClusterEndpoints {
	return VMClusterEndpoints{
		VMInsert:  fmt.Sprintf("vminsert-%s.%s.svc.cluster.local:8480", vmclusterName, namespace),
		VMSelect:  fmt.Sprintf("vmselect-%s.%s.svc.cluster.local:8481", vmclusterName, namespace),
		VMStorage: fmt.Sprintf("vmstorage-%s.%s.svc.cluster.local:8482", vmclusterName, namespace),
	}
}

// VMClusterEndpoints holds the service endpoints for a VMCluster deployment.
type VMClusterEndpoints struct {
	VMInsert  string
	VMSelect  string
	VMStorage string
}

// DeleteVMCluster deletes the named VMCluster resource and waits for the corresponding
// deployments (vmstorage, vmselect, vminsert) to be removed from the cluster.
//
// The function issues a kubectl delete for the VMCluster and then waits for the
// deployments with names derived from vmclusterName to be deleted. In case of
// missing resources the delete is tolerant due to --ignore-not-found=true.
func DeleteVMCluster(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmclusterName string) {
	// Delete the VMCluster resource
	fmt.Printf("Deleting VMCluster %s\n", vmclusterName)
	k8s.RunKubectl(t, kubeOpts, "delete", "vmcluster", vmclusterName, "--ignore-not-found=true")

	// Wait for deployments to be deleted
	k8s.RunKubectl(t, kubeOpts, "wait", "--for=delete", "deployment", fmt.Sprintf("vminsert-%s", vmclusterName), "--timeout=60s")

	// Wait for statefulsets to be deleted
	k8s.RunKubectl(t, kubeOpts, "wait", "--for=delete", "statefulset", fmt.Sprintf("vmstorage-%s", vmclusterName), "--timeout=60s")
	k8s.RunKubectl(t, kubeOpts, "wait", "--for=delete", "statefulset", fmt.Sprintf("vmselect-%s", vmclusterName), "--timeout=60s")
}

// GetVMClient creates and returns a VictoriaMetrics operator clientset using the
// kubeconfig referenced by kubeOpts.
//
// The function reads the kubeconfig path from kubeOpts, builds a REST config and
// constructs a typed client for the VictoriaMetrics Operator CRDs.
func GetVMClient(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions) *vmclient.Clientset {
	kubeConfigPath, err := kubeOpts.GetConfigPath(t)
	require.NoError(t, err)
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath}, &clientcmd.ConfigOverrides{})
	restConfig, err := clientConfig.ClientConfig()
	require.NoError(t, err)
	vmclient := vmclient.NewForConfigOrDie(restConfig)
	require.NoError(t, err)
	return vmclient
}

// WaitForVMClusterToBeOperational watches a VMCluster custom resource until it reports an operational status.
//
// This helper uses a watch on VMCluster objects and returns when the cluster's
// Status.UpdateStatus equals UpdateStatusOperational. A timeout is applied using
// consts.ResourceWaitTimeout to avoid blocking indefinitely.
func WaitForVMClusterToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface) {
	watchInterface, err := vmclient.OperatorV1beta1().VMClusters(namespace).Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	defer watchInterface.Stop()

	timeBoundContext, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	defer cancel()

	_, err = watchtools.UntilWithoutRetry(timeBoundContext, watchInterface, func(event watch.Event) (bool, error) {
		obj := event.Object
		vmCluster := obj.(*vmv1beta1.VMCluster)
		if vmCluster.Status.UpdateStatus == vmv1beta1.UpdateStatusOperational {
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err)
}

const (
	ingressTemplate = `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
spec:
  ingressClassName: nginx
  rules:
  - host: %s-%s.%s.nip.io
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: %s-vm
            port:
              number: %d
`
)

func exposeServiceAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, serviceName string, servicePort int32) {
	ingressName := fmt.Sprintf("%s-%s", serviceName, namespace)

	ingress := fmt.Sprintf(ingressTemplate, ingressName, serviceName, namespace, consts.NginxHost(), serviceName, servicePort)
	k8s.KubectlApplyFromString(t, kubeOpts, ingress)

	k8s.WaitUntilIngressAvailable(t, kubeOpts, ingressName, consts.Retries, consts.PollingInterval)
}

func ExposeVMInsertAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, "vminsert", 8480)
}

func ExposeVMSelectAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, "vmselect", 8481)
}
