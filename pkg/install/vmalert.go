package install

import (
	"context"
	"encoding/json"
	"fmt"
	"k8s.io/client-go/util/retry"
	"maps"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, getErr := vmclient.OperatorV1beta1().VMAlerts(namespace).Get(ctx, releaseName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		latest.Spec.Datasource.URL = overwatchSvcURL
		latest.Spec.RemoteRead.URL = overwatchSvcURL
		_, updateErr := vmclient.OperatorV1beta1().VMAlerts(namespace).Update(ctx, latest, metav1.UpdateOptions{})
		return updateErr
	})
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
	helpers.WaitForOperational(ctx, t, kubeOpts, consts.ResourceWaitTimeout, "VMAlert", namespace, func(fctx context.Context) ([]helpers.ResourceStatus, error) {
		list, err := vmclient.OperatorV1beta1().VMAlerts(namespace).List(fctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		result := make([]helpers.ResourceStatus, len(list.Items))
		for i := range list.Items {
			result[i] = helpers.ResourceStatus{Name: list.Items[i].Name, Status: list.Items[i].Status.UpdateStatus, Reason: list.Items[i].Status.Reason}
		}
		return result, nil
	}, helpers.TransientWebhookFailure)
}

// vmAlertRulesConfigMap holds the rules of a test VMAlert. Rules are mounted from a
// ConfigMap instead of VMRule CRs so the overwatch VMAlert never picks them up.
const vmAlertRulesConfigMap = "vmalert-e2e-rules"

// InstallVMAlert deploys VMAlert "vmalert" evaluating groups against tenant 0 of the
// namespace's VMCluster and writing results (recording rules, ALERTS) back to it.
// Notifications go to a blackhole: tests check ALERTS series, not Alertmanager.
func InstallVMAlert(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, clusterName string, vmc vmclient.Interface, groups []vmv1beta1.RuleGroup, extraArgs map[string]string) {
	ensureLicenseSecret(t, kubeOpts, namespace)

	// Rules ConfigMap, mounted by the operator at /etc/vm/configs/<name>/
	rules, err := yaml.Marshal(map[string][]vmv1beta1.RuleGroup{"groups": groups})
	require.NoError(t, err)
	cm, err := json.Marshal(&corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: vmAlertRulesConfigMap, Namespace: namespace},
		Data:       map[string]string{"rules.yaml": string(rules)},
	})
	require.NoError(t, err)
	KubectlApplyFromStringWithRetry(ctx, t, kubeOpts, string(cm))

	args := map[string]string{"notifier.blackhole": "true"}
	maps.Copy(args, extraArgs)
	alert := &vmv1beta1.VMAlert{
		TypeMeta:   metav1.TypeMeta{APIVersion: "operator.victoriametrics.com/v1beta1", Kind: "VMAlert"},
		ObjectMeta: metav1.ObjectMeta{Name: "vmalert", Namespace: namespace},
		Spec: vmv1beta1.VMAlertSpec{
			CommonAppsParams: vmv1beta1.CommonAppsParams{
				Image: vmv1beta1.Image{
					Repository: consts.VMAlertDefaultImage(),
					Tag:        consts.VMAlertDefaultVersion(),
				},
				Resources:    vmAgentResources,
				NodeSelector: monitoringNodeSelector,
				Tolerations:  []corev1.Toleration{monitoringToleration},
				ConfigMaps:   []string{vmAlertRulesConfigMap},
				ExtraArgs:    args,
			},
			RulePath: []string{fmt.Sprintf("%s/%s/*.yaml", vmv1beta1.ConfigMapsDir, vmAlertRulesConfigMap)},
			Datasource: vmv1beta1.VMAlertDatasourceSpec{
				URL: fmt.Sprintf("http://%s/select/0/prometheus", consts.GetVMSelectSvc(clusterName, namespace)),
			},
			RemoteWrite: &vmv1beta1.VMAlertRemoteWriteSpec{
				URL: fmt.Sprintf("http://%s/insert/0/prometheus", consts.GetVMInsertSvc(clusterName, namespace)),
			},
		},
	}
	data, err := json.Marshal(alert)
	require.NoError(t, err)
	for _, patch := range appendLicensePatch(t, nil) {
		data, err = patch.Apply(data)
		require.NoError(t, err)
	}

	helpers.Logf("Installing VMAlert in namespace %s", namespace)
	KubectlApplyFromStringWithRetry(ctx, t, kubeOpts, string(data))
	WaitForVMAlertToBeOperational(ctx, t, kubeOpts, namespace, vmc)
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
