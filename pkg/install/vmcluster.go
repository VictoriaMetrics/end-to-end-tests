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

func vmclusterLicensePatch() (jsonpatch.Patch, error) {
	patchJSON := fmt.Sprintf(`[{
		"op": "add",
		"path": "/spec/license",
		"value": {"keyRef": {"name": %q, "key": %q}}
	}]`, consts.LicenseSecretName, consts.LicenseSecretKey)
	return jsonpatch.DecodePatch([]byte(patchJSON))
}

func appendVMClusterLicensePatch(t terratesting.TestingT, jsonPatches []jsonpatch.Patch) []jsonpatch.Patch {
	if consts.LicenseFile() == "" {
		return jsonPatches
	}

	patch, err := vmclusterLicensePatch()
	require.NoError(t, err)
	return append(jsonPatches, patch)
}

func ensureVMClusterLicenseSecret(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	if consts.LicenseFile() == "" {
		return
	}

	secretYaml, err := consts.PrepareLicenseSecret(namespace)
	require.NoError(t, err)

	// Avoid KubectlApplyFromString wrapper here; it logs manifest contents.
	k8s.KubectlApplyFromString(t, kubeOpts, secretYaml)
}

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
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}
	ensureVMClusterLicenseSecret(t, kubeOpts, namespace)
	jsonPatches = appendVMClusterLicensePatch(t, jsonPatches)

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
	WaitForVMClusterToBeOperational(ctx, t, kubeOpts, namespace, vmclient)

	// Wait for all pods to be running
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pods", "--all", fmt.Sprintf("--timeout=%s", consts.ResourceWaitTimeout))

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
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "deployment", fmt.Sprintf("vminsert-%s", vmclusterName), "--timeout=60s")

	// Wait for statefulsets to be deleted
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "statefulset", fmt.Sprintf("vmstorage-%s", vmclusterName), "--timeout=60s")
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "statefulset", fmt.Sprintf("vmselect-%s", vmclusterName), "--timeout=60s")
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
// Status.UpdateStatus equals UpdateStatusOperational. A timeout of consts.ResourceWaitTimeout is
// applied to avoid blocking indefinitely.
func WaitForVMClusterToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface) {
	if ctx.Err() != nil {
		return
	}

	timeBoundContext, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	defer cancel()

	ticker := time.NewTicker(consts.PollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeBoundContext.Done():
			if ctx.Err() == nil {
				require.NoError(t, fmt.Errorf("timed out waiting for VMCluster in namespace %s to become operational", namespace))
			}
			return
		case <-ticker.C:
			list, err := vmclient.OperatorV1beta1().VMClusters(namespace).List(timeBoundContext, metav1.ListOptions{})
			if err != nil {
				continue
			}
			for i := range list.Items {
				if list.Items[i].Status.UpdateStatus == vmv1beta1.UpdateStatusOperational {
					return
				}
			}
		}
	}
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
            name: %s-%s
            port:
              number: %d
`
	ingressTemplateHTTPS = `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  annotations:
    nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
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
            name: %s-%s
            port:
              number: %d
`
)

// UpdateVMClusterSpec fetches the named VMCluster, applies mutate to its Spec,
// and saves the result back to the API server. The update is retried on conflict
// using the standard Kubernetes retry policy. After a successful update the
// function waits for the cluster to return to operational status.
// If ctx is cancelled before or during the operation the function returns
// silently, allowing it to be used inside a goroutine that is stopped via
// context cancellation.
func UpdateVMClusterSpec(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, clusterName string, client vmclient.Interface, mutate func(*vmv1beta1.VMClusterSpec)) {
	updateVMClusterSpec(ctx, t, namespace, clusterName, client, mutate)
	WaitForVMClusterToBeOperational(ctx, t, kubeOpts, namespace, client)
}

// UpdateVMClusterSpecNoWait fetches the named VMCluster, applies mutate to its Spec,
// and saves the result back to the API server without waiting for the cluster to
// become operational. Use this when the VMCluster uses OnDelete update strategy and
// pods are intentionally not restarted between spec changes.
func UpdateVMClusterSpecNoWait(ctx context.Context, t terratesting.TestingT, namespace, clusterName string, client vmclient.Interface, mutate func(*vmv1beta1.VMClusterSpec)) {
	updateVMClusterSpec(ctx, t, namespace, clusterName, client, mutate)
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
	tmpl := ingressTemplate
	if https {
		tmpl = ingressTemplateHTTPS
	}
	ingress := fmt.Sprintf(tmpl, ingressName, serviceName, namespace, consts.NginxHost(), serviceName, clusterName, servicePort)
	KubectlApplyFromString(ctx, t, kubeOpts, ingress)
}

func ExposeVMInsertAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, readiness vmclusterIngressReadiness) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, readiness.ClusterName, "vminsert", 8480, readiness.VMInsertHTTPS)
	// mTLS requires a client certificate; nginx cannot provide one, so skip the health check.
	if readiness.VMInsertMTLS {
		return
	}
	scheme := "http"
	if readiness.VMInsertHTTPS {
		scheme = "https"
	}
	waitForHTTPRoute(ctx, t, fmt.Sprintf("%s://%s/health", scheme, consts.VMInsertHost(namespace)))
}

func ExposeVMSelectAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, readiness vmclusterIngressReadiness) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, readiness.ClusterName, "vmselect", 8481, readiness.VMSelectHTTPS)
	// mTLS requires a client certificate; nginx cannot provide one, so skip the health check.
	if readiness.VMSelectMTLS {
		return
	}
	scheme := "http"
	if readiness.VMSelectHTTPS {
		scheme = "https"
	}
	waitForHTTPRoute(ctx, t, fmt.Sprintf("%s://%s/select/0/prometheus/api/v1/query?query=%s", scheme, consts.VMSelectHost(namespace), url.QueryEscape("1")))
}
