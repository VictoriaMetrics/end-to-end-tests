package install

import (
	"context"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// ReconfigureVMAlert is setting RemoteRead / RemoteWrite to VMSingle namespace
func ReconfigureVMAlert(ctx context.Context, t terratesting.TestingT, namespace, releaseName, overwatchURL string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	kubeConfigPath, err := kubeOpts.GetConfigPath(t)
	require.NoError(t, err)
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath}, &clientcmd.ConfigOverrides{})
	restConfig, err := clientConfig.ClientConfig()
	require.NoError(t, err)
	vmclient := vmclient.NewForConfigOrDie(restConfig)
	require.NoError(t, err)

	vmAlert, err := vmclient.OperatorV1beta1().VMAlerts(namespace).Get(ctx, releaseName, metav1.GetOptions{})
	require.NoError(t, err)

	overwatchSvcURL := fmt.Sprintf("http://%s/", overwatchURL)
	vmAlert.Spec.Datasource.URL = overwatchSvcURL
	vmAlert.Spec.RemoteRead.URL = overwatchSvcURL
	_, err = vmclient.OperatorV1beta1().VMAlerts(namespace).Update(ctx, vmAlert, metav1.UpdateOptions{})
	require.NoError(t, err)

}

// WaitForVMAlertToBeOperational polls a VMAlert custom resource until it reports an operational status.
//
// The function polls VMAlert objects in the provided namespace until the VMAlert's
// Status.UpdateStatus becomes UpdateStatusOperational or the wait times out. It uses
// consts.ResourceWaitTimeout to bound the wait. Polling (rather than a raw watch) is
// used because the API server/proxy can silently close a long-lived watch connection
// before the resource becomes ready, which would otherwise surface as a spurious
// hang/failure with no useful error.
func WaitForVMAlertToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface) {
	waitForOperational(ctx, t, kubeOpts, consts.ResourceWaitTimeout, "VMAlert", namespace, func(fctx context.Context) ([]resourceStatus, error) {
		list, err := vmclient.OperatorV1beta1().VMAlerts(namespace).List(fctx, metav1.ListOptions{})
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

// AddCustomAlertRules creates a VMRule with custom alerts
func AddCustomAlertRules(ctx context.Context, t terratesting.TestingT, namespace string) {
	manifestPath := consts.ManifestsRoot() + "/components/custom-alerts.yaml"
	manifest, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	docJson, err := yaml.YAMLToJSON(manifest)
	require.NoError(t, err)

	patchOps := []PatchOp{
		{
			Op:    "replace",
			Path:  "/metadata/namespace",
			Value: namespace,
		},
	}
	patch, err := CreateJsonPatch(patchOps)
	require.NoError(t, err)

	docJson, err = patch.Apply(docJson)
	require.NoError(t, err)

	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	KubectlApplyFromString(ctx, t, kubeOpts, string(docJson))
}
