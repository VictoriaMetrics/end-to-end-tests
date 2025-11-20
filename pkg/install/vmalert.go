package install

import (
	"context"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/clientcmd"
	watchtools "k8s.io/client-go/tools/watch"
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

// WaitForVMAlertToBeOperational watches a VMAlert custom resource until it reports an operational status.
//
// The function sets up a watch for VMAlert objects in the provided namespace and
// blocks until the VMAlert's Status.UpdateStatus becomes UpdateStatusOperational or
// the wait times out. It uses consts.ResourceWaitTimeout to bound the wait.
func WaitForVMAlertToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface) {
	watchInterface, err := vmclient.OperatorV1beta1().VMAlerts(namespace).Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	defer watchInterface.Stop()

	timeBoundContext, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	defer cancel()

	_, err = watchtools.UntilWithoutRetry(timeBoundContext, watchInterface, func(event watch.Event) (bool, error) {
		obj := event.Object
		vmAlert := obj.(*vmv1beta1.VMAlert)
		if vmAlert.Status.UpdateStatus == vmv1beta1.UpdateStatusOperational {
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err)
}

// AddCustomAlertRules creates a VMRule with custom alerts
func AddCustomAlertRules(ctx context.Context, t terratesting.TestingT, namespace string) {
	manifestPath := consts.ManifestsRoot() + "/custom-alerts.yaml"
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
	k8s.KubectlApplyFromString(t, kubeOpts, string(docJson))
}
