package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

// vmclusterImageSpec is used when patching explicit image coordinates into a VMCluster manifest.
type vmclusterImageSpec struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

type vmclusterIngressReadiness struct {
	ClusterName   string
	VMInsertHTTPS bool
	VMSelectHTTPS bool
	VMInsertMTLS  bool
	VMSelectMTLS  bool
}

type vmclusterReadinessSpec struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		VMInsert struct {
			ExtraArgs map[string]string `json:"extraArgs"`
		} `json:"vminsert"`
		VMSelect struct {
			ExtraArgs map[string]string `json:"extraArgs"`
		} `json:"vmselect"`
	} `json:"spec"`
}

// buildVMClusterImagePatch creates a JSON patch that sets explicit image repository/tag
// values for each VMCluster component based on the consts configured via test flags.
// Components whose image or version consts are empty are skipped so that the operator
// default still applies for those components.
func buildVMClusterImagePatch() (jsonpatch.Patch, error) {
	type componentImage struct {
		path  string
		image string
		tag   string
	}
	components := []componentImage{
		{"/spec/vmselect/image", consts.VMClusterVMSelectDefaultImage(), consts.VMClusterVMSelectDefaultVersion()},
		{"/spec/vminsert/image", consts.VMClusterVMInsertDefaultImage(), consts.VMClusterVMInsertDefaultVersion()},
		{"/spec/vmstorage/image", consts.VMClusterVMStorageDefaultImage(), consts.VMClusterVMStorageDefaultVersion()},
	}

	var ops []PatchOp
	for _, c := range components {
		if c.image == "" || c.tag == "" {
			continue
		}
		ops = append(ops, PatchOp{
			Op:    "add",
			Path:  c.path,
			Value: vmclusterImageSpec{Repository: c.image, Tag: c.tag},
		})
	}
	if len(ops) == 0 {
		return jsonpatch.Patch{}, nil
	}
	return CreateJsonPatch(ops)
}

// InstallVMCluster installs a VMCluster custom resource into the target namespace.
//
// The function ensures the namespace exists, reads a VMCluster template manifest
// from the repository manifests, replaces occurrences of the hardcoded cluster
// name `vm` with the provided namespace (so multiple test namespaces can coexist),
// writes the modified manifest to a temporary file and applies it to the cluster.
// After applying the manifest it waits for the VMCluster to reach an operational
// state within the provided timeout.
func InstallVMCluster(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, jsonPatches []jsonpatch.Patch, operationalTimeout time.Duration) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}
	ensureLicenseSecret(t, kubeOpts, namespace)
	jsonPatches = appendLicensePatch(t, jsonPatches)

	// Read VMCluster and patch it
	vmclusterYamlPath := consts.ManifestsRoot() + "/overwatch/vmcluster.yaml"
	vmclusterYaml, err := os.ReadFile(vmclusterYamlPath)
	require.NoError(t, err, "failed to read VMCluster YAML")

	vmclusterJson, err := yaml.YAMLToJSON(vmclusterYaml)
	require.NoError(t, err, "failed to convert VMCluster YAML to JSON")

	// Apply explicit image versions from test flags before caller patches so that
	// the VMCluster does not depend on operator default env vars being up-to-date.
	imagePatch, err := buildVMClusterImagePatch()
	require.NoError(t, err, "failed to build VMCluster image patch")
	if len(imagePatch) > 0 {
		vmclusterJson, err = imagePatch.Apply(vmclusterJson)
		require.NoError(t, err, "failed to apply VMCluster image patch")
	}

	for _, patch := range jsonPatches {
		vmclusterJson, err = patch.Apply(vmclusterJson)
		require.NoError(t, err, "failed to apply patch")
	}
	readiness := vmclusterIngressReadinessFromSpec(t, vmclusterJson)

	// Apply the VMCluster manifest
	helpers.Logf("Installing VMCluster in namespace %s", namespace)
	vmclusterString := string(vmclusterJson)
	KubectlApplyFromString(ctx, t, kubeOpts, vmclusterString)

	// Wait for VMCluster to become operational
	helpers.Logf("Waiting for VMCluster to become operational in namespace %s", namespace)
	WaitForVMClusterToBeOperational(ctx, t, kubeOpts, namespace, readiness.ClusterName, vmclient, operationalTimeout)

	// Wait only for VMCluster pods. The namespace may contain completed k6 job pods,
	// which never become Ready again and would make a namespace-wide wait fail.
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pods",
		"-l", fmt.Sprintf("managed-by=vm-operator,app.kubernetes.io/instance=%s", readiness.ClusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))

	// Expose VMSelect as ingress
	helpers.Logf("Configuring VMSelect ingress in namespace %s, https %t", namespace, readiness.VMSelectHTTPS)
	ExposeVMSelectAsIngress(ctx, t, kubeOpts, namespace, readiness)

	// Expose VMInsert as ingress
	helpers.Logf("Configuring VMInsert ingress in namespace %s, https %t", namespace, readiness.VMInsertHTTPS)
	ExposeVMInsertAsIngress(ctx, t, kubeOpts, namespace, readiness)
}

func vmclusterIngressReadinessFromSpec(t terratesting.TestingT, vmclusterJSON []byte) vmclusterIngressReadiness {
	var spec vmclusterReadinessSpec
	require.NoError(t, json.Unmarshal(vmclusterJSON, &spec), "failed to parse VMCluster JSON")

	return vmclusterIngressReadiness{
		ClusterName:   spec.Metadata.Name,
		VMInsertHTTPS: spec.Spec.VMInsert.ExtraArgs["tls"] == "true",
		VMSelectHTTPS: spec.Spec.VMSelect.ExtraArgs["tls"] == "true",
		VMInsertMTLS:  spec.Spec.VMInsert.ExtraArgs["mtls"] == "true",
		VMSelectMTLS:  spec.Spec.VMSelect.ExtraArgs["mtls"] == "true",
	}
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
func EnsureVMClusterComponents(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, vmclusterName string) {
	// Get the VMCluster resource
	vmcluster, err := vmclient.OperatorV1beta1().VMClusters(namespace).Get(ctx, vmclusterName, metav1.GetOptions{})
	require.NoError(t, err)

	// Validate VMCluster specification
	if vmcluster.Spec.RetentionPeriod == "" {
		t.Errorf("VMCluster %s in namespace %s has empty retention period", vmclusterName, namespace)
	} else {
		helpers.Logf("VMCluster %s has retention period: %s", vmclusterName, vmcluster.Spec.RetentionPeriod)
	}

	// Validate VMStorage configuration
	if vmcluster.Spec.VMStorage == nil {
		t.Errorf("VMCluster %s in namespace %s has no VMStorage configuration", vmclusterName, namespace)
	} else {
		helpers.Logf("VMCluster %s VMStorage replica count: %d", vmclusterName, *vmcluster.Spec.VMStorage.ReplicaCount)
		if vmcluster.Spec.VMStorage.StorageDataPath == "" {
			t.Errorf("VMCluster %s VMStorage has empty storage data path", vmclusterName)
		}
	}

	// Validate VMSelect configuration
	if vmcluster.Spec.VMSelect == nil {
		t.Errorf("VMCluster %s in namespace %s has no VMSelect configuration", vmclusterName, namespace)
	} else {
		helpers.Logf("VMCluster %s VMSelect replica count: %d", vmclusterName, *vmcluster.Spec.VMSelect.ReplicaCount)
	}

	// Validate VMInsert configuration
	if vmcluster.Spec.VMInsert == nil {
		t.Errorf("VMCluster %s in namespace %s has no VMInsert configuration", vmclusterName, namespace)
	} else {
		helpers.Logf("VMCluster %s VMInsert replica count: %d", vmclusterName, *vmcluster.Spec.VMInsert.ReplicaCount)
	}

	// Check operational status
	if vmcluster.Status.UpdateStatus != "ExpandSuccess" && vmcluster.Status.UpdateStatus != "Operational" {
		helpers.Logf("VMCluster %s status: %s (reason: %s)", vmclusterName, vmcluster.Status.UpdateStatus, vmcluster.Status.Reason)
	} else {
		helpers.Logf("VMCluster %s is operational", vmclusterName)
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
	helpers.Logf("Deleting VMCluster %s", vmclusterName)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vmcluster", vmclusterName, "--ignore-not-found=true")

	// Wait for deployments to be deleted
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "deployment", fmt.Sprintf("vminsert-%s", vmclusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))

	// Wait for statefulsets to be deleted
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "statefulset", fmt.Sprintf("vmstorage-%s", vmclusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "statefulset", fmt.Sprintf("vmselect-%s", vmclusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))
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

// WaitForVMClusterToBeOperational polls a VMCluster custom resource until it reports an operational status.
//
// This helper polls VMCluster objects at consts.PollingInterval and returns when the cluster's
// Status.UpdateStatus equals UpdateStatusOperational or the timeout expires.
//
// name restricts the check to a single VMCluster; pass "" to check every VMCluster in the
// namespace (the previous behavior). Namespaces shared by multiple VMClusters must pass a name,
// otherwise an unrelated cluster's failure is misattributed to whichever caller happens to be
// polling at the time.
//
// Fast-fail conditions (no timeout wait):
//   - VMCluster status.UpdateStatus == "failed": operator gave up; reason is surfaced immediately.
//   - Any vm-operator pod has InvalidImageName: the pod specification is invalid and cannot
//     recover without intervention.
func WaitForVMClusterToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, name string, vmclient vmclient.Interface, timeout time.Duration) {
	// "actual pod count: 0 less than needed" is transient: the operator may report it during
	// initial PVC provisioning (WaitForFirstConsumer storage class) before pods are created,
	// and recovers once PVCs bind and pods start.
	helpers.WaitForOperational(ctx, t, kubeOpts, timeout, "VMCluster", namespace, func(fctx context.Context) ([]helpers.ResourceStatus, error) {
		list, err := vmclient.OperatorV1beta1().VMClusters(namespace).List(fctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		var result []helpers.ResourceStatus
		for i := range list.Items {
			if name != "" && list.Items[i].Name != name {
				continue
			}
			result = append(result, helpers.ResourceStatus{Name: list.Items[i].Name, Status: list.Items[i].Status.UpdateStatus, Reason: list.Items[i].Status.Reason})
		}
		return result, nil
	}, "actual pod count: 0 less than needed")
}

// UpdateVMClusterSpec fetches the named VMCluster, applies mutate to its Spec,
// and saves the result back to the API server. The update is retried on conflict
// using the standard Kubernetes retry policy. After a successful update the
// function waits for the cluster to return to operational status.
// If ctx is cancelled before or during the operation the function returns
// silently, allowing it to be used inside a goroutine that is stopped via
// context cancellation.
func UpdateVMClusterSpec(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, clusterName string, client vmclient.Interface, mutate func(*vmv1beta1.VMClusterSpec)) {
	updateVMClusterSpec(ctx, t, namespace, clusterName, client, mutate)
	WaitForVMClusterToBeOperational(ctx, t, kubeOpts, namespace, clusterName, client, consts.VMClusterWaitTimeout)
}

// RestartVMStoragePods deletes all vmstorage pods for the given cluster so that the
// StatefulSet controller recreates them with the current template. Required when
// the StatefulSet uses OnDelete update strategy, where spec changes are only applied
// to pods after manual deletion.
func RestartVMStoragePods(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, clusterName string) {
	if ctx.Err() != nil {
		return
	}
	k8s.RunKubectlContext(t, ctx, kubeOpts,
		"delete", "pods",
		"-l", fmt.Sprintf("app.kubernetes.io/name=vmstorage,app.kubernetes.io/instance=%s", clusterName),
		"--wait=false",
	)
}

func updateVMClusterSpec(ctx context.Context, t terratesting.TestingT, namespace, clusterName string, client vmclient.Interface, mutate func(*vmv1beta1.VMClusterSpec)) {
	if ctx.Err() != nil {
		return
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cluster, err := client.OperatorV1beta1().VMClusters(namespace).Get(ctx, clusterName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutate(&cluster.Spec)
		_, err = client.OperatorV1beta1().VMClusters(namespace).Update(ctx, cluster, metav1.UpdateOptions{})
		return err
	})
	if err != nil && ctx.Err() == nil {
		require.NoError(t, err)
	}
}

func exposeServiceAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, clusterName, serviceName string, servicePort int32, https bool) {
	ingressName := fmt.Sprintf("%s-%s", serviceName, namespace)
	ingress, err := helpers.BuildIngressManifest(ingressName, fmt.Sprintf("%s-%s.%s.nip.io", serviceName, namespace, consts.IngressHost()), fmt.Sprintf("%s-%s", serviceName, clusterName), servicePort, https)
	require.NoError(t, err, "failed to build ingress manifest")
	KubectlApplyFromString(ctx, t, kubeOpts, ingress)
}

func ExposeVMInsertAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, readiness vmclusterIngressReadiness) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, readiness.ClusterName, "vminsert", 8480, readiness.VMInsertHTTPS)
	// mTLS requires a client certificate; the ingress cannot provide one, so skip the health check.
	if readiness.VMInsertMTLS {
		return
	}
	scheme := "http"
	if readiness.VMInsertHTTPS {
		scheme = "https"
	}
	helpers.WaitForHTTPRoute(ctx, t, fmt.Sprintf("%s://%s/health", scheme, consts.VMInsertHost(namespace)))
}

func ExposeVMSelectAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, readiness vmclusterIngressReadiness) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, readiness.ClusterName, "vmselect", 8481, readiness.VMSelectHTTPS)
	// mTLS requires a client certificate; the ingress cannot provide one, so skip the health check.
	if readiness.VMSelectMTLS {
		return
	}
	scheme := "http"
	if readiness.VMSelectHTTPS {
		scheme = "https"
	}
	helpers.WaitForHTTPRoute(ctx, t, fmt.Sprintf("%s://%s/select/0/prometheus/api/v1/query?query=%s", scheme, consts.VMSelectHost(namespace), url.QueryEscape("1")))
}
