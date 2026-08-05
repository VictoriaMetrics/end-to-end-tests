package install

import (
	"context"
	"encoding/json"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	operatorv1 "github.com/VictoriaMetrics/operator/api/operator/v1"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const defaultVLAgentName = "vlagent"

func baseVLAgentJSON() ([]byte, error) {
	agent := &operatorv1.VLAgent{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "operator.victoriametrics.com/v1",
			Kind:       "VLAgent",
		},
		ObjectMeta: metav1.ObjectMeta{Name: defaultVLAgentName},
		Spec: operatorv1.VLAgentSpec{
			ComponentVersion: consts.VLEnterpriseVersion(),
			RemoteWrite:      []operatorv1.VLAgentRemoteWriteSpec{},
		},
	}
	return json.Marshal(agent)
}

// InstallVLAgent deploys VLAgent forwarding JSON logs to a mTLS-protected VLInsert.
func InstallVLAgent(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface, remoteWriteURL, tlsSecretName string) {
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
	}

	secretRef := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: tlsSecretName}, Key: key}
	}
	data, err := baseVLAgentJSON()
	require.NoError(t, err, "failed to marshal VLAgent manifest")
	ensureLicenseSecret(t, kubeOpts, namespace)
	var agent operatorv1.VLAgent
	require.NoError(t, json.Unmarshal(data, &agent), "failed to unmarshal VLAgent manifest")
	agent.Spec.RemoteWrite = []operatorv1.VLAgentRemoteWriteSpec{{
		URL: remoteWriteURL,
		TLSConfig: &operatorv1.TLSConfig{
			CASecret:   secretRef("ca.crt"),
			CertSecret: secretRef("client.crt"),
			KeySecret:  secretRef("client.key"),
		},
	}}
	data, err = json.Marshal(&agent)
	require.NoError(t, err, "failed to marshal VLAgent manifest")
	for _, patch := range appendLicensePatch(t, []jsonpatch.Patch{}) {
		data, err = patch.Apply(data)
		require.NoError(t, err, "failed to apply VLAgent license patch")
	}

	helpers.Logf("Installing VLAgent in namespace %s", namespace)
	KubectlApplyFromString(ctx, t, kubeOpts, string(data))
	WaitForVLAgentToBeOperational(ctx, t, kubeOpts, namespace, vmc)
}

// WaitForVLAgentToBeOperational waits until operator reports VLAgent operational.
func WaitForVLAgentToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface) {
	helpers.WaitForOperational(ctx, t, kubeOpts, consts.ResourceWaitTimeout, "VLAgent", namespace, func(fctx context.Context) ([]helpers.ResourceStatus, error) {
		list, err := vmc.OperatorV1().VLAgents(namespace).List(fctx, metav1.ListOptions{})
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

// DeleteVLAgent deletes named VLAgent resource.
func DeleteVLAgent(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, name string) {
	helpers.Logf("Deleting VLAgent %s", name)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vlagent", name, "--ignore-not-found=true")
}
