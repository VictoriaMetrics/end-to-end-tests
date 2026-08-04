package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// baseVMAgentJSON builds a minimal VMAgent CR with only the image set from consts.
// It does NOT clone the monitoring VMAgent to avoid inheriting scrape configs that
// produce high-label metrics (e.g. kube-state-metrics) which get rejected by vminsert.
// Callers must supply their own remoteWrite and any extra configuration via patches.
func baseVMAgentJSON(t terratesting.TestingT) []byte {
	agent := &vmv1beta1.VMAgent{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "operator.victoriametrics.com/v1beta1",
			Kind:       "VMAgent",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "vmagent",
		},
		Spec: vmv1beta1.VMAgentSpec{
			CommonAppsParams: vmv1beta1.CommonAppsParams{
				Image: vmv1beta1.Image{
					Repository: consts.VMAgentDefaultImage(),
					Tag:        consts.VMAgentDefaultVersion(),
				},
			},
			RemoteWrite: []vmv1beta1.VMAgentRemoteWriteSpec{},
		},
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err, "failed to marshal base VMAgent to JSON")
	return data
}

// InstallVMAgent deploys a VMAgent into the specified namespace using the k8s-stack
// VMAgent as the base spec (same image/version), then applies caller-supplied patches.
func InstallVMAgent(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface, jsonPatches []jsonpatch.Patch) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}
	ensureLicenseSecret(t, kubeOpts, namespace)
	jsonPatches = appendLicensePatch(t, jsonPatches)

	vmagentJson := baseVMAgentJSON(t)

	var err error
	for _, patch := range jsonPatches {
		vmagentJson, err = patch.Apply(vmagentJson)
		require.NoError(t, err, "failed to apply patch")
	}

	helpers.Logf("Installing VMAgent in namespace %s", namespace)
	KubectlApplyFromString(ctx, t, kubeOpts, string(vmagentJson))

	WaitForVMAgentToBeOperational(ctx, t, kubeOpts, namespace, vmc)

	ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)
}

// ApplyVMAgentWithPatches deploys a named VMAgent into the specified namespace using
// the k8s-stack VMAgent as the base spec, then applies caller-supplied patches.
// The patches must set /metadata/name to the desired CR name.
func ApplyVMAgentWithPatches(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface, name string, jsonPatches []jsonpatch.Patch) {
	ensureLicenseSecret(t, kubeOpts, namespace)
	jsonPatches = appendLicensePatch(t, jsonPatches)

	vmagentJson := baseVMAgentJSON(t)

	var err error
	for _, patch := range jsonPatches {
		vmagentJson, err = patch.Apply(vmagentJson)
		require.NoError(t, err, "failed to apply patch")
	}

	helpers.Logf("Installing VMAgent %s in namespace %s", name, namespace)
	KubectlApplyFromString(ctx, t, kubeOpts, string(vmagentJson))
	WaitForVMAgentToBeOperational(ctx, t, kubeOpts, namespace, vmc)

	ExposeNamedVMAgentAsIngress(ctx, t, kubeOpts, namespace, name)
}

// ExposeNamedVMAgentAsIngress creates an Ingress for a VMAgent with a custom CR name.
// The ingress name is "<name>-ingress", the host is "<name>-<namespace>.<nginxHost>.nip.io",
// and the backend service is "vmagent-<name>".
func ExposeNamedVMAgentAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, name string) {
	vmagentYaml, err := os.ReadFile(consts.OverwatchVMSingleIngress())
	require.NoError(t, err)

	docJson, err := yaml.YAMLToJSON(vmagentYaml)
	require.NoError(t, err)

	ingressName := name + "-ingress"
	host := consts.VMAgentNamedHost(name, namespace)
	serviceName := "vmagent-" + name

	patchOps := []PatchOp{
		{Op: "replace", Path: "/metadata/name", Value: ingressName},
		{Op: "add", Path: "/metadata/namespace", Value: namespace},
		{Op: "replace", Path: "/spec/rules/0/host", Value: host},
		{Op: "replace", Path: "/spec/rules/0/http/paths/0/backend/service/name", Value: serviceName},
	}

	patchObj, err := CreateJsonPatch(patchOps)
	require.NoError(t, err)
	docJson, err = patchObj.Apply(docJson)
	require.NoError(t, err)

	KubectlApplyFromString(ctx, t, kubeOpts, string(docJson))
	WaitForHTTPRoute(ctx, t, fmt.Sprintf("http://%s/health", host))
}

// ExposeVMAgentAsIngress creates an Ingress resource to expose the VMAgent instance.
//
// It reads the ingress template from "../../manifests/overwatch/vmsingle-ingress.yaml",
// replaces the host placeholder with the configured VMAgent host, and applies it.
func ExposeVMAgentAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	// Copy vmsingle-ingress.yaml to temp file, update ingress host and apply it
	vmagentYaml, err := os.ReadFile(consts.OverwatchVMSingleIngress())
	require.NoError(t, err)

	docJson, err := yaml.YAMLToJSON(vmagentYaml)
	require.NoError(t, err)

	host := consts.VMAgentNamespacedHost(namespace)

	patchOps := []PatchOp{
		{
			Op:    "replace",
			Path:  "/metadata/name",
			Value: "vmagent-ingress",
		},
		{
			Op:    "add",
			Path:  "/metadata/namespace",
			Value: namespace,
		},
		{
			Op:    "replace",
			Path:  "/spec/rules/0/host",
			Value: host,
		},
		{
			Op:    "replace",
			Path:  "/spec/rules/0/http/paths/0/backend/service/name",
			Value: "vmagent-vmagent",
		},
	}

	patchObj, err := CreateJsonPatch(patchOps)
	require.NoError(t, err)
	docJson, err = patchObj.Apply(docJson)
	require.NoError(t, err)

	KubectlApplyFromString(ctx, t, kubeOpts, string(docJson))
	WaitForHTTPRoute(ctx, t, fmt.Sprintf("http://%s/health", host))
}

var vmAgentUpdateMu sync.Mutex

func lockVMAgentUpdates() func() {
	vmAgentUpdateMu.Lock()
	return vmAgentUpdateMu.Unlock
}

// EnsureVMAgentRemoteWriteURL ensures that the specified VMAgent contains a remoteWrite
// entry with the provided URL. If no remoteWrite entries exist or the provided URL is
// not present, the function appends a remoteWrite entry with that URL and updates the
// VMAgent resource in Kubernetes.
//
// This helper is intended for use in end-to-end tests to guarantee that a VMAgent is
// configured to forward data to a particular remote endpoint (for example, a VMSingle
// instance used in overwatch tests).
//     directly for API calls here but kept for symmetry with other helpers).
//   - namespace: Kubernetes namespace where the VMAgent CR lives.
//   - vmAgentName: name of the VMAgent custom resource to inspect and potentially update.
//   - url: the remoteWrite URL that must be present in the VMAgent configuration.

func EnsureVMAgentRemoteWriteURL(ctx context.Context, t terratesting.TestingT, vmclient vmclient.Interface, kubeOpts *k8s.KubectlOptions, namespace, vmAgentName, url string) {
	unlock := lockVMAgentUpdates()
	defer unlock()

	// Get the VMAgent resource
	vmAgent, err := vmclient.OperatorV1beta1().VMAgents(namespace).Get(ctx, vmAgentName, metav1.GetOptions{})
	require.NoError(t, err)

	// Check if remoteWrite is configured and has at least one URL
	if vmAgent == nil || len(vmAgent.Spec.RemoteWrite) == 0 {
		t.Errorf("VMAgent %s in namespace %s does not have any remoteWrite configuration", vmAgentName, namespace)
		return
	}

	// Validate that at least one remoteWrite entry has a URL
	found := false
	for _, rw := range vmAgent.Spec.RemoteWrite {
		if rw.URL == url {
			found = true
			break
		}
	}
	if !found {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Get the fresh VMAgent resource version as it may have been updated by another test
			vmAgent, err := vmclient.OperatorV1beta1().VMAgents(namespace).Get(ctx, vmAgentName, metav1.GetOptions{})
			if err != nil {
				return err
			}

			// Check again if the URL is already present to avoid duplicates during retries
			for _, rw := range vmAgent.Spec.RemoteWrite {
				if rw.URL == url {
					return nil
				}
			}

			vmAgent.Spec.RemoteWrite = append(vmAgent.Spec.RemoteWrite, vmv1beta1.VMAgentRemoteWriteSpec{
				URL: url,
			})
			_, err = vmclient.OperatorV1beta1().VMAgents(namespace).Update(ctx, vmAgent, metav1.UpdateOptions{})
			return err
		})
		require.NoError(t, err)
		WaitForVMAgentToBeOperational(ctx, t, kubeOpts, namespace, vmclient)
	}
}

// WaitForVMAgentToBeOperational watches the VMAgent custom resource in the given
// namespace and blocks until the agent reports an operational update status.
//
// The function polls VMAgent objects until Status.UpdateStatus becomes Operational
// or the timeout (consts.ResourceWaitTimeout) expires. Polling (rather than a raw
// watch) is used because the API server/proxy can silently close a long-lived watch
// connection before the resource becomes ready, which would otherwise surface as a
// spurious hang/failure with no useful error.
//     on stuck image pulls.
//   - namespace: the Kubernetes namespace where the VMAgent CR is located.
//   - vmclient: client for interacting with VictoriaMetrics Operator CRDs.
func WaitForVMAgentToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface) {
	waitForOperational(ctx, t, kubeOpts, consts.ResourceWaitTimeout, "VMAgent", namespace, func(fctx context.Context) ([]resourceStatus, error) {
		list, err := vmclient.OperatorV1beta1().VMAgents(namespace).List(fctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		result := make([]resourceStatus, len(list.Items))
		for i := range list.Items {
			result[i] = resourceStatus{Name: list.Items[i].Name, Status: list.Items[i].Status.UpdateStatus, Reason: list.Items[i].Status.Reason}
		}
		return result, nil
	})
}

// DeleteVMAgent deletes the specified VMAgent resource from the cluster.
// It ignores "not found" errors.
func DeleteVMAgent(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmagentName string) {
	// Delete the VMAgent resource
	helpers.Logf("Deleting VMAgent %s", vmagentName)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vmagent", vmagentName, "--ignore-not-found=true")
}
