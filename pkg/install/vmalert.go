package install

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
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
				require.NoError(t, fmt.Errorf("timed out waiting for VMAlert in namespace %s to become operational", namespace))
			}
			return
		case <-ticker.C:
			if pullErr := checkForImagePullErrors(timeBoundContext, t, kubeOpts); pullErr != nil {
				require.NoError(t, pullErr)
				return
			}

			list, err := vmclient.OperatorV1beta1().VMAlerts(namespace).List(timeBoundContext, metav1.ListOptions{})
			if err != nil {
				continue
			}
			for i := range list.Items {
				vmAlert := &list.Items[i]
				switch vmAlert.Status.UpdateStatus {
				case vmv1beta1.UpdateStatusOperational:
					return
				case vmv1beta1.UpdateStatusFailed:
					reason := strings.TrimSpace(vmAlert.Status.Reason)
					if reason == "" {
						reason = "unknown reason"
					}
					require.NoError(t, fmt.Errorf("VMAlert %s/%s entered failed state: %s",
						namespace, vmAlert.Name, reason))
					return
				}
			}
		}
	}
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
	KubectlApplyFromString(ctx, t, kubeOpts, string(docJson))
}
