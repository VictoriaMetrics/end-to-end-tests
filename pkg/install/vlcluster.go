package install

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	. "github.com/onsi/ginkgo/v2" //nolint
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

// InstallVLCluster installs victoria-logs-cluster into the given namespace and waits for its
// vlinsert/vlselect ingress routes to become ready.
//
// affinity, when non-nil, is marshaled to JSON and applied to the vlinsert/vlselect/vlstorage
// component values (see tests.VLClusterAffinity) to co-locate/isolate the cluster's pods for
// chaos testing.
//
// Returns (insertURL, selectURL).
func InstallVLCluster(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, releaseName string, affinity map[string]interface{}) (string, string) {
	upgradeArgs := []string{"--create-namespace", "--wait", "--timeout", "10m"}
	if v := consts.VLClusterChartVersion(); v != "" {
		upgradeArgs = append(upgradeArgs, "--version", v)
	}

	setValues := map[string]string{
		"vlinsert.ingress.enabled":          "true",
		"vlinsert.ingress.ingressClassName": "nginx",
		"vlinsert.ingress.hosts[0].name":    consts.VLInsertHost(namespace),
		"vlinsert.ingress.hosts[0].path[0]": "/",
		"vlselect.ingress.enabled":          "true",
		"vlselect.ingress.ingressClassName": "nginx",
		"vlselect.ingress.hosts[0].name":    consts.VLSelectHost(namespace),
		"vlselect.ingress.hosts[0].path[0]": "/",
	}
	if v := consts.VLVersion(); v != "" {
		setValues["vlinsert.image.tag"] = v
		setValues["vlselect.image.tag"] = v
		setValues["vlstorage.image.tag"] = v
	}

	var setJSONValues map[string]string
	if affinity != nil {
		affinityJSON, err := json.Marshal(affinity)
		require.NoError(t, err)
		setJSONValues = map[string]string{
			"vlinsert.affinity":  string(affinityJSON),
			"vlselect.affinity":  string(affinityJSON),
			"vlstorage.affinity": string(affinityJSON),
		}
	}

	helmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		SetValues:      setValues,
		SetJSONValues:  setJSONValues,
		ExtraArgs:      map[string][]string{"upgrade": upgradeArgs},
	}

	By(fmt.Sprintf("Install %s as %s in %s", consts.VictoriaLogsClusterChart, releaseName, namespace))
	if err := helm.UpgradeE(t, helmOpts, consts.VictoriaLogsClusterChart, releaseName); err != nil {
		t.Fatalf("Failed to install chart %s: %v", consts.VictoriaLogsClusterChart, err)
	}

	insertURL := consts.VLInsertUrl(namespace)
	selectURL := consts.VLSelectUrl(namespace)
	WaitForHTTPRoute(ctx, t, insertURL+"/health")
	WaitForHTTPRoute(ctx, t, selectURL+"/health")
	return insertURL, selectURL
}

// DeleteVLCluster uninstalls the victoria-logs-cluster Helm release.
func DeleteVLCluster(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, releaseName string) {
	opts := &helm.Options{KubectlOptions: kubeOpts}
	_ = helm.DeleteE(t, opts, releaseName, true)
}
