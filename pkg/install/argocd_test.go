package install

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildArgoCDHelmApplication(t *testing.T) {
	manifest, err := BuildArgoCDHelmApplication("operator", "monitoring", "victoria-metrics-k8s-stack", "0.90.2", map[string]string{
		"victoria-metrics-operator.serviceAccount.name": "operator-test",
		"victoria-metrics-operator.env[0].name":         "WATCH_NAMESPACE",
		"victoria-metrics-operator.env[0].value":        "monitoring",
	})
	require.NoError(t, err)
	for _, expected := range []string{
		"kind: Application",
		"name: operator",
		"namespace: argocd",
		"chart: victoria-metrics-k8s-stack",
		"targetRevision: 0.90.2",
		"argocd.argoproj.io/compare-options: ServerSideDiff=false",
		"victoria-metrics-operator.serviceAccount.name",
		"WATCH_NAMESPACE",
	} {
		require.Contains(t, manifest, expected)
	}
	require.NotContains(t, strings.ToLower(manifest), "repo: local")
	require.Contains(t, manifest, "ServerSideApply=true")
}

func TestBuildArgoCDHelmApplicationWithoutSSA(t *testing.T) {
	manifest, err := BuildArgoCDHelmApplicationWithoutSSA("operator", "monitoring", "victoria-metrics-operator", "0.67.2", nil)
	require.NoError(t, err)
	require.NotContains(t, manifest, "ServerSideApply=true")
}

func TestArgoCDInstallURL(t *testing.T) {
	require.Equal(t,
		"https://raw.githubusercontent.com/argoproj/argo-cd/v3.5.1/manifests/install.yaml",
		fmt.Sprintf(argocdInstallURLTemplate, "v3.5.1"),
	)
}

func TestDeleteArgoCDApplicationWithNilKubectlOptions(t *testing.T) {
	DeleteArgoCDApplication(t, nil, "operator")
}
