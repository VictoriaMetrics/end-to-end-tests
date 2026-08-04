package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

type vlclusterIngressReadiness struct {
	ClusterName   string
	VLInsertHTTPS bool
	VLSelectHTTPS bool
}

type vlclusterReadinessSpec struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		VLInsert struct {
			ExtraArgs map[string]string `json:"extraArgs"`
		} `json:"vlinsert"`
		VLSelect struct {
			ExtraArgs map[string]string `json:"extraArgs"`
		} `json:"vlselect"`
	} `json:"spec"`
}

// buildVLClusterImagePatch creates a JSON patch that sets the VictoriaLogs image tag shared by
// all VLCluster components, based on the version configured via test flags. If no version is
// configured the operator default still applies.
func buildVLClusterImagePatch() (jsonpatch.Patch, error) {
	v := consts.VLVersion()
	if v == "" {
		return jsonpatch.Patch{}, nil
	}
	ops := []PatchOp{{Op: "add", Path: "/spec/clusterVersion", Value: v}}
	return CreateJsonPatch(ops)
}

// InstallVLCluster installs a VLCluster custom resource into the target namespace.
//
// The function ensures the namespace exists, reads a VLCluster template manifest from the
// repository manifests, applies jsonPatches (e.g. to set the resource name and pod affinity),
// applies the manifest to the cluster, waits for the VLCluster to become operational, and
// exposes vlinsert/vlselect via ingress.
func InstallVLCluster(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vlclient vmclient.Interface, jsonPatches []jsonpatch.Patch, operationalTimeout time.Duration) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}

	// Read VLCluster and patch it
	vlclusterYamlPath := consts.ManifestsRoot() + "/overwatch/vlcluster.yaml"
	vlclusterYaml, err := os.ReadFile(vlclusterYamlPath)
	require.NoError(t, err, "failed to read VLCluster YAML")

	vlclusterJson, err := yaml.YAMLToJSON(vlclusterYaml)
	require.NoError(t, err, "failed to convert VLCluster YAML to JSON")

	// Apply the explicit image version from test flags before caller patches so that the
	// VLCluster does not depend on operator default env vars being up-to-date.
	imagePatch, err := buildVLClusterImagePatch()
	require.NoError(t, err, "failed to build VLCluster image patch")
	if len(imagePatch) > 0 {
		vlclusterJson, err = imagePatch.Apply(vlclusterJson)
		require.NoError(t, err, "failed to apply VLCluster image patch")
	}

	for _, patch := range jsonPatches {
		vlclusterJson, err = patch.Apply(vlclusterJson)
		require.NoError(t, err, "failed to apply patch")
	}
	readiness := vlclusterIngressReadinessFromSpec(t, vlclusterJson)

	// Apply the VLCluster manifest
	helpers.Logf("Installing VLCluster in namespace %s", namespace)
	vlclusterString := string(vlclusterJson)
	KubectlApplyFromString(ctx, t, kubeOpts, vlclusterString)

	// Wait for VLCluster to become operational
	helpers.Logf("Waiting for VLCluster to become operational in namespace %s", namespace)
	WaitForVLClusterToBeOperational(ctx, t, kubeOpts, namespace, vlclient, operationalTimeout)

	// Wait only for VLCluster pods. The namespace may contain completed job pods, which
	// never become Ready again and would make a namespace-wide wait fail.
	k8s.RunKubectlContext(t, ctx, kubeOpts, "wait", "--for=condition=Ready", "pods",
		"-l", fmt.Sprintf("managed-by=vm-operator,app.kubernetes.io/instance=%s", readiness.ClusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))

	// Expose VLInsert and VLSelect as ingress
	helpers.Logf("Configuring VLInsert ingress in namespace %s, https %t", namespace, readiness.VLInsertHTTPS)
	ExposeVLInsertAsIngress(ctx, t, kubeOpts, namespace, readiness)

	helpers.Logf("Configuring VLSelect ingress in namespace %s, https %t", namespace, readiness.VLSelectHTTPS)
	ExposeVLSelectAsIngress(ctx, t, kubeOpts, namespace, readiness)
}

func vlclusterIngressReadinessFromSpec(t terratesting.TestingT, vlclusterJSON []byte) vlclusterIngressReadiness {
	var spec vlclusterReadinessSpec
	require.NoError(t, json.Unmarshal(vlclusterJSON, &spec), "failed to parse VLCluster JSON")

	return vlclusterIngressReadiness{
		ClusterName:   spec.Metadata.Name,
		VLInsertHTTPS: spec.Spec.VLInsert.ExtraArgs["tls"] == "true",
		VLSelectHTTPS: spec.Spec.VLSelect.ExtraArgs["tls"] == "true",
	}
}

// ExposeVLInsertAsIngress creates an ingress for the VLInsert service and waits for it to serve
// health checks.
func ExposeVLInsertAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, readiness vlclusterIngressReadiness) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, readiness.ClusterName, "vlinsert", 9481, readiness.VLInsertHTTPS)
	scheme := "http"
	if readiness.VLInsertHTTPS {
		scheme = "https"
	}
	helpers.WaitForHTTPRoute(ctx, t, fmt.Sprintf("%s://%s/health", scheme, consts.VLInsertHost(namespace)))
}

// ExposeVLSelectAsIngress creates an ingress for the VLSelect service and waits for it to serve
// health checks.
func ExposeVLSelectAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, readiness vlclusterIngressReadiness) {
	exposeServiceAsIngress(ctx, t, kubeOpts, namespace, readiness.ClusterName, "vlselect", 9471, readiness.VLSelectHTTPS)
	scheme := "http"
	if readiness.VLSelectHTTPS {
		scheme = "https"
	}
	helpers.WaitForHTTPRoute(ctx, t, fmt.Sprintf("%s://%s/health", scheme, consts.VLSelectHost(namespace)))
}

// WaitForVLClusterToBeOperational polls a VLCluster custom resource until it reports an
// operational status. Mirrors WaitForVMClusterToBeOperational.
func WaitForVLClusterToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vlclient vmclient.Interface, timeout time.Duration) {
	helpers.WaitForOperational(ctx, t, kubeOpts, timeout, "VLCluster", namespace, func(fctx context.Context) ([]helpers.ResourceStatus, error) {
		list, err := vlclient.OperatorV1().VLClusters(namespace).List(fctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		result := make([]helpers.ResourceStatus, len(list.Items))
		for i := range list.Items {
			result[i] = helpers.ResourceStatus{Name: list.Items[i].Name, Status: list.Items[i].Status.UpdateStatus, Reason: list.Items[i].Status.Reason}
		}
		return result, nil
	})
}

// DeleteVLCluster deletes the named VLCluster resource and waits for the corresponding
// deployments (vlinsert, vlselect) and statefulset (vlstorage) to be removed from the cluster.
func DeleteVLCluster(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vlclusterName string) {
	helpers.Logf("Deleting VLCluster %s", vlclusterName)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vlcluster", vlclusterName, "--ignore-not-found=true")

	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "deployment", fmt.Sprintf("vlinsert-%s", vlclusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "deployment", fmt.Sprintf("vlselect-%s", vlclusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "wait", "--for=delete", "statefulset", fmt.Sprintf("vlstorage-%s", vlclusterName),
		fmt.Sprintf("--timeout=%s", consts.VMClusterWaitTimeout))
}
